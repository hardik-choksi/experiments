# Go's G-M-P Scheduler Model — Deep Dive

## History: the old G-M model and why it failed

Before Go 1.1 (pre-2012), Go had only G (goroutines) and M (OS threads). There was **one global run queue** protected by a single mutex. Every goroutine creation, completion, or reschedule required acquiring that global lock.

Problems:
- **Lock contention**: all Ms fought over one mutex. Throughput collapsed under high goroutine churn.
- **Poor cache locality**: a goroutine unblocked by a channel send was placed in the global queue. Whichever M picked it up next was random — the goroutine's data was cold in that core's cache.
- **Excessive thread hand-offs**: when a goroutine blocked in a syscall, its M blocked with it. A new M had to be created/woken. When the syscall returned, the goroutine's context had to migrate to whatever M was available.

Dmitry Vyukov proposed the fix in 2012: add **P (Processor)** as a third entity. P holds a local run queue, eliminating the global lock for the common case. This became Go 1.1's scheduler.

## The three entities

### G — Goroutine

A G is a `runtime.g` struct (defined in `runtime/runtime2.go`). It holds:

| Field | What it is |
|---|---|
| `stack.lo`, `stack.hi` | Stack bounds |
| `sched` (gobuf) | Saved registers (SP, PC) when not running |
| `m` | Pointer to the M currently running this G (nil if parked) |
| `preempt` | Flag set by sysmon to request preemption |
| `stackguard0` | Poisoned by scheduler to trigger stack-check preemption |
| `goid` | Monotonically increasing goroutine ID |
| `_defer`, `_panic` | Defer/panic chains |
| `atomicstatus` | Current state (see below) |

**Goroutine states:**
```
_Gidle      → just allocated, not yet initialized
_Grunnable  → in a run queue, ready to execute
_Grunning   → currently executing on an M
_Gsyscall   → in a blocking syscall, not on a P
_Gwaiting   → parked (channel, mutex, netpoller, timer)
_Gdead      → finished, in free list for reuse
_Gpreempted → stopped by async preemption (Go 1.14+)
```

**Stack size:** Initial stack is **2 KB** (since Go 1.4). It grows dynamically by copying to a larger allocation (2x). Shrinks during GC if using less than 1/4 of current size. Maximum is 1 GB on 64-bit.

A G is extremely cheap: just a struct + a small stack. You can have millions of them.

### M — Machine (OS Thread)

An M is an actual OS thread created via `pthread_create`. The `runtime.m` struct holds:

| Field | What it is |
|---|---|
| `curg` | The G currently running on this M |
| `p` | The P this M is associated with (nil during syscall or idle) |
| `g0` | Special goroutine with a larger stack, used for scheduling operations |
| `gsignal` | Special G for handling OS signals on this thread |
| `spinning` | Whether this M is actively looking for work |

**How many Ms?** Not controlled by GOMAXPROCS. If 1000 goroutines are all blocked in syscalls, there can be 1000 Ms. The runtime creates new threads on demand up to `debug.SetMaxThreads` (default 10,000). Idle Ms go into `sched.midle` list for reuse — they're not destroyed immediately.

**The g0 goroutine:** Every M has a special `g0` with a larger stack. When the scheduler needs to run scheduling code (finding next goroutine, work stealing), it switches to g0's stack. All goroutine context switches go through g0. You never run user code on g0.

### P — Processor (the key innovation)

P is the "execution context" — a virtual processor. An M **must hold a P** to execute Go code. The `runtime.p` struct holds:

| Field | What it is |
|---|---|
| `runq [256]guintptr` | Local run queue — lock-free, fixed-size ring buffer |
| `runnext` | Single-slot priority queue (next goroutine to run) |
| `mcache` | Per-P memory allocation cache (no locking for small allocs!) |
| `deferpool` | Reusable defer structs |
| `timers` | Per-P timer heap (for `time.Sleep`, deadlines) |
| `status` | `_Pidle`, `_Prunning`, `_Psyscall`, `_Pgcstop`, `_Pdead` |
| `schedtick` | Incremented each schedule event (sysmon uses this to detect stuck goroutines) |
| `syscalltick` | Incremented on each syscall (sysmon uses this for handoff detection) |

**How many Ps?** Exactly `GOMAXPROCS` (default = `runtime.NumCPU()`). This is the actual parallelism control — it caps how many goroutines can run simultaneously, not how many OS threads exist.

**Why P matters:** Before P existed, all memory allocation went through a global heap lock. With P, each P has its own `mcache`, so most small allocations are lock-free. Same for the run queue — each P's local queue is only written by its own M (single-producer), so enqueue is lock-free.

## The big picture

```
┌────────────────────────────────────────────────────────────────┐
│                         Go Runtime                             │
├────────────────────────────────────────────────────────────────┤
│                                                                │
│  M1 (OS thread) ──→ P1 ──→ [G0, G1, G2, ...]  (local queue)  │
│                         └→ runnext: G9                         │
│                         └→ mcache (allocation cache)           │
│                         └→ timers (deadline heap)              │
│                                                                │
│  M2 (OS thread) ──→ P2 ──→ [G3, G4, G5, ...]                  │
│                                                                │
│  M3 (OS thread) ──→ P3 ──→ [G6, G7]                           │
│                                                                │
│  M4 (OS thread) ──→ (no P, blocked in syscall with G8)        │
│                                                                │
│  M_sysmon (OS thread, no P) ──→ netpoll, preemption, GC       │
│                                                                │
│  ┌─────────────────────────┐                                   │
│  │   Global Run Queue      │  ← overflow + starvation relief   │
│  │   [G10, G11, G12, ...]  │                                   │
│  └─────────────────────────┘                                   │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

**The critical invariant:** an M can only run Go code when it holds a P. No P = no running goroutines (except special runtime code like sysmon).

## Run queues: local vs global

### Local run queue (per P)

A `[256]guintptr` ring buffer using atomic head/tail pointers. Since only the owning M writes to the tail (single-producer) and others steal from the head (multi-consumer), the common path is entirely **lock-free**.

Each P also has `runnext` — a single-slot priority queue. When `go func()` creates a new goroutine, it goes into `runnext` (not the ring buffer). This gives newly created goroutines priority — they run next on the creating P, exploiting warm caches from the parent goroutine.

If `runnext` was already occupied, the displaced G moves to the ring buffer. If the ring buffer is full (256 entries), half the queue is batch-moved to the global run queue.

### Global run queue

One global queue protected by `sched.lock` (a mutex). It holds goroutines that:
- Overflowed from a P's local queue
- Were moved there from a P being retaken (syscall handoff)
- Came from the netpoller (goroutines woken by I/O readiness)

To prevent starvation, each P checks the global queue **every 61 scheduling events** (a prime number to avoid sync patterns — see [scheduler-fairness.md — Why 61](scheduler-fairness.md#why-schedtick-checks-the-global-queue-every-61-events-not-64) for the full explanation of why prime and why specifically 61). When it takes from the global queue, it takes a batch: `(global_queue_size / GOMAXPROCS) + 1`, amortizing the lock cost.

## Work stealing

When a P's local queue is empty and the global queue is empty, the M enters work stealing via `findRunnable()`:

```
1. Check own runnext and local queue (empty)
2. Check global queue (empty, or take a batch)
3. Check netpoller — call netpoll(0) non-blocking, get any ready goroutines
4. Pick a RANDOM victim P → steal HALF its local run queue
```

Step 4 is the core: `runqsteal()` atomically takes `n/2` goroutines from the victim's ring buffer head using CAS on `runqhead`. Random victim selection avoids herding — all idle Ps don't converge on the same victim.

If all Ps are empty and there's nothing from netpoll, the M releases its P (puts it in idle list), and parks itself. The P becomes `_Pidle`.

Work stealing is what makes Go's scheduling self-balancing: no central planner, just decentralized opportunistic stealing.

## How `go func()` works

When the compiler sees `go f(args)`, it emits a call to `runtime.newproc()`:

1. **Get a G:** Check P's local `gfree` list (free-list of dead Gs for reuse), then global `sched.gfree`. If both empty, allocate a new G on the heap.
2. **Initialize:** Copy `f` and its arguments onto the new G's stack. Set `pc` to point to `f`.
3. **Set status:** `_Grunnable`.
4. **Enqueue:** `runqput(p, newg, next=true)` — the new G goes into `p.runnext`. If `runnext` was occupied, the displaced G goes into the ring buffer (or overflows to global queue).
5. **Wake idle P:** If there are idle Ps without Ms, call `wakep()` to create/wake an M — new work shouldn't sit idle when there's free CPU capacity.

## Blocking syscalls and P handoff

This is the most subtle part. When a goroutine makes a **blocking syscall** — file I/O, CGo, anything that's not network I/O managed by the netpoller — the OS thread actually blocks. Here's what happens:

### Before the syscall: `entersyscall()`

1. The goroutine's G saves its context (SP, PC).
2. The M notes which P it was using. P's status → `_Psyscall`.
3. The M does **NOT** immediately release the P. Rationale: most syscalls are fast (microseconds). Releasing the P every time would be expensive for the common case.

### During the syscall

The M is blocked in the kernel. The P is in `_Psyscall` — technically not running anything. Its local run queue is stuck.

### Sysmon intervenes: `retake()`

Sysmon scans all Ps periodically. For a P in `_Psyscall`, if `syscalltick` hasn't changed since last check (meaning same syscall is still in progress), sysmon calls `handoffp(p)`:

1. Detaches P from the blocked M.
2. Finds an idle M (or creates a new one).
3. Hands the P to the new M.
4. The new M picks up P's local run queue and continues running goroutines.

The blocked goroutine stays on the old M in `_Gsyscall` state. The rest of the world keeps running.

### After the syscall: `exitsyscall()`

When the original M's syscall returns:

1. **Fast path:** Try to reacquire the original P. If it's still in `_Psyscall` (sysmon didn't steal it yet), take it back immediately. This is the common case for fast syscalls.
2. **Slow path:** Original P was stolen. Try to acquire any idle P from `sched.pidle`.
3. **Worst case:** No idle P available. Place the goroutine on the global run queue (`_Grunnable`). The M releases itself to idle list. The goroutine gets picked up later by whatever P has capacity.

There's also `entersyscallblock()` — used when the runtime **knows** the syscall will definitely block (like certain internal operations). This releases the P **immediately** without waiting for sysmon, which is more aggressive.

### Network I/O is different

Network I/O bypasses all of this. The FD is non-blocking, so `read()` never blocks the OS thread. Instead, the goroutine is parked on the netpoller (see [netpoller.md](netpoller.md)). The M stays free, the P stays running other goroutines. No handoff needed.

This is why network-heavy Go programs (HTTP servers, proxies) are so efficient: no syscall-blocking overhead, no M proliferation, no P handoffs — just goroutines parking and waking through the netpoller.

## Preemption

### Cooperative preemption (all Go versions)

Every non-leaf function has a stack-check prologue compiled by the Go compiler:

```asm
MOVQ (TLS), CX           // load g pointer
CMPQ SP, stackguard0(CX) // compare stack pointer against guard
JBE  morestack           // if SP <= guard, call morestack
```

This is nearly free on modern CPUs — two loads and a comparison, branch not taken in the common case. The preemption trick: sysmon, when it decides a goroutine has been running too long (>10ms), poisons `g.stackguard0` to a sentinel value (`stackPreempt`). On the next function call, the prologue fires, calls `morestack`, which notices the preempt flag, and instead of growing the stack, calls `gopreempt_m()` — parks the goroutine and puts it on the global run queue.

**The limitation:** tight loops with no function calls — `for { x++ }` — never hit a function prologue and never get preempted cooperatively. This was a real problem.

### Async preemption (Go 1.14+, SIGURG)

Go 1.14 added non-cooperative preemption (designed by Austin Clements):

1. Sysmon detects a goroutine running on a P for more than 10ms.
2. Sysmon calls `preemptM(m)` → `tgkill(tid, SIGURG)` — sends SIGURG to that specific OS thread.
3. The M's signal handler fires on the `gsignal` goroutine.
4. Signal handler examines the goroutine's instruction pointer. If it's at a **safe point** (not in a write barrier, not in runtime internals), it injects a call to `runtime.asyncPreempt`.
5. When execution resumes from the signal, the goroutine "calls" `asyncPreempt` → `gopreempt_m()` → parked.

**Why SIGURG?** Not used by libc on any major platform, doesn't interfere with debuggers (gdb/lldb), not commonly used by application code. This is also why we see `EINTR` in our epoll server — Go's SIGURG interrupts `epoll_wait`.

## Sysmon — the background watchdog

Sysmon is a dedicated OS thread created at startup. It runs **without a P** — so it can't be paused by GC stop-the-world. It sleeps between iterations, with adaptive delay: 20μs when active, up to 10ms when idle.

Each iteration:

| Task | What it does |
|---|---|
| `netpoll(0)` | Non-blocking epoll_wait, injects ready goroutines into global queue |
| `retake()` | Scans all Ps. Preempts long-running goroutines (>10ms). Hands off Ps from syscall-blocked Ms. |
| Timer check | Wakes goroutines whose `time.Sleep` / deadlines expired |
| Force GC | Triggers GC if heap grew enough or enough time passed |
| Deadlock detection | If all goroutines are blocked with no way to unblock → `panic("all goroutines are asleep")` |

Without sysmon: tight loops never preempt, network I/O readiness delayed up to 10ms, blocking syscalls permanently consume Ps.

## GOMAXPROCS — what it actually controls

GOMAXPROCS controls **exactly one thing**: the number of P structs. This caps how many goroutines execute simultaneously. It does NOT cap the number of OS threads.

```
GOMAXPROCS=4 means:
  4 Ps exist → at most 4 goroutines run in parallel
  But there could be 500 Ms (if 496 goroutines are in blocking syscalls)
```

When you call `runtime.GOMAXPROCS(n)`:
1. Stop-the-world.
2. If `n > current`: allocate new Ps, add to idle list.
3. If `n < current`: excess Ps set to `_Pdead`, their run queues drained to global queue.
4. Resume.

**Container gotcha:** `runtime.NumCPU()` reads `/proc/cpuinfo`, which shows the **host's** CPU count, not the container's CPU limit. A container limited to 0.5 CPU on a 64-core host sets GOMAXPROCS=64, creating 64 Ps competing for 0.5 CPU of actual time. Fix: use `uber-go/automaxprocs` or Go 1.25+'s built-in CPU quota awareness.

## Spinning threads

A spinning M is associated with a P but has no work — it actively burns CPU looking for goroutines instead of sleeping.

Why? When a goroutine becomes runnable (channel send, timer, I/O), waking a sleeping OS thread costs microseconds (kernel context switch). A spinning M picks it up in nanoseconds.

The invariant: **at most 1 spinning M per idle P**. Total spinning Ms ≤ GOMAXPROCS. When a spinning M finds work, it transitions to non-spinning. When a new goroutine is created and there are no spinning Ms but idle Ps exist, `wakep()` wakes/creates an M to spin.

This is why Go has low scheduling latency: the first goroutine after a quiet period gets picked up almost instantly.

## Goroutine stack mechanics

### The stack check prologue

Every function begins with:
```asm
CMPQ SP, stackguard0(g)
JBE  morestack
```
Nearly free: two loads, one compare, branch not taken. `stackguard0` is set to `stack.lo + 928 bytes`, ensuring enough room for the next frame.

### Copying stacks (Go 1.3+)

When `morestack` is called:
1. Allocate a new stack at **2x the current size**.
2. Copy all content from old stack to new stack.
3. **Update all pointers** into the old stack to point into the new. The GC's stack map metadata (generated by the compiler for each function at each call site) identifies all pointer slots.
4. Release old stack memory.
5. Resume execution on the new stack.

### Why not segmented stacks? (Go 1.0–1.2)

Old Go used segmented stacks: allocate a new segment, link to old, free on return. This caused the **"hot split" problem**: a function on the boundary between segments called in a tight loop would allocate/free a segment every iteration. Copying stacks eliminated this entirely.

### Stack shrinking

During GC, if a goroutine uses less than 1/4 of its current stack, the runtime shrinks it by half (copies to smaller allocation). This prevents goroutines that briefly needed a deep stack from permanently hogging memory.

### Escape analysis connection

Because stacks can be copied (and all pointers into them updated), you can't hold a raw pointer to a stack variable across a scheduling point in unsafe code. The compiler's escape analysis is conservative: if it can't prove a pointer to a local won't outlive the frame, it heap-allocates that variable. This is why `&x` where `x` is local sometimes causes a heap allocation — the compiler makes `x` escape to be safe.

## The scheduler decision tree

When `schedule()` runs on an M, `findRunnable()` does the following in order:

```
1. Every 61 scheduling events → check global queue (starvation prevention)
2. Check p.runnext (highest priority — just-created or just-woken goroutines)
3. Check p.runq (local 256-entry ring buffer)
4. Check global run queue (batch take)
5. Call netpoll(0) (non-blocking — collect I/O-ready goroutines)
6. Work-steal from random victim P (take half)
7. Re-check global queue
8. Re-check netpoll (block briefly if timers are pending)
9. Nothing found → release P, park M
```

Order is deliberate: local work first (cache hot), global queue prevents starvation, stealing is last resort (avoid thrashing other P's caches), netpoll checked multiple times to minimize I/O latency.

## Resources

### Must-read design documents

- **Scalable Go Scheduler Design Doc — Dmitry Vyukov (2012)** — the original proposal that introduced P.
  https://docs.google.com/document/d/1TTj4T2JO42uD5ID9e89oa0sLKhJYD0Y_kqxDv3I3XMw/edit

- **Non-cooperative goroutine preemption — Austin Clements** — design doc for async preemption (SIGURG).
  https://go.googlesource.com/proposal/+/master/design/24543-non-cooperative-preemption.md

### Go runtime source files (read in this order)

1. `runtime/runtime2.go` — the `g`, `m`, `p` struct definitions. Read every field.
   https://github.com/golang/go/blob/master/src/runtime/runtime2.go

2. `runtime/proc.go` — the scheduler: `schedule()`, `findRunnable()`, `newproc()`, `runqput()`, `runqsteal()`, `handoffp()`, `entersyscall()`, `exitsyscall()`, `sysmon()`, `retake()`.
   https://github.com/golang/go/blob/master/src/runtime/proc.go

3. `runtime/netpoll.go` — netpoller core: `pollDesc`, `netpollblock()`, `netpollunblock()`.
   https://go.dev/src/runtime/netpoll.go

4. `runtime/netpoll_epoll.go` — Linux epoll backend. Read alongside netpoll.go.

5. `runtime/stack.go` — stack growth, copying, shrinking.

6. `runtime/preempt.go` — async preemption logic.
   https://go.dev/src/runtime/preempt.go

### Blog posts

- **Daniel Morsing — "The Go Scheduler" (2013)** — short, clear, accurate. Best first read.
  https://morsmachine.dk/go-scheduler

- **Daniel Morsing — "The Go Netpoller" (2013)** — companion post, explains netpoller-to-scheduler integration.
  https://morsmachine.dk/netpoller

- **Ardan Labs / Bill Kennedy — "Scheduling In Go" (3-part series)** — best modern deep-dive.
  Part I (OS Scheduler): https://www.ardanlabs.com/blog/2018/08/scheduling-in-go-part1.html
  Part II (Go Scheduler): https://www.ardanlabs.com/blog/2018/08/scheduling-in-go-part2.html

- **Jaana Dogan (JBD) — "Go's Work-Stealing Scheduler"** — well-illustrated.
  https://rakyll.org/scheduler/

- **Cloudflare — "How Stacks are Handled in Go"** — segmented-to-copying stack transition.
  https://blog.cloudflare.com/how-stacks-are-handled-in-go/

- **DataDog / Felix Geisendörfer — go-profiler-notes: Goroutine Scheduler** — exceptional detail, real diagrams.
  https://datadoghq.dev/go-profiler-notes/mental-model-for-go/goroutine-scheduler.html

- **Hidetatz — "Preemption in Go"** — traces through the SIGURG signal handling path.
  https://hidetatz.github.io/goroutine_preemption/

- **Dave Cheney — "Why is a Goroutine's Stack Infinite?"**
  https://dave.cheney.net/2013/06/02/why-is-a-goroutines-stack-infinite

### Conference talks

- **GopherCon 2018 — Kavya Joshi — "The Scheduler Saga"** — the definitive talk. Builds a scheduler from scratch and arrives at the real design by first principles.
  https://www.youtube.com/watch?v=YHRO5WQGh0k

### Academic papers

- **Columbia University — "Analysis of the Go Runtime Scheduler" (Deshpande, Sponsler, Weiss)**
  http://www.cs.columbia.edu/~aho/cs6998/reports/12-12-11_DeshpandeSponslerWeiss_GO.pdf

### Companion doc

- **[scheduler-fairness.md](scheduler-fairness.md)** — covers the scheduling theory: N:M models, convoy effect, why schedtick uses 61, FIFO vs LIFO tradeoffs, time slice inheritance (the 99.88% benchmark), runtime APIs (`Gosched`, `Goexit`, `LockOSThread`), and scheduler observability tools (`schedtrace`, `go tool trace`, ftrace).
