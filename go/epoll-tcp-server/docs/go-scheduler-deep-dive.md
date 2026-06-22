# The Go Scheduler: Queues, Fairness, and What Happens Under the Hood

> Based on [GopherCon 2021: Queues, Fairness, and The Go Scheduler - Madhav Jivrajani](https://youtu.be/wQpC99Xu1U4), extended with additional research.

---

## 1. Why a User-Space Scheduler?

When you write `go doSomething()`, the Go runtime creates a goroutine — a lightweight, user-space thread managed entirely by the Go runtime, not by the operating system kernel.

The distinction matters. An OS thread costs roughly 1 MB of stack memory at creation. A goroutine starts with a stack of just 2–8 KB and grows as needed. OS thread context switches require a trip into kernel space (1–10 microseconds). Goroutine context switches happen entirely in user space (~100 nanoseconds, roughly 10–100x cheaper).

This is why Go can handle millions of goroutines on hardware that would choke on a few thousand OS threads. But it creates a fundamental problem: the OS kernel knows nothing about goroutines. It only knows about threads. So the Go runtime must act as a middleman — a user-space scheduler that maps goroutines onto OS threads so the kernel can actually execute them on hardware.

Think of it as a compiled Go binary having two logical parts:

1. **Your application code** — functions you wrote
2. **The Go runtime** — a scheduler, garbage collector, memory allocator, and more

Your code calls into the runtime constantly. When you write `go doSomething()`, the compiler translates that into a call to `runtime.newproc`, which creates a new goroutine. The runtime decides when and where to actually run it.

---

## 2. The N:M Scheduling Model

There are three common approaches to mapping user-space concurrency units onto OS threads:

**1:1 (Kernel threading):** Each user-space thread maps directly to one OS thread. Java (before Project Loom), C pthreads, and Rust's std::thread use this. Simple, but expensive — you are limited by how many OS threads the kernel can manage, and each one costs a full stack allocation.

**N:1 (Green threads):** All user-space threads run on a single OS thread. Early Ruby and Python (with the GIL) effectively do this. Lightweight, but you cannot use more than one CPU core, and one blocking syscall blocks everything.

**N:M (Hybrid):** N user-space threads are multiplexed onto M OS threads, where N >> M. Go, Erlang's BEAM VM, and Rust's Tokio runtime use this model.

Go uses N:M scheduling. This gives you:

- **Flexibility**: millions of goroutines on a handful of OS threads
- **Parallelism**: work runs on multiple cores simultaneously
- **Resilience**: if one goroutine blocks on a syscall, other goroutines can be moved to a different thread

The critical advantage: when a goroutine blocks on a syscall and its OS thread blocks with it, the scheduler can take the other goroutines that were queued behind it and move them to a different, non-blocked thread. Without N:M scheduling, those goroutines would be stuck waiting.

---

## 3. The GMP Model

Go's scheduler is built around three core entities, commonly called GMP:

### G — Goroutine

A goroutine is a heap-allocated struct (`runtime.g` in `runtime/runtime2.go`) that holds:
- The goroutine's stack (initially 2–8 KB, growable)
- The instruction pointer (where execution should resume)
- The current status (running, runnable, waiting, etc.)
- The channel it might be blocked on

### M — Machine (OS Thread)

An M (`runtime.m`) represents an actual operating system thread. It is what the kernel schedules onto a CPU core. An M executes Go code by running a goroutine, but it can also be parked (idle) or blocked in a syscall.

The Go runtime can create new Ms as needed — for example, when a goroutine enters a blocking syscall. The total number of Ms is *not* bounded by GOMAXPROCS. You can have hundreds of Ms if many goroutines are blocked in syscalls. Only GOMAXPROCS of them will be actively executing Go code at any time.

### P — Processor (Logical Processor)

A P (`runtime.p`) is the key abstraction that ties everything together. It represents the *right to execute Go code*. A P is not a CPU core — it is a resource that an M must acquire before it can run goroutines.

The number of Ps is always equal to `GOMAXPROCS` (default: number of CPU cores).

Each P owns:
- A **local run queue** (LRQ) of goroutines ready to run (fixed capacity: 256 goroutines)
- Per-P caches for memory allocation (mcache)
- Other per-P state the scheduler needs

**Why does P exist?** Before Go 1.1, the scheduler only had Gs and Ms. The per-thread state (local run queues, memory caches) was stored directly on the M. This caused two problems:

1. **Wasted state on blocked threads**: when an M blocked in a syscall, its local run queue and memory caches sat unused. The goroutines in that queue were stuck.
2. **Unbounded work stealing**: to steal work, an idle M had to scan all other Ms. Since Ms can be created dynamically, this set is unbounded.

The P solves both problems. When an M blocks in a syscall, the P (with its local run queue and caches) is detached and handed to a different M that can use it. And since the number of Ps is fixed at GOMAXPROCS, work stealing only needs to check a bounded number of targets.

This design was introduced by Dmitry Vyukov in 2012 in his [Scalable Go Scheduler Design Doc](https://docs.google.com/document/d/1TTj4T2JO42uD5ID9e89oa0sLKhJYD0Y_kqxDv3I3XMw).

### The picture

```
              Global Run Queue (mutex-protected)
              [G5] [G6] [G7] ...

    P0                          P1
    Local Run Queue             Local Run Queue
    [G1] [G2] [G3]              [G4]

    ↕ (bound)                   ↕ (bound)
    M0 (OS Thread)              M1 (OS Thread)
    currently running: G1       currently running: G4

    ↕ (scheduled by kernel)     ↕
    CPU Core 0                  CPU Core 1
```

### Thread States: Parking, Spinning, and Sleeping

These terms describe what Ms, Gs, and Ps are doing when they are *not* actively running your code.

**Parking** means different things depending on what is being parked:

- **Parking a goroutine (G)** — `gopark()` in `proc.go`. The G's status changes from `_Grunning` to `_Gwaiting`, and it is **removed from all run queues entirely**. It sits inert, referenced only by whatever is holding it (a channel's wait queue, a mutex's wait list). The M immediately calls `schedule()` to run a different goroutine. The G stays parked until something calls `goready()` on it (e.g., data arrives on a channel).

- **Parking an M (OS thread)** — `stopm()` in `proc.go`. The M is added to `sched.midle` (idle-M list) and its OS thread blocks via `notesleep(&mp.park)` — a futex-like wait. **Zero CPU consumed.** Another M calls `notewakeup(&mp.park)` to bring it back when work appears.

- **Parking a P** — there is no `parkP()` function. A P is simply marked `_Pidle` and pushed onto `sched.pidle` via `pidleput()`. Since a P is just a data structure (run queue + caches), it doesn't "sleep" — it sits on the list until some M claims it via `pidleget()` / `startm()`.

**Spinning** is an M that holds a P (consuming a CPU core) but has no goroutine to run yet. Instead of immediately sleeping, it actively loops looking for work — checking other Ps' local queues, the global queue, and the netpoller. The field `m.spinning` marks this state; `sched.nmspinning` counts how many Ms are spinning globally.

Why not just sleep immediately? Creating/waking an OS thread costs 1–10μs. If new work appears 100ns later, a spinning M grabs it instantly — a sleeping M would need an expensive wake-up first. The runtime keeps a small number of Ms spinning briefly to amortize latency for bursty workloads. If a spinning M finds nothing after exhausting all work sources, it gives up its P and sleeps (`stopm()`).

**Sleeping** is the OS-level mechanism underlying both parked Ms and sysmon's idle loop. The kernel removes the thread from its runnable list — zero CPU cycles consumed. This is fundamentally different from spinning, where the CPU continuously executes polling instructions.

| State | CPU usage | Who does it | How it ends |
|-------|-----------|-------------|-------------|
| **Spinning** | 100% (polling loop) | M with a P but no G to run | Finds work → runs it; or gives up P → parks |
| **Parked/Sleeping** | 0% (kernel-descheduled) | M with no work, or G waiting on I/O/channel | Explicit wake-up (`notewakeup`, `goready`) |
| **Idle** | 0% | P in `sched.pidle`, or M in `sched.midle` | Claimed by `startm()` or `pidleget()` |
| **Blocked** | 0% (stuck in syscall) | M in a blocking syscall, or G in `_Gwaiting` | Syscall returns; or event fires (`goready`) |

### What "Calling Into the Runtime" Means

When you read that "the application calls into the scheduler," it refers to specific code paths where control transfers from your code into runtime-owned functions:

| Trigger | Runtime function | What happens |
|---------|-----------------|--------------|
| **Function calls** | `morestack` → `newstack()` | Compiler inserts a stack-growth-check prologue in every function. The runtime piggybacks preemption checks here. |
| **Channel operations** | `chansend()` / `chanrecv()` | Calls `gopark()` when the goroutine must wait. |
| **Memory allocation** | `mallocgc()` | Large allocations can trigger GC assist or stack checks. |
| **System calls** | `entersyscall()` / `exitsyscall()` | Hands the P off when the M blocks in the OS. |
| **`go func()`** | `newproc()` | Creates a new G and places it in the run queue. |
| **Explicit yields** | `Gosched()`, `select{}`, `time.Sleep()` | Voluntarily enters `schedule()`. |

A tight loop like `for { x = x*x + 1 }` with no function calls, no allocations, no channel ops — **never calls into the runtime** (pre Go 1.14). That is exactly why asynchronous preemption via SIGURG was needed.

Note: `schedtick` does *not* increment on every runtime call. It increments inside `execute()` — it counts scheduling loop iterations on a P, not the frequency of runtime entry. See [Section 8](#8-the-schedtick-counter-and-the-magic-number-61) for details.

---

## 4. Run Queues and Finding Work

When a goroutine finishes or blocks and a P needs a new goroutine to run, the scheduler executes a function called `findRunnable()` in `runtime/proc.go`. It checks sources of work in this order:

### Step 1: Check the local run queue

The P checks its own LRQ first. This requires no lock (only this P accesses its own LRQ), so it is extremely fast.

The LRQ has two parts, checked in order:

1. **`runnext`** — a single-slot fast path. If occupied, the goroutine here runs next and **inherits the remaining time slice** of the goroutine that readied it (`inheritTime = true`, so `schedtick` is not incremented). See [Section 9](#9-lifo-locality-and-time-slice-inheritance).
2. **`runq`** — a 256-slot circular buffer, strictly **FIFO** (enqueue at tail via `runqput`, dequeue from head via `runqget`).

### Step 2: Check the global run queue

If the LRQ is empty, the P acquires the global run queue's mutex and checks there. The global run queue is also FIFO — `globrunqput()` pushes to the back, `globrunqget()` pops from the front.

**How many goroutines does it take?** This depends on *why* the P is checking:

- **From `findRunnable()`** (P is completely out of work): it takes a **proportionate share**, not just one:

  ```
  steal count = min(len(global_queue) / GOMAXPROCS + 1, len(global_queue)/2)
  ```

  By taking its "fair share," the scheduler reduces how often Ps need to grab the global lock and leaves a proportionate share for other Ps.

- **From `schedule()` on the 61st schedtick** (fairness check, P may still have local work): it takes **exactly 1** goroutine — `globrunqget(_p_, 1)` passes a hard-coded `max=1`. This goroutine is executed immediately (not queued into `runnext` or `runq`). See [Section 8](#8-the-schedtick-counter-and-the-magic-number-61).

### Step 3: Check the network poller (netpoller)

The netpoller handles asynchronous I/O — goroutines waiting on network reads/writes, channel operations, and timers. If a goroutine's I/O has completed, the netpoller returns it as runnable.

This is critical: network I/O in Go does *not* block an OS thread. When a goroutine does a `net.Conn.Read()`, the runtime uses epoll (Linux), kqueue (macOS), or IOCP (Windows) under the hood. The goroutine is parked on the netpoller, and the M is free to run other goroutines. When data arrives, the netpoller marks the goroutine runnable again.

### Step 4: Steal from another P

If local queue, global queue, and netpoller are all empty, the P resorts to work stealing. It:

1. Randomly picks another P
2. Checks if that P has goroutines in its LRQ
3. If yes, steals **half** of them — from the **head** of the victim's queue
4. Retries up to **4 times** with different random targets

Random victim selection avoids the thundering herd problem (all idle Ps trying to steal from the same busy P). Stealing half amortizes the overhead — instead of going back to steal again after running one goroutine, the thief has a batch of work.

**Why steal from the head?** Cache locality. The goroutines near the **tail** were just enqueued — likely spawned by the goroutine the victim P is currently running, so they're hot in that core's L1/L2 cache. The goroutines near the **head** are older and colder. The thief takes the cold ones, leaving the hot ones for the owner. Both the owner (`runqget`) and the thief (`runqgrab`) operate on the head side — the owner dequeues one at a time, the thief grabs a contiguous chunk in one atomic CAS (`atomic.CasRel(&pp.runqhead, h, h+n)`).

On the last of several steal attempts, the thief may also take the victim's `runnext` slot (`stealRunNextG=true`), but only after a brief yield to reduce racing with a goroutine that's about to consume its own `runnext`.

If after 4 retries the P still has no work, it parks itself in an idle list and goes to sleep until new work appears.

---

## 5. Fairness and the Convoy Effect

### The supermarket problem

Imagine a supermarket checkout line (FIFO queue). One customer has 25 items. Every customer behind them waits. The cashier spends disproportionate time on the first customer while short-order customers — who would take 30 seconds each — wait minutes.

This is the **convoy effect**: a resource-intensive task at the head of a FIFO queue delays all subsequent tasks, regardless of their own resource requirements. It was first studied in the context of lock scheduling in operating systems and is a fundamental problem in any FIFO-based scheduler.

Go faces this problem directly: what happens when a goroutine runs a tight infinite loop?

```go
go func() {
    for {
        // CPU-intensive work, no function calls, no channel ops
        x = x*x + 1
    }
}()
```

Without preemption, this goroutine never voluntarily yields the processor. Every other goroutine on the same P starves.

---

## 6. Preemption: The Evolution

### Go 1.0 — Purely cooperative

In Go 1.0, preemption was entirely cooperative. A goroutine only yielded when it explicitly called into the runtime — via a channel operation, a syscall, a memory allocation, or a function call. A tight loop with no function calls could run forever, starving all other goroutines.

### Go 1.2 — Function call preemption

Go 1.2 added preemption checks at function call boundaries. Here is how it works:

Go uses segmented stacks (later [contiguous stacks](https://blog.cloudflare.com/how-stacks-are-handled-in-go/)). Every function call begins with a **stack growth check** — the compiler inserts a prologue that checks whether the goroutine's stack needs to grow. The runtime piggybacks preemption onto this check. If a goroutine has been running for more than 10ms, the runtime sets a flag (`stackPreempt`) on the goroutine. At the next function call, the stack growth check sees this flag and yields.

The problem: the compiler can **inline** small functions. When a function is inlined, there is no actual function call, so there is no stack growth check, so there is no preemption point. A tight loop calling only inlined functions behaves exactly like Go 1.0 — it never yields.

### Go 1.14 — Non-cooperative (asynchronous) preemption

Go 1.14 fixed this once and for all with asynchronous preemption. The mechanism:

1. Each goroutine gets a **time slice of 10 milliseconds** (soft limit)
2. The **sysmon daemon** (a special goroutine running without a P) continuously monitors running goroutines
3. When sysmon detects a goroutine has been running for >10ms, it sends a **SIGURG signal** to the OS thread executing that goroutine
4. The signal handler saves the goroutine's register state and yields

**Why SIGURG?** Austin Clements [explained the choice](https://youtu.be/1I1WmeSjRSw) in detail. The requirements were:
- Must be a signal not used by libc internally
- Must not be used by debuggers (like SIGTRAP or SIGSTOP)
- Must not be used by other common runtime systems
- Must not have a default action of terminating the process

SIGURG (urgent condition on socket) meets all criteria. It is extremely rarely used by applications, its default action is to be ignored, and it does not conflict with common signal-based tools.

### Where do preempted goroutines go?

When a goroutine is preempted for running too long, it is placed in the **global run queue**, not back in the local run queue. This is a deliberate fairness decision.

If the preempted goroutine went back to the local run queue:

```
[preempted-G] [G1] [G2] [G3] ... in local queue
→ G1 runs (short, finishes in 1ms)
→ G2 runs (short, finishes in 1ms)
→ G3 runs (short, finishes in 1ms)
→ preempted-G runs again (another 10ms slice)
→ G4 has to wait 10ms before getting its turn
```

The short-lived goroutines repeatedly get stuck behind 10ms slices from the preempted goroutine. By putting preempted goroutines in the global queue instead, they effectively go to the back of the line — other Ps' goroutines get priority.

### The full preemption chain (step by step)

1. **Detection** — `sysmon()` periodically calls `retake()`, which walks all Ps. For each P in `_Prunning`, it compares the saved `schedtick` against the current value plus a time threshold (`forcePreemptNS = 10ms`). If the schedtick hasn't advanced (same G running) for ≥10ms, `retake()` calls `preemptone(pp)`.

2. **Signal** — `preemptone()` sets `gp.preempt = true` and `gp.stackguard0 = stackPreempt` (for the cooperative path if a function call happens first). It also calls `preemptM(mp)`, which sends **SIGURG** to the specific M running that goroutine.

3. **Handler** — `sighandler()` in `signal_unix.go` receives SIGURG and calls `doSigPreempt(gp, ctxt)`. If the G is at an async-safe point (`isAsyncSafePoint()`), the handler rewrites the signal context so that when it returns, execution jumps into `asyncPreempt` instead of resuming the interrupted instruction.

4. **Yield** — `asyncPreempt` (assembly, per-arch) saves the G's register state onto its stack, then calls `asyncPreempt2()` → `gopreempt_m(gp)` → `goschedImpl(gp)`:
   - `casgstatus(gp, _Grunning, _Grunnable)`
   - `globrunqput(gp)` — the G goes to the **global** run queue
   - Calls `schedule()`

5. **Next G** — The **same M** (still holding its P) now runs `schedule()` → `findRunnable()` → `execute()`. No external entity assigns work to the M — the M is a self-scheduling loop. It checks local queue → global queue → netpoller → steals from other Ps. Once it finds a runnable G, `execute()` sets it to `_Grunning` and jumps into it via `gogo(&gp.sched)`.

Key point: **no new thread is created for this**. The same M, with the same P, simply picks up a different G.

---

## 7. The sysmon Daemon

The sysmon goroutine (`runtime.sysmon()` in `proc.go`) is a background daemon with special privileges:

- It runs on a **dedicated M without a P** — so it cannot be preempted and does not compete for a processor slot. This is deliberate: if sysmon needed a P, it could be starved by all Ps being busy running long goroutines — exactly the situation sysmon exists to detect and fix.
- It is started from `runtime.main()` via `newm(sysmon, nil, -1)` — the `nil` P argument is what makes this M permanently P-less. This happens before the user's `main()` body executes.
- It runs for the entire lifetime of the program.

**How many threads at startup?** Go creates threads lazily, not eagerly. At startup there are exactly **two OS threads**: M0 (the main OS thread running `main()`) and sysmon's M. There are GOMAXPROCS P structs allocated by `procresize()`, but only P0 is attached to M0 — the rest sit idle in `sched.pidle` with no M. Additional Ms are created on demand (via `startm()`/`newm()`) when goroutines need to run in parallel. You might eventually have far more than GOMAXPROCS threads if many goroutines block in syscalls.

Its responsibilities:

1. **Preemption**: scans for goroutines running >10ms and sends SIGURG
2. **Syscall handoff**: detects Ps stuck in the "syscall" state for too long and initiates handoff (detaching the P from the blocked M)
3. **Netpoller**: periodically polls the network poller to make ready goroutines runnable
4. **Force GC**: triggers garbage collection if it hasn't run recently
5. **Timers**: fires expired timers

Sysmon does not spin — it never enters the `spinning` state and is never counted in `sched.nmspinning`. Unlike worker Ms that spin (hold a P, burn CPU cycles polling for work), sysmon has no P and is not trying to steal goroutines to execute. It does monitoring work and then actually sleeps via `usleep(delay)` — its OS thread is genuinely descheduled by the kernel between iterations, consuming zero CPU while asleep.

It uses an adaptive sleep schedule:
- Starts sleeping for **20 microseconds**
- After ~50 consecutive idle cycles (no retakes or preemptions needed), it doubles the sleep interval
- Maximum sleep: **10 milliseconds**
- Snaps back to short intervals when activity resumes

---

## 8. The Schedtick Counter and the Magic Number 61

There is a subtle fairness problem between local and global run queues. A busy P with a full local queue might never check the global queue — and goroutines sitting in the global queue starve.

The solution is a counter called `schedtick` (a field on each P, `p.schedtick` in `runtime2.go`). It is incremented inside `execute()` each time the scheduler begins executing a runnable goroutine **with a fresh time slice**:

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

When `inheritTime` is true (the goroutine came from `runnext` and is inheriting the remaining time slice), `schedtick` is **not** incremented. Preemption itself does not increment schedtick either — the increment only happens when the *next* goroutine starts running with a fresh slice.

Every time `schedtick` is a multiple of **61**, the scheduler checks the global run queue *before* checking the local queue. It takes **exactly one** goroutine (`globrunqget(_p_, 1)`) and executes it immediately — this is a lightweight fairness check, not a bulk drain:

```go
// In schedule():
if _g_.m.p.ptr().schedtick%61 == 0 && sched.runqsize > 0 {
    lock(&sched.lock)
    gp = globrunqget(_g_.m.p.ptr(), 1)   // max = 1
    unlock(&sched.lock)
}
```

### Why 61?

Three constraints:

1. **Not too large** — if the check is too infrequent, global queue goroutines starve
2. **Not too small** — if the check is too frequent, you get unnecessary lock contention on the global queue and unfairness toward local goroutines
3. **Must be prime** — this is the interesting one

The frequency at which an application calls into the scheduler (and increments schedtick) tends to follow patterns aligned with powers of two. A goroutine that runs a loop body every 16 iterations before yielding has a scheduler frequency of 16. Another common one is 32, or 64.

If the global-queue-check interval were 64, it would synchronize (collide) with the application's pattern — every time the application calls into the scheduler, the global check fires too, creating a burst of global queue contention. This is the same principle as choosing prime hash table sizes: you want to avoid harmonic interference.

61 is prime, not close to any power of two, and small enough to prevent starvation. The collisions between a frequency of 61 and a frequency of 8, 16, 32, or 64 are minimized because 61 shares no factors with any of them.

Think of it like a sine wave visualization:
- frequency-8 and frequency-64 waves cross zero together very often (many collisions)
- frequency-8 and frequency-61 waves rarely cross zero together (few collisions)

This is the same principle behind why good hash table implementations use prime-sized buckets.

---

## 9. LIFO, Locality, and Time Slice Inheritance

When a goroutine spawns a new goroutine, where does the child go?

### The FIFO approach (put at tail)

The child goes to the tail of the local run queue. Fair — first created, first run. But terrible for locality. Consider a producer-consumer over an unbuffered channel:

```go
go func() {
    for {
        ch <- produceMessage()  // sender
    }
}()

go func() {
    for {
        process(<-ch)           // receiver
    }
}()
```

If the receiver blocks waiting for data, then the sender sends data and unblocks the receiver, the receiver goes to the tail of the queue. If the queue has 200 goroutines in it, each running for up to 10ms, the receiver waits up to **2 seconds** (200 x 10ms) before it processes the message. The message is sitting there, the receiver is ready, but it has to wait behind hundreds of unrelated goroutines.

### The LIFO approach (put at head)

The child goes to the head of the queue and runs immediately. Great for locality — the producer and consumer can ping-pong rapidly, processing messages with minimal latency.

But pure LIFO has a starvation risk: two goroutines communicating over a channel could monopolize the processor indefinitely, starving all other goroutines in the queue.

### Go's solution: LIFO + Time Slice Inheritance

Go uses a **`runnext` slot** — a single-goroutine fast path on each P, **separate from the 256-slot circular `runq`**. When a goroutine readies another goroutine (via `go func()` or by sending on a channel), the newly readied goroutine goes into `runnext` via `runqput(_p_, gp, next=true)`. The previous occupant of `runnext` (if any) gets pushed into the tail of `runq`. The scheduler checks `runnext` before `runq`, giving it LIFO-like priority.

To be precise: the circular `runq` buffer itself is strictly **FIFO** (enqueue at tail, dequeue from head). The LIFO-like behavior comes entirely from the `runnext` slot sitting in front of the FIFO queue.

The twist: the spawned/unblocked goroutine **inherits the remaining time slice** of the goroutine that spawned or unblocked it.

If goroutine A has been running for 3ms of its 10ms slice and then spawns goroutine B:
- B gets placed at the head of the local queue
- B inherits only 7ms (10ms - 3ms) of time slice
- Together, A and B share a single 10ms window

This prevents the ping-pong starvation problem. Even if A and B bounce back and forth over a channel, they share 10ms total, after which one of them gets preempted and the other goroutines get their turn.

The impact was dramatic. From the commit that implemented this change:

```
BenchmarkPingPongHog   1,607,649ns → 1,963ns   (99.88% improvement)
```

Improvements were seen across many benchmarks, not just this synthetic one.

### What happens when a goroutine blocks on a channel

When a goroutine executes `<-ch` and the channel is empty:

1. `chanrecv()` (in `runtime/chan.go`) builds a `sudog` (a waiter struct)
2. Enqueues it onto `c.recvq` — **the channel's own wait queue**, not any P run queue
3. Calls `gopark(chanparkcommit, ..., waitReasonChanReceive, ...)`
4. `gopark()` → G's status changes to `_Gwaiting` → the G is **removed from all run queues**
5. The M calls `schedule()` and runs a different goroutine

The blocked G exists only as a `sudog` reference inside the channel's `recvq`. It is not in any P's local queue or the global queue.

When a sender sends on the channel:

1. `chansend()` / `send()` finds the waiting receiver in `c.recvq`
2. Copies the value directly to the receiver's memory (zero-copy, no intermediate buffer)
3. Calls `goready(gp)` → `ready(gp, traceskip, next=true)`
4. `ready()` → G's status changes to `_Grunnable` → `runqput(_p_, gp, true)`

Because `next=true`, the unblocked goroutine goes into the **`runnext` slot** of the sender's P — not the local queue tail, not the global queue. It gets priority to run next and inherits the sender's remaining time slice. This is the same `runnext` + time slice inheritance mechanism described above, applied to channel communication.

---

## 10. Syscall Handoff

When a goroutine makes a blocking syscall (like reading from disk), the OS thread blocks. The P, its local queue, and its caches would all be stuck waiting. The scheduler handles this through **handoff**:

### The optimistic path

For most syscalls, the scheduler is optimistic:

1. The goroutine enters the syscall
2. The P's status changes to "syscall" (`_Psyscall`)
3. If the syscall returns quickly, the P resumes as if nothing happened

This avoids the cost of creating or waking a new thread for short syscalls.

### The pessimistic path (sysmon intervention)

If sysmon detects a P has been in the syscall state for too long:

1. **`releasep()`** — the P is detached from the blocked M
2. A new or idle M is found (or created) and acquires the P
3. The new M starts running goroutines from the P's local queue

### The immediate handoff path

For syscalls the runtime *knows* will block for a long time (like `select()` or `read()` on certain file descriptors), handoff happens immediately without waiting for sysmon.

### Returning from a syscall

When the goroutine's syscall finally returns:

1. It tries to re-acquire its **old P** (for cache locality)
2. If the old P is busy, it tries to acquire any idle P
3. If no P is available, the goroutine goes into the **global run queue**

---

## 11. Runtime APIs

The runtime exposes a few knobs. The talk's advice: treat the runtime as a black box first. Only use these APIs when absolutely necessary.

### `runtime.GOMAXPROCS(n int)`

Sets the number of Ps (and thus the max number of threads actively running Go code). Default is the number of CPU cores.

Changing GOMAXPROCS at runtime triggers a **stop-the-world** pause: the scheduler must resize the P list, potentially creating or destroying Ps. Do not change it frequently.

### `runtime.Gosched()`

Voluntarily yields the processor. The calling goroutine is placed in the **global run queue** (not the local queue — it goes to the back of the global line).

If you are considering using this for performance, the improvement is almost always achievable through better algorithm or data structure design instead.

### `runtime.Goexit()`

Terminates only the calling goroutine. If called from the main goroutine, other goroutines continue running — but when they finish, there is nothing to return to. The runtime detects this as a deadlock and crashes. Used internally by `testing.Fatal()`.

### `runtime.LockOSThread()` / `runtime.UnlockOSThread()`

Wires the calling goroutine to its current OS thread. Tracked by `g.lockedm` and `m.lockedg` fields in `runtime2.go`. While locked:
- This goroutine can only run on this M (enforced by `schedule()`)
- This M can only run this goroutine
- The P is NOT locked — it can be detached and given to another M

**When to use it**: when your goroutine changes the underlying thread's per-thread kernel state. Linux namespaces are per-thread (not per-process) — `setns()` changes the namespace of the calling OS thread only. Without `LockOSThread`, the goroutine could be rescheduled onto a different thread between `setns()` and the next network operation.

```go
func doInNamespace(nsPath string) error {
    runtime.LockOSThread()
    defer runtime.UnlockOSThread()

    origNS, _ := netns.Get()
    defer origNS.Close()

    newNS, _ := netns.GetFromPath(nsPath)
    defer newNS.Close()

    netns.Set(newNS)          // changes THIS THREAD's namespace
    defer netns.Set(origNS)   // restore on exit

    // All code here runs in the target namespace, guaranteed.
    conn, err := net.Dial("tcp", "10.0.0.1:80")
    // ...
}
```

**What happens when the locked goroutine blocks (channel, mutex)?** The locked M **releases its P** (via `stoplockedm()` → `releasep()` → `handoffp()`) and **parks** — it does NOT continue running other goroutines. It sleeps exclusively until its locked G becomes runnable again. When the G is unblocked, another M's `schedule()` detects `gp.lockedm != 0` and hands its P to the locked M via `startlockedm()`, waking it.

**What happens when the locked goroutine exits without `UnlockOSThread()`?** The OS thread is **terminated** via `mexit()`. This is one of the only cases where Go kills a thread. Reason: the thread may carry tainted kernel state (changed namespace, signal mask) that would be dangerous to reuse. ([Source: commit d0f8a75](https://github.com/golang/go/commit/d0f8a7517ab0b33c8e3dd49294800dd6144e4cee))

**Can locked goroutines be preempted?** Yes — `preemptone()` has no special case for locked goroutines. The preempted G goes to the global queue, but when dequeued, `schedule()` redirects it back to its locked M via `startlockedm()`.

**WeWork case study**: WeWork published articles about debugging exactly this issue — goroutines changing thread namespace state without `LockOSThread`, causing subtle and hard-to-reproduce networking bugs. Debugging thread state issues across goroutine rescheduling is extraordinarily difficult.

**Gotcha**: if a goroutine locked to a thread spawns a new goroutine, the child has **no guarantee** of running on the same locked thread. Do not assume child goroutines inherit the thread lock.

### `runtime.NumGoroutine()`

Returns the current number of goroutines. Useful for monitoring and debugging goroutine leaks.

---

## 12. Observing the Scheduler

### GODEBUG=schedtrace

```bash
GODEBUG=schedtrace=1000 ./myapp
```

Emits scheduler state every 1000ms to stderr:

```
SCHED 1004ms: gomaxprocs=4 idleprocs=0 threads=5 spinningthreads=0
  idlethreads=1 runqueue=12 [3 4 0 1]
```

This tells you: 4 Ps, none idle, 12 goroutines in global queue, [3, 4, 0, 1] goroutines in each P's local queue.

A more detailed variant:

```bash
GODEBUG=schedtrace=1000,scheddetail=1 ./myapp
```

### go tool trace

The execution tracer gives a visual timeline of goroutine scheduling:

```go
import "runtime/trace"

f, _ := os.Create("trace.out")
trace.Start(f)
defer trace.Stop()
```

Then:

```bash
go tool trace trace.out
```

This opens a browser UI showing exactly when each goroutine ran, on which P, when it was blocked, when it was stolen, and more.

### Linux ftrace for observing SIGURG

Since preemption uses SIGURG (signal 23), you can observe it with Linux's tracing subsystem:

```bash
# Enable signal tracing
echo 1 > /sys/kernel/debug/tracing/events/signal/signal_generate/enable
echo 'sig==23' > /sys/kernel/debug/tracing/events/signal/signal_generate/filter
echo 1 > /sys/kernel/debug/tracing/tracing_on

# Run your Go binary
./myapp &

# Watch the signals
cat /sys/kernel/debug/tracing/trace_pipe | grep myapp
```

You will see SIGURG signals being sent to specific thread IDs at roughly 10ms intervals — direct evidence of the scheduler preempting goroutines.

---

## 13. Putting It All Together

```
                         ┌─────────────────────────┐
                         │     Global Run Queue     │
                         │   (mutex-protected)      │
                         │  [G] [G] [G] [G] ...     │
                         └────────┬────────────────┘
                                  │
                    ┌─────────────┼─────────────┐
                    │             │             │
              ┌─────▼─────┐ ┌────▼──────┐ ┌───▼───────┐
              │    P0      │ │    P1     │ │    P2     │
              │ LRQ: 256   │ │ LRQ: 256  │ │ LRQ: 256  │
              │ [G][G][G]  │ │ [G][G]    │ │ [G]       │
              └─────┬──────┘ └────┬──────┘ └───┬───────┘
                    │             │             │
              ┌─────▼─────┐ ┌────▼──────┐ ┌───▼───────┐
              │    M0      │ │    M1     │ │    M2     │
              │ (OS Thread)│ │(OS Thread)│ │(OS Thread)│
              └─────┬──────┘ └────┬──────┘ └───┬───────┘
                    │             │             │
              ┌─────▼─────┐ ┌────▼──────┐ ┌───▼───────┐
              │  CPU Core  │ │ CPU Core  │ │ CPU Core  │
              └────────────┘ └───────────┘ └───────────┘

         ┌──────────┐              ┌──────────────┐
         │  sysmon  │              │  netpoller   │
         │ (no P)   │              │ (epoll/kqueue)│
         │ preempts │              │ async I/O    │
         │ handoffs │              │ channels     │
         └──────────┘              │ timers       │
                                   └──────────────┘
```

The scheduling loop for each P:

1. Check local run queue → run a G
2. Every 61st schedtick → check global queue first (fairness)
3. If local is empty → steal from global queue (proportionate share)
4. If global is empty → poll netpoller
5. If netpoller is empty → steal half from random other P (retry 4x)
6. If all empty → park the P and sleep

Concurrently:
- sysmon watches for goroutines running >10ms → SIGURG preemption
- sysmon watches for Ps stuck in syscall state → handoff
- Preempted goroutines → global queue (not local)
- Newly spawned/unblocked goroutines → `runnext` slot (LIFO) with inherited time slice

### Startup trace: how Ps get filled with goroutines

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

**Step 1 — Startup.** `schedinit()` calls `procresize(4)`, allocating 4 P structs. P0 attaches to M0 (the main OS thread). P1–P3 sit idle in `sched.pidle`. Sysmon's M (P-less) is also running.

```
M0/P0: running [main goroutine]
sysmon: running (no P, sleeping between checks)
P1, P2, P3: _Pidle in sched.pidle, no M attached
```

**Step 2 — Each `go func(n)` → `newproc()` → `runqput(P0, newG, true)`.** All 8 goroutines land on P0 initially, because `main` runs on P0. Each new G goes into P0's `runnext`, bumping the previous occupant into `runq`'s tail:

```
After all 8 go statements:
P0: runnext=[G7], runq=[G0, G1, G2, G3, G4, G5, G6]  (FIFO order)
P1: empty    P2: empty    P3: empty
```

None go to the global queue — the 256-slot local queue is nowhere near full.

**Step 3 — `wakep()` wakes idle Ps.** When the first runnable G is added while idle Ps exist, `newproc` calls `wakep()` → `startm()`, which pulls an idle P (say P1) from `sched.pidle`, finds or creates an M for it, and wakes that M. The new M starts at `schedule()` → `findRunnable()`.

**Step 4 — Work stealing distributes the goroutines.** P1's M has an empty local queue. `findRunnable()` → `runqsteal()`: picks P0 as victim, steals half of P0's runq from the head. Similarly P2 and P3 wake up and steal from whoever has the most:

```
After redistribution (approximate):
P0: runnext=[G7], runq=[G5, G6]  → ~3 goroutines
P1: runq=[G0, G1]                → ~2 goroutines
P2: runq=[G2, G3]                → ~2 goroutines
P3: runq=[G4]                    → ~1 goroutine
```

The exact split depends on timing. The global queue stays empty throughout — it is only used for overflow (>256 per P), async preemption, or goroutines readied from contexts with no current P.

**Step 5 — Execution.** Each P's M calls `execute()` on the next G from its local queue. When a goroutine finishes, `goexit()` → `schedule()` picks the next one. The same M+P pair persists across goroutines — no new thread is needed.

**Step 6 — `wg.Wait()`.** Main goroutine calls `gopark()` (via semaphore). When the last `wg.Done()` fires, `goready()` puts main into a P's `runnext`. Main resumes and the program exits.

---

## Resources

### Primary sources (talks)

| Talk | Speaker | Why watch it |
|------|---------|-------------|
| [Queues, Fairness, and The Go Scheduler](https://youtu.be/wQpC99Xu1U4) | Madhav Jivrajani (GopherCon 2021) | The talk this article is based on. Visual, incremental, covers the full scheduler design. |
| [The Scheduler Saga](https://www.youtube.com/watch?v=YHRO5WQGh0k) | Kavya Joshi (GopherCon 2018) | Covers 1:1, N:1, and N:M models. Explains *why* Go chose N:M with excellent diagrams. |
| [Pardon the Interruption: Loop Preemption in Go 1.14](https://youtu.be/1I1WmeSjRSw) | Austin Clements (GopherCon 2019) | Deep dive into async preemption. Explains why SIGURG was chosen, the design constraints, and the implementation. |
| [Go Scheduler: Implementing language with lightweight concurrency](https://www.youtube.com/watch?v=-K11rY57K7k) | Dmitry Vyukov | From the original designer of the current scheduler. Explains the design decisions and tradeoffs. |

### Design documents

| Document | Why read it |
|----------|-------------|
| [Scalable Go Scheduler Design Doc](https://docs.google.com/document/d/1TTj4T2JO42uD5ID9e89oa0sLKhJYD0Y_kqxDv3I3XMw) | Dmitry Vyukov's original proposal that introduced Ps and work stealing. The foundational document for Go's current scheduler. |
| [Non-cooperative goroutine preemption proposal](https://go.googlesource.com/proposal/+/master/design/24543-non-cooperative-preemption.md) | Austin Clements' design doc for async preemption in Go 1.14. Covers signal choice, safe points, and GC interaction. |
| [Go Execution Tracer Design Doc](https://docs.google.com/document/d/1FP5apqzBgr7ahCCgFO-yoVhk4YZrNIDNf9RybngBc14/pub) | Architecture behind Go's built-in execution tracer. Understand what data is collected and how it represents scheduler behavior. |

### Articles and blog posts

| Article | Author | Why read it |
|---------|--------|-------------|
| [Scheduling in Go (3-part series)](https://www.ardanlabs.com/blog/2018/08/scheduling-in-go-part1.html) | William Kennedy (Ardan Labs) | The most thorough written explanation of Go's scheduler. Part 1 covers OS scheduler basics, Part 2 covers Go's scheduler, Part 3 covers concurrency patterns. |
| [The Go Scheduler](https://morsmachine.dk/go-scheduler) | Daniel Morsing | Short, clear, classic explanation of the GMP model. Good first read. |
| [Go's work-stealing scheduler](https://rakyll.org/scheduler/) | Jaana Dogan (rakyll) | Focused explanation of work stealing in Go with good diagrams. |
| [Go Asynchronous Preemption: A Deep Look](https://medium.com/@workspace.behnam/go-asynchronous-preemption-a-deep-look-136a39122a4f) | Behnam | Detailed walkthrough of async preemption mechanics with code-level analysis. |
| [Scheduler Tracing In Go](https://www.ardanlabs.com/blog/2015/02/scheduler-tracing-in-go.html) | Ardan Labs | How to use `GODEBUG=schedtrace` and `scheddetail` to observe the scheduler in real time. |
| [GopherCon 2021 Speaker Notes](https://hackmd.io/@MadhavJivrajani/HJOudkJPu) | Madhav Jivrajani | The speaker's own notes for this talk, with additional context and links. |
| [Go Concurrency Patterns](https://go.dev/blog/pipelines) | Official Go Blog | Not scheduler-specific, but critical for understanding how concurrency patterns interact with the scheduler. |

### Source code

| File | What it contains |
|------|-----------------|
| [`src/runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go) | The main scheduler loop: `schedule()`, `findRunnable()`, `execute()`, `goexit()`, work stealing logic |
| [`src/runtime/runtime2.go`](https://github.com/golang/go/blob/master/src/runtime/runtime2.go) | Core struct definitions: `g`, `m`, `p`, `schedt` (global scheduler state) |
| [`src/runtime/preempt.go`](https://github.com/golang/go/blob/master/src/runtime/preempt.go) | Async preemption implementation |
| [`src/runtime/signal_unix.go`](https://github.com/golang/go/blob/master/src/runtime/signal_unix.go) | Signal handling, including SIGURG handler |

**Key commits:**

| Commit | What it introduced |
|--------|-------------------|
| [`e870f06`](https://github.com/golang/go/commit/e870f06c3f49ed63960a2575e330c2c75fc54a34) | `runnext` slot + time slice inheritance (the 99.88% benchmark improvement) |
| [`f9066fe`](https://github.com/golang/go/commit/f9066fe1c0a7181242f77d8534e0b6e112c982a9) | Async preemption via SIGURG (Go 1.14) |
| [`bc31bcc`](https://github.com/golang/go/commit/bc31bcccd3b94ec8dd324e523c4c7ae9180b937f) | Function-call preemption via stack growth check (Go 1.2) |

### Books

| Book | Why read it |
|------|-------------|
| *Concurrency in Go* — Katherine Cox-Buday (O'Reilly) | Practical patterns for Go concurrency. Good chapter on goroutine scheduling and the runtime. |
| *The Go Programming Language* — Donovan & Kernighan | Chapters 8-9 cover goroutines and channels. The Kernighan seal of quality. |

### Tools for hands-on exploration

| Tool | What it does |
|------|-------------|
| `GODEBUG=schedtrace=N` | Emit scheduler state to stderr every N milliseconds |
| `GODEBUG=schedtrace=N,scheddetail=1` | Detailed per-P, per-M, per-G state dumps |
| `go tool trace` | Visual timeline of goroutine scheduling in the browser |
| `runtime/trace` package | Programmatic tracing from within your code |
| [GSE (Go Scheduler Exporter)](https://github.com/MadhavJivrajani/gse) | Madhav's tool from the talk — exports scheduler traces to Prometheus for Grafana visualization |
| Linux ftrace (`/sys/kernel/debug/tracing/`) | Observe SIGURG signals at the kernel level |
| [`perf`](https://perf.wiki.kernel.org/) | Linux profiling tool — can observe context switches and signals |

### Related case studies

| Case Study | What it demonstrates |
|------------|---------------------|
| [Linux Namespaces and Go Don't Mix](https://www.weave.works/blog/linux-namespaces-and-go-don-t-mix) (Weave Works) | What happens when you change thread state (Linux namespaces) without locking the goroutine to its thread. Hard-to-reproduce bugs across goroutine rescheduling. |
| [Linux Namespaces and Go Started to Mix](https://www.weave.works/blog/linux-namespaces-golang-followup) (Weave Works follow-up) | The fix: `LockOSThread` and proper namespace handling. |

### Academic references

| Paper | Why read it |
|-------|-------------|
| [Scheduling Multithreaded Computations by Work Stealing](https://dl.acm.org/doi/10.1145/324133.324234) — Blumofe & Leiserson (JACM 1999) | The foundational paper on work-stealing algorithms. Proves expected time is O(T1/P + T_inf). Go's work stealing is a direct descendant of this design. |
| [Analysis of the Go Runtime Scheduler](http://www.cs.columbia.edu/~aho/cs6998/reports/12-12-11_DeshpandeSponslerWeiss_GO.pdf) — Columbia University | Academic analysis of Go's scheduler design with comparisons to other runtimes. |
| [Go Wiki: LockOSThread](https://go.dev/wiki/LockOSThread) | Official documentation on thread pinning — essential reading before using this API. |

---

## Key Takeaways

1. **Go's scheduler is distributed, not centralized.** Each P has its own local queue. The global queue exists but is accessed infrequently. This reduces lock contention.

2. **Fairness is a first-class design goal**, not an afterthought. The schedtick/61 mechanism, time slice inheritance, preempted-goroutines-go-to-global-queue — these are all fairness decisions.

3. **The scheduler uses domain-specific knowledge.** LIFO placement + time slice inheritance is not a textbook scheduling algorithm. It is optimized specifically for Go's channel-based concurrency patterns, and the benchmarks prove it works.

4. **Preemption evolved over three major Go releases** (1.0 → 1.2 → 1.14), from purely cooperative to fully asynchronous. Each step solved a real class of starvation bugs.

5. **Treat the runtime as a black box until you cannot.** The scheduler is remarkably good at its job. Reach for `runtime.GOMAXPROCS`, `runtime.Gosched`, or `runtime.LockOSThread` only when you have a measured problem that requires it.

---

## Suggested Learning Path

1. **Watch first**: Kavya Joshi's "The Scheduler Saga" for visual intuition of the GMP model
2. **Read the foundations**: Ardan Labs 3-part series by William Kennedy (OS scheduler → Go scheduler → concurrency)
3. **Watch the source talk**: Madhav Jivrajani's GopherCon 2021 talk (this article's source) for fairness, preemption, and live scheduler visualization
4. **Go deeper on preemption**: Austin Clements' talk on async preemption in Go 1.14
5. **Read the design doc**: Dmitry Vyukov's original scalable scheduler proposal
6. **Get hands-on**: Use `GODEBUG=schedtrace` and `go tool trace` on your own programs
7. **Read the source**: `runtime/proc.go` — specifically `schedule()`, `findRunnable()`, and the work-stealing loop
8. **Edge cases**: The Weave Works `LockOSThread` case study for real-world runtime pitfalls

---

## Companion Docs

- **[gmp-scheduler.md](gmp-scheduler.md)** — struct-level GMP details, run queue internals, work stealing mechanics, syscall handoff, parking/spinning/sleeping explained from scratch, futex/note, channel blocking (sudog), startup sequence, stack growth
- **[scheduler-fairness.md](scheduler-fairness.md)** — scheduling theory from the same GopherCon 2021 talk: N:M models, convoy effect, schedtick/61, FIFO vs LIFO, time slice inheritance, LockOSThread deep mechanics (stoplockedm/startlockedm), runtime APIs, observability tools
- **[go-sched-que.md](go-sched-que.md)** — Q&A format covering all the above: OS thread states, futex, semaphores, all G/M/P states, schedule()/execute()/goready() call chains, channels vs pipes, LockOSThread edge cases
- **[netpoller.md](netpoller.md)** — how Go's netpoller uses epoll internally, pollDesc data structures, goroutine parking/waking on I/O, deadline timer heap, comparison with our manual epoll server
- **[eintr-preemption.md](eintr-preemption.md)** — SIGURG preemption timeline, tgkill vs kill, why epoll_wait returns EINTR, gsignal stacks
