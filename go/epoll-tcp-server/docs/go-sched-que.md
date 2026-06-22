# Go Scheduler: Questions & Answers

> Detailed answers to questions that came up while reading [go-scheduler-deep-dive.md](go-scheduler-deep-dive.md). All answers verified against Go runtime source code (`runtime/proc.go`, `runtime/runtime2.go`, `runtime/chan.go`) and corroborated by official design docs and GitHub issues.

---

## 1. Threading Foundations (Parking, Spinning, Sleeping)

### What does "parking" mean?

**Parking a goroutine (G)** — `gopark()` in `proc.go`. The G's status changes from `_Grunning` to `_Gwaiting`, and it is **removed from all run queues entirely**. It sits inert, referenced only by whatever is holding it (a channel's wait queue, a mutex's wait list). The M immediately calls `schedule()` to run a different goroutine. The G stays parked until something calls `goready()` on it.

**Parking an M (OS thread)** — `stopm()` in `proc.go`. The M is added to `sched.midle` (idle-M list) and its OS thread blocks via `notesleep(&mp.park)` — a futex-like wait. **Zero CPU consumed.** Another M calls `notewakeup(&mp.park)` to bring it back when work appears.

**Parking a P** — there is no `parkP()` function. A P is simply marked `_Pidle` and pushed onto `sched.pidle` via `pidleput()`. Since a P is just a data structure (run queue + caches), it doesn't "sleep" — it sits on the list until some M claims it via `pidleget()` / `startm()`.

### What does "spinning" mean?

A **spinning M** holds a P (consuming a CPU core) but has no goroutine to run yet. Instead of immediately sleeping, it actively loops looking for work — checking other Ps' local queues, the global queue, and the netpoller. The field `m.spinning` marks this state; `sched.nmspinning` counts how many Ms are spinning globally.

Why not just sleep immediately? Creating/waking an OS thread costs 1–10μs. If new work appears 100ns later, a spinning M grabs it instantly — a sleeping M would need an expensive wake-up first. The runtime keeps a small number of Ms spinning briefly to amortize latency for bursty workloads. If a spinning M finds nothing after exhausting all work sources, it gives up its P and sleeps (`stopm()`).

### What does "sysmon does not spin" mean?

Sysmon never enters the `spinning` state and is never counted in `sched.nmspinning`. Unlike worker Ms that spin (hold a P, burn CPU cycles polling for work), sysmon has no P and is not trying to steal goroutines to execute. It does monitoring work and then actually sleeps via `usleep(delay)` — its OS thread is genuinely descheduled by the kernel, consuming zero CPU while asleep.

### Sleeping, idle, blocked — what's the difference?

| Term | Means | Example |
|------|-------|---------|
| **Sleeping** | OS-level: the kernel removes the thread from its runnable list. Zero CPU cycles consumed. | `notesleep()` (parked Ms), `usleep()` (sysmon) |
| **Idle** | Go-runtime concept: a resource (P or M) with no assigned work, sitting on a free list. An idle M's thread is actually sleeping. | M in `sched.midle`, P in `sched.pidle` |
| **Blocked** | Thread stuck inside an OS syscall, or G in `_Gwaiting`. The kernel won't schedule it until the syscall returns / event fires. | M stuck in a file read, G waiting on a channel |

When sysmon "goes to sleep," its OS thread literally calls `usleep(delay)`. During that sleep, the thread is fully descheduled — zero CPU. The Go runtime cannot wake it early (it's a plain timed sleep, not event-driven).

| State | CPU usage | Who does it | How it ends |
|-------|-----------|-------------|-------------|
| **Spinning** | 100% (polling loop) | M with a P but no G to run | Finds work → runs it; or gives up P → parks |
| **Parked/Sleeping** | 0% (kernel-descheduled) | M with no work, or G waiting on I/O/channel | Explicit wake-up (`notewakeup`, `goready`) |
| **Idle** | 0% | P in `sched.pidle`, or M in `sched.midle` | Claimed by `startm()` or `pidleget()` |
| **Blocked** | 0% (stuck in syscall) | M in a blocking syscall, or G in `_Gwaiting` | Syscall returns; or event fires (`goready`) |

---

## 2. What "Calling Into the Runtime/Scheduler" Means

This does NOT mean every line of your code talks to the scheduler. It means specific code paths where control transfers from your application into runtime-owned functions:

| Trigger | Runtime function | What happens |
|---------|-----------------|--------------|
| **Function calls** | `morestack` → `newstack()` | Compiler inserts a stack-growth-check prologue in every function. The runtime piggybacks preemption checks here. |
| **Channel operations** | `chansend()` / `chanrecv()` | Calls `gopark()` when the goroutine must wait. |
| **Memory allocation** | `mallocgc()` | Large allocations can trigger GC assist or stack checks. |
| **System calls** | `entersyscall()` / `exitsyscall()` | Hands the P off when the M blocks in the OS. |
| **`go func()`** | `newproc()` | Creates a new G and places it in the run queue. |
| **Explicit yields** | `Gosched()`, `select{}`, `time.Sleep()` | Voluntarily enters `schedule()`. |

A tight loop like `for { x = x*x + 1 }` with no function calls, no allocations, no channel ops — **never calls into the runtime** (pre Go 1.14). That is exactly why asynchronous preemption via SIGURG was needed.

`schedtick` does *not* increment on every runtime call. It increments inside `execute()` — it counts scheduling loop iterations on a P, not the frequency of runtime entry.

---

## 3. Sysmon Thread — Is It GOMAXPROCS + 1?

**Not exactly.** Go creates threads lazily, not eagerly. At startup there are exactly **two OS threads**:

1. **M0** — the main OS thread running `runtime.main()` → user's `main()`
2. **Sysmon's M** — started via `newm(sysmon, nil, -1)` where the `nil` P argument makes it permanently P-less

There are GOMAXPROCS P structs allocated by `procresize()`, but only P0 is attached to M0 — P1, P2, etc. sit idle in `sched.pidle` with no M attached.

Additional Ms are created **on demand** via `startm()`/`newm()` when goroutines need to run in parallel and there aren't enough idle Ms. You might eventually have far more than GOMAXPROCS+1 threads if many goroutines block in syscalls (each blocked syscall ties up an M).

---

## 4. Schedtick and the 61st Tick

### When does schedtick increment?

Inside `execute()`, **only when `inheritTime == false`**:

```go
func execute(gp *g, inheritTime bool) {
    casgstatus(gp, _Grunnable, _Grunning)
    if !inheritTime {
        _g_.m.p.ptr().schedtick++
    }
    // ...
    gogo(&gp.sched)
}
```

- `inheritTime == false` → fresh time slice → **schedtick++**
- `inheritTime == true` → goroutine from `runnext`, inheriting remaining time slice → **no increment**
- Preemption itself does NOT increment schedtick — the increment only happens when the *next* G starts running with a fresh slice

*Source: [Commit e870f06c](https://github.com/golang/go/commit/e870f06c3f49ed63960a2575e330c2c75fc54a34) introduced the `inheritTime` parameter.*

### On the 61st tick — takes ONE, not fair share

```go
// In schedule():
if _g_.m.p.ptr().schedtick%61 == 0 && sched.runqsize > 0 {
    lock(&sched.lock)
    gp = globrunqget(_g_.m.p.ptr(), 1)   // max = 1
    unlock(&sched.lock)
}
```

It passes `max=1`, so it takes **exactly ONE goroutine**. That goroutine is **executed immediately** — not queued into `runnext` or the local `runq`.

The "fair share" formula (`n = len(GRQ)/GOMAXPROCS + 1`, capped at `len(GRQ)/2`) only applies when `globrunqget` is called from `findRunnable()` with `max <= 0` — i.e., when a P is completely out of work. The 61-tick path is a lightweight fairness check, not a bulk drain.

*Source: [Go issue #34981 "Why 61?"](https://github.com/golang/go/issues/34981) confirms the rationale.*

---

## 5. Local Run Queue: FIFO or LIFO?

**The circular `runq` buffer (256 slots) is strictly FIFO** — enqueue at tail (`runqput` → `runqtail++`), dequeue from head (`runqget` → `runqhead++`).

**`runnext` is a completely separate single-slot fast-path**, not part of the circular buffer. It's checked *before* the circular buffer by `runqget()`. When a goroutine readies another (via `go func()` or channel send), the readied goroutine goes into `runnext` (via `runqput(_p_, gp, next=true)`), bumping the previous `runnext` occupant into `runq`'s tail.

So the *overall* behavior looks like LIFO for the most recently readied goroutine (it "cuts the line" via `runnext`), but the underlying queue itself is FIFO.

---

## 6. Work Stealing — Head or Tail?

### From another P's local queue

The thief steals from the **head** of the victim's queue (the oldest, coldest goroutines):

```go
// runqgrab — steals half, starting from victim's head
for i := uint32(0); i < n; i++ {
    g := pp.runq[(h+i) % uint32(len(pp.runq))]  // h = head
    batch[(batchHead+i) % uint32(len(batch))] = g
}
atomic.CasRel(&pp.runqhead, h, h+n)  // advance victim's head
```

**Why steal from head?** Cache locality. Goroutines near the **tail** were just enqueued — likely spawned by the goroutine the victim P is currently running, so they're hot in that core's L1/L2 cache. Goroutines near the **head** are older and colder. The thief takes the cold ones, leaving the hot ones for the owner.

### From the global queue

Also from the **head** — `sched.runq.pop()` removes from the front. `globrunqput()` pushes to the back. Standard FIFO.

---

## 7. What Happens When a Goroutine Blocks on a Channel

### Blocking (`<-ch` on empty channel)

1. `chanrecv()` (in `runtime/chan.go`) builds a `sudog` (waiter struct)
2. Enqueues it onto `c.recvq` — **the channel's own wait queue**, not any P run queue
3. Calls `gopark(chanparkcommit, ..., waitReasonChanReceive, ...)`
4. `gopark()` → G status changes to `_Gwaiting` → G is **removed from all run queues**
5. The M calls `schedule()` and runs a different goroutine

The blocked G is NOT in any run queue. It exists only as a `sudog` reference inside the channel's `recvq`.

### Unblocking (sender sends data)

1. `chansend()` / `send()` finds the waiting receiver in `c.recvq`
2. Copies the value directly to the receiver's memory (zero-copy)
3. Calls `goready(gp)` → `ready(gp, traceskip, next=true)`
4. `ready()` → G status → `_Grunnable` → **`runqput(_p_, gp, true)`**

Because `next=true`, the unblocked goroutine goes into the **`runnext` slot** of the sender's P. It gets priority to run next and inherits the sender's remaining time slice.

---

## 8. Preemption and Completion — Who Schedules the Next Goroutine?

### When a goroutine's timeslice expires (preempted)

1. `sysmon` → `retake()` detects G running >10ms (saved schedtick hasn't advanced)
2. `preemptone()` sends SIGURG to the M
3. Signal handler → `doSigPreempt()` → `asyncPreempt` → `gopreempt_m()` → `goschedImpl()`
4. `goschedImpl()`: G → `_Grunnable`, `globrunqput(gp)` → **global queue**
5. Calls `schedule()` on the **same M** (still holding its P)

### When a goroutine completes normally

1. Function returns → `goexit()` → `goexit0()` → G recycled (`_Gdead`)
2. Calls `schedule()` on the same M

### Who puts the next goroutine on the thread?

**The same M does it itself.** After losing its G (preemption, completion, or blocking), the M still holds its P and runs `schedule()` → `findRunnable()` → `execute()`. No external entity assigns work — the M is a self-scheduling loop:

1. `schedtick%61 == 0`? → check global queue (take 1)
2. Check local queue (`runnext` first, then FIFO head)
3. `findRunnable()`:
   - Check global queue (fair share)
   - Poll netpoller
   - Steal half from random other P (retry 4x)
   - Give up → park the M (`stopm()`)

### Does schedtick++ happen on preemption?

**No.** Preemption puts the old G in the global queue and enters `schedule()`. The increment happens later, in `execute()`, when the *next* G starts running with a fresh time slice.

---

## 9. Startup Trace — How Ps Get Filled

```go
func main() {
    runtime.GOMAXPROCS(4)
    var wg sync.WaitGroup
    for i := 0; i < 8; i++ {
        wg.Add(1)
        go func(n int) {
            defer wg.Done()
            fmt.Println("goroutine", n)
        }(i)
    }
    wg.Wait()
}
```

**Step 1 — Startup.** `schedinit()` → `procresize(4)` allocates 4 Ps. P0 attaches to M0. P1–P3 idle. Sysmon's M (P-less) is running.

```
M0/P0: running [main goroutine]
sysmon: running (no P, sleeping between checks)
P1, P2, P3: _Pidle in sched.pidle, no M attached
```

**Step 2 — Each `go func(n)` → `newproc()` → `runqput(P0, newG, true)`.** All 8 land on P0 (because `main` runs on P0). Each new G goes into P0's `runnext`, bumping the previous occupant into `runq`'s tail:

```
After all 8 go statements:
P0: runnext=[G7], runq=[G0, G1, G2, G3, G4, G5, G6]
P1: empty    P2: empty    P3: empty
```

**Step 3 — `wakep()` wakes idle Ps.** When the first G is added with idle Ps available, `newproc` → `wakep()` → `startm()` pulls an idle P (P1) from `sched.pidle`, creates/wakes an M, attaches P1. The new M starts at `schedule()` → `findRunnable()`.

**Step 4 — Work stealing.** P1's M has an empty local queue. `findRunnable()` → `runqsteal()`: picks P0, steals half from P0's head. Similarly P2, P3 wake and steal:

```
After redistribution (approximate):
P0: runnext=[G7], runq=[G5, G6]  → ~3 goroutines
P1: runq=[G0, G1]                → ~2 goroutines
P2: runq=[G2, G3]                → ~2 goroutines
P3: runq=[G4]                    → ~1 goroutine
```

**Step 5 — Execution.** Each P's M calls `execute()` on the next G. When a G finishes, `goexit()` → `schedule()` picks the next. Same M+P pair persists — no new threads needed.

**Step 6 — `wg.Wait()`.** Main goroutine → `gopark()`. Last `wg.Done()` → `goready()` puts main into a P's `runnext`. Main resumes, program exits.

---

## Quick Reference

| Question | Answer |
|----------|--------|
| 61st tick: how many from GRQ? | Exactly **1** (`globrunqget(p, 1)`) |
| 61st tick: where does it go? | **Executed immediately** (not into runq or runnext) |
| Local runq ordering? | **FIFO** (enqueue tail, dequeue head) |
| `runnext` — LIFO? | Separate single-slot, checked **before** FIFO queue |
| Work steal direction? | From victim's **head** (oldest/coldest goroutines) |
| Channel-blocked G goes where? | **Channel's wait queue** (`c.recvq`), not any run queue |
| Channel-unblocked G goes where? | **Sender's P `runnext`** slot |
| Preempted G goes where? | **Global run queue** |
| schedtick++ on preemption? | **No** — only in `execute()` when next G starts fresh slice |
| Sysmon = extra thread? | **Yes**, runs without a P, started before `main()` body |
| Threads at startup? | **2** (M0 + sysmon). More created on demand. |

---

## Sources

- [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go) — `schedule()`, `findRunnable()`, `execute()`, `gopark()`, `goready()`, `stopm()`, `startm()`, `sysmon()`, `retake()`, `preemptone()`, `runqput()`, `runqget()`, `runqsteal()`, `runqgrab()`, `globrunqget()`, `globrunqput()`
- [`runtime/runtime2.go`](https://github.com/golang/go/blob/master/src/runtime/runtime2.go) — structs `g`, `m` (`spinning`, `park note`), `p` (`schedtick`, `runq`, `runnext`), `schedt` (`nmspinning`, `pidle`, `midle`)
- [`runtime/chan.go`](https://github.com/golang/go/blob/master/src/runtime/chan.go) — `chansend()`, `chanrecv()`, `send()`, `recv()`
- [`runtime/signal_unix.go`](https://github.com/golang/go/blob/master/src/runtime/signal_unix.go) — `sighandler()`, `doSigPreempt()`
- [Commit e870f06c](https://github.com/golang/go/commit/e870f06c3f49ed63960a2575e330c2c75fc54a34) — `runnext` slot + time slice inheritance + `inheritTime` parameter
- [Go issue #34981 "Why 61?"](https://github.com/golang/go/issues/34981) — rationale for prime-number schedtick interval
- [Go issue #20168](https://github.com/golang/go/issues/20168) — global queue polling optimization discussion

---
---

# Part 2: Deeper Questions

> Second round of questions — digging into the OS-level and runtime-level concepts that the first round's answers assumed you already knew. Everything here is verified against Linux kernel source, man pages, and Go runtime source code.

---

## 10. OS Thread States (Linux)

At the OS level, Linux doesn't distinguish between "processes" and "threads" — they're all **tasks** (`task_struct` in the kernel). Each task is in exactly one state at any time.

| Kernel Constant | `ps` Code | What it means |
|---|---|---|
| `TASK_RUNNING` | `R` | The task is either **currently executing on a CPU core** or **sitting in the run queue waiting for a CPU core**. The kernel does not distinguish "running" from "runnable" — both are `R`. |
| `TASK_INTERRUPTIBLE` | `S` | Sleeping. Waiting for some event (timer, data on a socket, a futex wake). **Can** be woken by a signal. This is the most common "blocked" state — almost every sleeping thread you see in `ps` is in `S`. |
| `TASK_UNINTERRUPTIBLE` | `D` | Sleeping, but **cannot** be woken by signals — only by the specific event it's waiting for (usually disk I/O). This is the infamous "D state" that you can't even `kill -9`. It exists because interrupting mid-I/O could corrupt data. |
| `__TASK_STOPPED` | `T` | Stopped by a job-control signal (`SIGSTOP`, `SIGTSTP`, Ctrl+Z). Can be resumed with `SIGCONT`. |
| `__TASK_TRACED` | `t` | Stopped by a debugger via `ptrace`. |
| `EXIT_ZOMBIE` | `Z` | The task has terminated, but its parent hasn't called `wait()` to collect its exit status yet. The task's memory is freed, but its entry in the process table persists so the parent can read the exit code. |
| `EXIT_DEAD` | `X` | Final cleanup state. The parent has reaped the task (or the task is being removed). You should never see this in `ps`. |

In practice, most threads you'll encounter are either `R` (running/runnable) or `S` (sleeping, waiting for something).

**How this maps to Go:**
- A spinning M → its OS thread is in `R` (running, executing the spin loop)
- A parked M (in `notesleep`) → its OS thread is in `S` (interruptible sleep on a futex)
- An M blocked in a syscall (e.g., `read()` on disk) → its OS thread is in `D` (uninterruptible sleep)
- sysmon sleeping via `usleep()` → its OS thread is in `S`

*Source: Linux kernel [`include/linux/sched.h`](https://github.com/torvalds/linux/blob/master/include/linux/sched.h), [`ps(1)` man page](https://man7.org/linux/man-pages/man1/ps.1.html).*

---

## 11. What Is a Futex?

**Futex = Fast Userspace Mutex.** It's a Linux kernel feature (since 2.5.7) that provides the building block for almost all userspace synchronization — mutexes, condition variables, semaphores, Go's `note` type.

### The problem it solves

Without futex, locking a mutex always requires a syscall (kernel mode switch), even when nobody else is contending for the lock. Syscalls cost ~1μs. If your mutex is uncontended 99% of the time, you're paying 1μs for nothing.

### How it works

A futex is just a **32-bit integer in userspace memory**. The kernel knows nothing about it until you explicitly ask.

**Uncontended case (fast path — no syscall):**
```
Thread A wants the lock:
  atomic CAS(&futex_word, 0, 1)  →  succeeds (was 0, now 1)
  // Thread A has the lock. No syscall. ~10ns.

Thread A releases the lock:
  atomic store(&futex_word, 0)
  // No waiters? Done. No syscall.
```

**Contended case (slow path — syscall):**
```
Thread B wants the lock:
  atomic CAS(&futex_word, 0, 1)  →  fails (it's 1, someone holds it)
  // Need to wait. Now we involve the kernel:
  syscall: futex(&futex_word, FUTEX_WAIT, 1, ...)
  // Kernel checks: is futex_word still 1?
  //   Yes → put Thread B to sleep on a kernel wait queue keyed by &futex_word
  //   No  → return immediately (someone released it between our CAS and syscall)

Thread A releases the lock:
  atomic store(&futex_word, 0)
  syscall: futex(&futex_word, FUTEX_WAKE, 1)
  // Kernel wakes one thread sleeping on &futex_word
```

The critical guarantee: `FUTEX_WAIT` **atomically** checks that the value hasn't changed AND puts the thread to sleep. Without this atomicity, there'd be a race between "check value" and "go to sleep" where a wake-up could be lost.

### The two key operations

| Operation | What it does |
|-----------|-------------|
| `futex(addr, FUTEX_WAIT, expected_val, timeout)` | If `*addr == expected_val`, put calling thread to sleep. Otherwise return `EAGAIN` immediately. |
| `futex(addr, FUTEX_WAKE, count)` | Wake up to `count` threads sleeping on `addr`. |

Go's `notesleep` and `notewakeup` are thin wrappers around these two operations.

*Source: [`futex(2)` man page](https://man7.org/linux/man-pages/man2/futex.2.html).*

---

## 12. What Is a Semaphore?

A semaphore is a synchronization primitive with an **integer counter** and two operations:

| Operation | Classic name | What it does |
|-----------|-------------|-------------|
| **Wait / Acquire** | P (from Dutch *proberen*, "to try") | Decrement the counter. If it would go below 0, **block** until someone increments it. |
| **Signal / Release** | V (from Dutch *verhogen*, "to increase") | Increment the counter. If threads are waiting, **wake one**. |

A **binary semaphore** (counter is 0 or 1) acts like a mutex. A **counting semaphore** (counter can be > 1) controls access to a pool of resources.

### Semaphores in Go's runtime

Go's runtime has its own semaphore implementation in `runtime/sema.go`. It's not a general-purpose counting semaphore — it's a **sleep/wakeup pairing mechanism** keyed by a memory address:

- `runtime_Semacquire(addr)` — block the calling goroutine until a wakeup is available at `addr`
- `runtime_Semrelease(addr)` — wake one goroutine waiting at `addr`

**How `sync.WaitGroup.Wait()` uses it:** WaitGroup maintains a counter. When you call `Wait()` and the counter is non-zero, the goroutine blocks via `runtime_Semacquire` on the WaitGroup's internal semaphore address. Each `Done()` decrements the counter; when it hits zero, `runtime_Semrelease` wakes all waiters.

**Data structures behind it:**

- **`sudog`** — a "pseudo-G" wait-queue node. It represents one goroutine's presence in a wait structure. Used everywhere: channels, semaphores, select. Contains a pointer back to the `g` and linking fields (`waitlink`, `waittail`).
- **`semaRoot`** — a treap (tree + heap) that maps semaphore addresses to their wait queues. Multiple waiters at the same address are chained as a linked list off each treap node.

*Source: [`runtime/sema.go`](https://github.com/golang/go/blob/master/src/runtime/sema.go).*

---

## 13. `notesleep`, `notewakeup`, and `usleep`

These are **Go runtime internal functions**, not direct syscalls — but they use OS primitives underneath.

### The `note` type

Defined in `runtime2.go`:

```go
type note struct {
    key uintptr
}
```

A `note` is a **one-shot sleep/wakeup primitive**. It has two states: "not notified" (key=0) and "notified" (key=1). The contract: call `noteclear` first, then exactly one `notesleep` and exactly one `notewakeup` per cycle.

### `notesleep(n *note)` — "sleep until someone wakes me"

Blocks the current OS thread until `notewakeup(n)` is called. On Linux, it loops calling `futexsleep()`:

```go
// Simplified from runtime/lock_futex.go:
func notesleep(n *note) {
    for atomic.Load(&n.key) == 0 {
        futexsleep(key32(&n.key), 0, -1)  // FUTEX_WAIT, sleep forever
    }
}
```

`futexsleep` calls the `futex` syscall with `FUTEX_WAIT`. The thread is fully descheduled by the kernel — zero CPU.

**Used for:** parking idle Ms. When an M has no work, `stopm()` calls `notesleep(&m.park)`. The M's OS thread sleeps until another M wakes it.

### `notewakeup(n *note)` — "wake the sleeping thread"

```go
// Simplified from runtime/lock_futex.go:
func notewakeup(n *note) {
    old := atomic.Xchg(key32(&n.key), 1)  // set key to 1
    futexwakeup(key32(&n.key), 1)          // FUTEX_WAKE, wake 1 thread
}
```

**Used for:** waking parked Ms. When `startm()` needs an M, it calls `notewakeup(&mp.park)` to bring a sleeping M back to life.

### `usleep(usec uint32)` — "sleep for exactly N microseconds"

This is a **raw assembly stub** in `sys_linux_amd64.s` that issues the `nanosleep` syscall directly:

```asm
TEXT runtime·usleep(SB),NOSPLIT,$16
    // Convert microseconds to timespec {seconds, nanoseconds}
    // Issue SYS_nanosleep syscall
    SYSCALL
    RET
```

It bypasses the Go scheduler entirely (marked `NOSPLIT`, no stack checks, no goroutine bookkeeping). The OS thread unconditionally sleeps for the full duration — there is **no wakeup mechanism**. Nobody can wake it early.

**Used for:** sysmon's adaptive sleep loop. Between monitoring iterations, sysmon calls `usleep(delay)` to sleep for 20μs–10ms.

### Comparison

| Function | Mechanism | Can be woken early? | Used by |
|----------|-----------|-------------------|---------|
| `notesleep` | futex (FUTEX_WAIT) | **Yes**, by `notewakeup` | Parking idle Ms (`stopm`) |
| `notewakeup` | futex (FUTEX_WAKE) | N/A (wakes others) | Waking parked Ms (`startm`) |
| `usleep` | nanosleep syscall | **No**, always sleeps full duration | sysmon's sleep loop |

*Source: [`runtime/lock_futex.go`](https://github.com/golang/go/blob/master/src/runtime/lock_futex.go), [`runtime/os_linux.go`](https://github.com/golang/go/blob/master/src/runtime/os_linux.go), [`runtime/sys_linux_amd64.s`](https://github.com/golang/go/blob/master/src/runtime/sys_linux_amd64.s).*

---

## 14. `sched.midle` and `sched.pidle`

`sched` is a global variable of type `schedt` (defined in `runtime2.go`). It holds all global scheduler state, protected by `sched.lock` (a mutex).

```go
type schedt struct {
    lock mutex

    midle        listHeadManual // idle M's waiting for work
    nmidle       int32          // count of idle M's
    nmidlelocked int32          // count of locked idle M's

    pidle        puintptr       // idle P's
    npidle       atomic.Int32   // count of idle P's

    nmspinning   atomic.Int32   // count of spinning M's
    maxmcount    int32          // max M's allowed (default 10000)
    // ...
}
```

### `sched.midle` — idle M list

A **singly-linked list** of parked Ms. Each M has a `m.schedlink` field that points to the next idle M. Push/pop from the head (stack-like, LIFO). When `stopm()` parks an M, it pushes it onto this list. When `startm()` needs an M, it pops from here.

### `sched.pidle` — idle P list

A **singly-linked list** of idle Ps. Each P has a `p.link` field that points to the next idle P. Also LIFO. `pidleput()` pushes, `pidleget()` pops.

Both lists require holding `sched.lock` to access.

```
sched.midle → M3 → M7 → M2 → nil    (3 idle Ms, sleeping via notesleep)
sched.pidle → P2 → P3 → nil          (2 idle Ps, just data structures sitting there)
```

*Source: [`runtime/runtime2.go`](https://github.com/golang/go/blob/master/src/runtime/runtime2.go) (`schedt` struct), [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go) (`pidleget`, `pidleput`).*

---

## 15. All Goroutine (G) States

Every goroutine has a `status` field (atomically accessed via `readgstatus`/`casgstatus`). Here are all states from `runtime2.go`:

| State | Value | Meaning |
|-------|-------|---------|
| `_Gidle` | 0 | Just allocated, not yet initialized. |
| `_Grunnable` | 1 | Ready to run, sitting on a run queue. Not executing code. |
| `_Grunning` | 2 | Currently executing on an M. Owns its stack. Not on any run queue. |
| `_Gsyscall` | 3 | Executing a blocking syscall. Has an M but may not have a P (P may have been handed off). |
| `_Gwaiting` | 4 | Blocked — waiting on a channel, mutex, timer, I/O, etc. **Not on any run queue.** Recorded in some wait structure (channel's `recvq`, semaphore tree, etc.). |
| `_Gdead` | 6 | Unused. Either just exited, or sitting on a free list for reuse, or being initialized. |
| `_Gcopystack` | 8 | Stack is being moved/grown. Not executing code. |
| `_Gpreempted` | 9 | Stopped by signal-based async preemption. Like `_Gwaiting` but specifically for the `suspendG` mechanism. |
| `_Gscan + X` | 0x1000 + X | GC is scanning this goroutine's stack. Combined with any of the above (e.g., `_Gscanrunnable = 0x1001`). The base state cannot change while scanning. |

### The lifecycle

```
_Gidle → _Grunnable → _Grunning → _Grunnable (preempted)
                          ↓              ↓
                       _Gsyscall      _Gwaiting (channel/mutex/etc.)
                          ↓              ↓
                       _Grunnable     _Grunnable (goready wakes it)
                          
_Grunning → _Gdead (goroutine finishes, recycled to free list)
```

*Source: [`runtime/runtime2.go`](https://github.com/golang/go/blob/master/src/runtime/runtime2.go).*

---

## 16. All M (OS Thread) States

**Ms do NOT have an explicit state enum.** Their state is implied by a combination of fields on the `m` struct:

| Implicit state | How to identify it | What's happening |
|---|---|---|
| **Running a goroutine** | `m.curg != nil` | Executing user code. Has a P. |
| **Spinning** | `m.spinning == true` | Holds a P, burning CPU looking for work in `findRunnable()`. |
| **Parked / Idle** | On `sched.midle`, blocked in `notesleep(&m.park)` | No P, no work. OS thread sleeping. Zero CPU. |
| **In syscall** | `m.curg` is in `_Gsyscall`, P status is `_Psyscall` | OS thread blocked in kernel. P may be detached. |
| **Locked (idle)** | `m.lockedg != 0`, in `stoplockedm()` → `mPark()` | Dedicated to a specific G. P released. Sleeping until locked G is runnable. |
| **Running scheduler code** | On `g0` stack, in `schedule()`/`findRunnable()` | Between goroutines. Looking for next G to run. |

### P states (for completeness)

| State | Value | Meaning |
|-------|-------|---------|
| `_Pidle` | 0 | Not in use. On `sched.pidle` list. |
| `_Prunning` | 1 | Attached to an M, running goroutines. |
| `_Psyscall` | 2 | Attached to an M that's in a syscall. May be detached by sysmon. |
| `_Pgcstop` | 3 | Halted for a stop-the-world GC pause. |
| `_Pdead` | 4 | No longer used (GOMAXPROCS was reduced). |

*Source: [`runtime/runtime2.go`](https://github.com/golang/go/blob/master/src/runtime/runtime2.go).*

---

## 17. `schedule()` — The Central Loop

`schedule()` is the **heart of the Go scheduler**. It runs on an M's `g0` stack (a special system goroutine, not a user goroutine) and its job is: **find a runnable G and run it**.

It never returns to its caller — it always ends by jumping into a goroutine via `execute()` → `gogo()`.

### What it does (simplified)

```
schedule():
  1. If this M is locked to a G (m.lockedg != 0):
       → call stoplockedm()  (release P, park, wait for locked G)
       → when woken: execute(lockedg)

  2. Every 61st schedtick: check global run queue
       → globrunqget(_p_, 1)  // take 1 goroutine
       → if found: execute(gp)

  3. Check local run queue
       → runqget(_p_)  // runnext first, then FIFO head
       → if found: execute(gp)

  4. Call findRunnable()  // this blocks until work is found
       → checks: global queue (fair share), netpoller, work stealing
       → if all empty: parks the M via stopm()
       → eventually returns a runnable G

  5. If the found G is locked to a different M:
       → call startlockedm(gp)  (hand P to locked M, wake it)
       → goto step 1  (this M needs to find different work)

  6. execute(gp, inheritTime)
```

### When is it called?

`schedule()` is called whenever an M needs a new goroutine to run:

| Caller | When |
|--------|------|
| `park_m()` | After `gopark()` — a goroutine blocked on channel/mutex/etc. |
| `goexit0()` | After a goroutine finishes execution. |
| `goschedImpl()` | After preemption (SIGURG) or voluntary yield (`Gosched()`). |
| `mstart1()` | When a new M starts up for the first time. |
| `exitsyscall0()` | When returning from a syscall and no P could be acquired. |
| `stoplockedm()` | (indirectly) After a locked M's G becomes runnable again. |

*Source: [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go).*

---

## 18. `execute()` — Dispatching a Goroutine

`execute()` is called by `schedule()` after it has found a runnable G. It does the actual "put this goroutine on the CPU" work.

```go
func execute(gp *g, inheritTime bool) {
    // 1. Attach G to this M
    mp := getg().m
    mp.curg = gp
    gp.m = mp

    // 2. Change G status: _Grunnable → _Grunning
    casgstatus(gp, _Grunnable, _Grunning)

    // 3. Reset preemption state
    gp.waitsince = 0
    gp.preempt = false
    gp.stackguard0 = gp.stack.lo + _StackGuard

    // 4. Increment schedtick (only for fresh time slices)
    if !inheritTime {
        mp.p.ptr().schedtick++
    }

    // 5. Jump into the goroutine's saved PC/SP
    gogo(&gp.sched)  // never returns
}
```

`gogo()` is an assembly function that restores the goroutine's saved registers (program counter, stack pointer, etc.) and jumps into its code. From this point, the goroutine is running.

`execute()` never returns — the goroutine eventually yields back to the scheduler through `gopark`, `goexit`, `Gosched`, preemption, or a syscall, which all eventually loop back to `schedule()`.

*Source: [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go).*

---

## 19. `goready()` — Waking a Goroutine

`goready()` makes a parked (`_Gwaiting`) goroutine runnable again. Called when the event a goroutine was waiting for has occurred.

```go
func goready(gp *g, traceskip int) {
    systemstack(func() {
        ready(gp, traceskip, true)  // true = put in runnext
    })
}
```

`ready()` does:
1. `casgstatus(gp, _Gwaiting, _Grunnable)` — mark as runnable
2. `runqput(_p_, gp, next=true)` — put into current P's `runnext` slot
3. If there are idle Ps, call `wakep()` to bring one online

**Who calls `goready()`:**
- Channel send/receive (`chansend`/`chanrecv`) when unblocking a waiting partner
- `sync.Mutex.Unlock()` when waking a waiter
- `sync.WaitGroup.Done()` (via `runtime_Semrelease`) when counter hits zero
- Timer expiration
- Netpoller when I/O completes

*Source: [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go).*

---

## 20. `stopm()` and `startm()`

### `stopm()` — "I have no work, put me to sleep"

Called when an M has exhausted all work sources (local queue, global queue, netpoller, work stealing all failed).

```
stopm():
  1. Release P (if any) → pidleput(pp)
  2. Add self to sched.midle (idle M list)
  3. notesleep(&m.park)  →  OS thread sleeps (futex). Zero CPU.
  // ...wakes up here when notewakeup(&m.park) is called...
  4. Acquire the P that was attached by startm()
  5. Return to caller (which re-enters schedule())
```

### `startm(pp *p, spinning bool)` — "I have a P with work, find an M to run it"

Called when work appears and a P needs an M to execute it. Typically called by `wakep()`.

```
startm(pp, spinning):
  1. Get an idle P: pp (passed in) or pidleget()
  2. Get an idle M: pop from sched.midle
     → If no idle M exists: newm() — create a brand new OS thread
  3. Attach P to M: mp.nextp = pp
  4. notewakeup(&mp.park)  →  wake the M's OS thread
  // The woken M picks up in stopm() step 4, acquires the P, enters schedule()
```

**The key insight:** `stopm` and `startm` are symmetric — one parks an M, the other wakes one. They communicate through `sched.midle` (the idle M list) and `m.park` (the futex-backed note).

*Source: [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go).*

---

## 21. Does schedtick Increment When a Preempted/Blocked Goroutine Starts Running Again?

**Yes.** When a previously preempted goroutine is picked up from the global queue (or wherever it landed), it goes through `schedule()` → `findRunnable()` → `execute(gp, inheritTime=false)`. Since `inheritTime` is `false` (it's getting a fresh time slice, not inheriting one from `runnext`), **schedtick is incremented**.

The only time schedtick is NOT incremented is when a goroutine comes from `runnext` with `inheritTime=true` — meaning it's sharing the time slice of the goroutine that readied it.

To be precise:
- Preempted G sits in global queue → some M picks it up → `execute(gp, false)` → **schedtick++**
- Channel-blocked G unblocked → goes to sender's `runnext` → `execute(gp, true)` → **no schedtick++** (inheriting time slice)
- G from local queue (not runnext) → `execute(gp, false)` → **schedtick++**

---

## 22. When Is an OS Thread (M) Killed?

**Almost never.** Go's general policy is to keep idle threads alive (sleeping via `notesleep`) rather than destroying them. An M that was needed once may be needed again, and creating a new thread is expensive.

The exceptions:

| Case | What happens |
|------|-------------|
| **Locked M's goroutine exits without `UnlockOSThread()`** | The M is terminated via `mexit()`. This is the one normal-path case where Go kills a thread. Reason: the thread may carry tainted kernel state (changed namespace, signal mask, etc.) that would be dangerous to reuse. |
| **Program exits** | All threads are terminated by the OS. |
| **Thread limit exceeded** | If the runtime tries to create a new M and `sched.mnext - sched.nmfreed >= sched.maxmcount` (default 10,000), the runtime crashes with `"program exceeds N-thread limit"`. This is a **fatal error**, not graceful degradation. |

There is an open feature request ([golang/go#14592](https://github.com/golang/go/issues/14592)) to let idle threads exit after some timeout, but as of Go 1.23+ this is not implemented — idle Ms persist for the lifetime of the program.

*Source: [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go) (`goexit0`, `mexit`), [`pkg.go.dev/runtime#LockOSThread`](https://pkg.go.dev/runtime#LockOSThread), [golang/go#14592](https://github.com/golang/go/issues/14592).*

---

## 23. Channels vs Unix Pipes

Good instinct — they're conceptually similar (both pass data between concurrent entities through a FIFO), but they differ in almost every implementation detail:

| Aspect | Unix Pipe | Go Channel |
|--------|-----------|------------|
| **Implemented in** | Kernel space | User space (entirely in the Go runtime) |
| **Sending/receiving** | `write(fd, ...)` / `read(fd, ...)` — syscalls | `ch <- v` / `<-ch` — compiled to `runtime.chansend` / `runtime.chanrecv`, no syscalls |
| **Data path** | Data goes through a kernel buffer (pipe buffer, default 64KB) | Buffered: ring buffer in heap memory. Unbuffered: **direct copy** from sender's stack to receiver's stack (zero intermediate buffer) |
| **Blocking mechanism** | Thread blocks in kernel (`TASK_INTERRUPTIBLE`) | Goroutine parks via `gopark()` — the M is free to run other goroutines |
| **Works across** | Processes (even unrelated ones, via named pipes) | Goroutines within a single process only |
| **Cost of send** | ~1-5μs (syscall overhead) | ~100-300ns (no syscall, just memory operations + goroutine scheduling) |
| **Type safety** | Untyped byte stream | Typed — `chan int`, `chan string`, `chan MyStruct` |
| **Directionality** | Unidirectional (separate read/write FDs) | Bidirectional by default, can be restricted (`chan<-`, `<-chan`) |

The critical difference for the scheduler: when a goroutine blocks on a channel, **the OS thread does NOT block**. The goroutine is parked (`_Gwaiting`), and the M immediately runs another goroutine. With Unix pipes, the thread itself blocks in the kernel, tying up a whole OS thread.

---

## 24. `LockOSThread()` — Deep Dive

### What it does

```go
runtime.LockOSThread()
```

Wires the calling goroutine to its current OS thread. Two fields track this:

```go
// In runtime2.go:
type g struct {
    lockedm muintptr   // M this G is locked to
}
type m struct {
    lockedg guintptr   // G this M is locked to
}
```

While locked:
- **This goroutine can only run on this M.** The scheduler enforces this in `schedule()`.
- **This M can only run this goroutine.** No other goroutine will be scheduled onto it.
- **The P is NOT locked.** It can be detached and given to another M.

### Why it exists — namespace example

Linux namespaces are **per-thread**, not per-process. When you call `setns(fd, CLONE_NEWNET)`, it changes the network namespace of the **calling OS thread only**. Without `LockOSThread`, the Go scheduler could reschedule your goroutine onto a different thread between `setns()` and your next network operation — silently breaking your namespace isolation.

```go
func doInNamespace(nsPath string) error {
    // Pin this goroutine to its current OS thread
    runtime.LockOSThread()
    defer runtime.UnlockOSThread()

    // Save the current namespace so we can restore it
    origNS, err := netns.Get()
    if err != nil {
        return err
    }
    defer origNS.Close()

    // Open and switch to the target namespace
    newNS, err := netns.GetFromPath(nsPath)
    if err != nil {
        return err
    }
    defer newNS.Close()

    // setns() changes THIS THREAD's namespace
    if err := netns.Set(newNS); err != nil {
        return err
    }
    defer netns.Set(origNS) // restore on exit

    // Everything here runs in the target namespace.
    // Without LockOSThread, the goroutine could be moved to a
    // different thread (which is still in the original namespace)
    // between any two of these lines.
    conn, err := net.Dial("tcp", "10.0.0.1:80")
    if err != nil {
        return err
    }
    defer conn.Close()
    // ...
    return nil
}
```

### What happens when a locked goroutine...

#### Blocks on a channel or mutex

1. `gopark()` → G status changes to `_Gwaiting`
2. `schedule()` is called on this M
3. `schedule()` sees `m.lockedg != 0` and the locked G is not runnable
4. Calls `stoplockedm()`:
   - **Releases the P** via `releasep()` → `handoffp()` (P is given to another M or put in `sched.pidle`)
   - Marks itself idle-locked (`incidlelocked(1)`)
   - **Parks via `mPark()`** (futex sleep) — the M does NOT run other goroutines, it sleeps exclusively waiting for its locked G
5. When the G is unblocked (`goready()` → `runqput()`), some other M's `schedule()` dequeues it, sees `gp.lockedm != 0`, and calls `startlockedm(gp)`:
   - Gives its own P to the locked M (`mp.nextp = pp`)
   - Wakes the locked M (`notewakeup(&mp.park)`)
   - Parks itself (`stopm()`)
6. The locked M wakes up, acquires the P, runs its locked G

**Key point:** The locked M does NOT continue running other goroutines while its G is blocked. It parks and waits exclusively. The P is freed for other Ms to use.

#### Blocks in a syscall

Same as any M in a syscall — the P may be detached by sysmon via `handoffp()`. When the syscall returns, the goroutine tries to re-acquire its old P (or any P). The lock relationship (`lockedm`/`lockedg`) persists through the syscall — the goroutine stays bound to this M throughout.

#### Completes naturally (without `UnlockOSThread()`)

From the official docs:
> "If the calling goroutine exits without unlocking the thread, the thread will be terminated."

1. `goexit()` → `goexit0()`
2. `goexit0()` sees `m.lockedExt > 0` (locked via public API)
3. The M is routed to `mexit()` — the OS thread is **terminated**
4. The M is NOT returned to `sched.midle` — it's gone forever

This is one of the **only cases where Go kills an OS thread**. The reason: the thread may carry tainted kernel state (changed namespace, signal mask, etc.) that would be dangerous if another goroutine inherited it.

*Source: [Commit d0f8a75](https://github.com/golang/go/commit/d0f8a7517ab0b33c8e3dd49294800dd6144e4cee) — fixed a bug where `lockedExt` was incorrectly cleared on exit, potentially leaking namespace-tainted threads back into the pool.*

#### Gets preempted by sysmon

**Yes, locked goroutines CAN be preempted.** `preemptone()` has no special case for locked goroutines:

```go
func preemptone(_p_ *p) bool {
    mp := _p_.m.ptr()
    gp := mp.curg
    // No check for gp.lockedm here
    gp.preempt = true
    gp.stackguard0 = stackPreempt
    preemptM(mp)  // send SIGURG
    return true
}
```

When the locked G is preempted:
1. G goes to the global run queue as `_Grunnable`
2. The locked M enters `schedule()` → sees `m.lockedg` not runnable → `stoplockedm()` → releases P, parks
3. Some other M dequeues the G from global queue → sees `gp.lockedm != 0` → `startlockedm()` → hands P to locked M, wakes it
4. Locked M wakes, runs the G again

The preemption is essentially a brief detour — the G takes a trip through the global queue and comes right back to its locked M. This maintains preemption fairness even for locked goroutines.

#### Gets stolen by another P?

**The goroutine itself cannot be stolen.** When work stealing picks up a G with `gp.lockedm != 0`, `schedule()` redirects it back to its locked M via `startlockedm()`. But the **P's other goroutines** (in its local runq) can absolutely be stolen by other Ps — the P is not locked, only the G-to-M binding is.

---

## Additional Sources (Part 2)

- Linux kernel [`include/linux/sched.h`](https://github.com/torvalds/linux/blob/master/include/linux/sched.h) — thread state constants
- [`futex(2)` man page](https://man7.org/linux/man-pages/man2/futex.2.html) — futex syscall semantics
- [`ps(1)` man page](https://man7.org/linux/man-pages/man1/ps.1.html) — process state codes
- [`runtime/lock_futex.go`](https://github.com/golang/go/blob/master/src/runtime/lock_futex.go) — `notesleep`, `notewakeup`, `noteclear`
- [`runtime/os_linux.go`](https://github.com/golang/go/blob/master/src/runtime/os_linux.go) — `futexsleep`, `futexwakeup`
- [`runtime/sys_linux_amd64.s`](https://github.com/golang/go/blob/master/src/runtime/sys_linux_amd64.s) — `usleep` assembly stub
- [`runtime/sema.go`](https://github.com/golang/go/blob/master/src/runtime/sema.go) — semaphore implementation, `semaRoot`, `sudog`
- [`runtime/runtime2.go`](https://github.com/golang/go/blob/master/src/runtime/runtime2.go) — `schedt`, `g`/`m`/`p` structs, all state enums
- [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go) — `schedule`, `execute`, `goready`, `stopm`, `startm`, `stoplockedm`, `startlockedm`, `goexit0`, `mexit`, `preemptone`
- [`pkg.go.dev/runtime#LockOSThread`](https://pkg.go.dev/runtime#LockOSThread) — official documentation
- [Commit d0f8a75](https://github.com/golang/go/commit/d0f8a7517ab0b33c8e3dd49294800dd6144e4cee) — `lockedExt` fix for namespace safety
- [`setns(2)` man page](https://man7.org/linux/man-pages/man2/setns.2.html) — per-thread namespace semantics
- [golang/go#14592](https://github.com/golang/go/issues/14592) — "let idle OS threads exit" (open, not implemented)
- [golang/go#20395](https://github.com/golang/go/issues/20395) — LockOSThread thread termination design
