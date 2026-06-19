# Scheduler Fairness, Time Slice Inheritance, and Runtime Knobs

> Companion to [gmp-scheduler.md](gmp-scheduler.md). That doc covers the GMP model, struct-level details, work stealing, syscall handoff, preemption mechanics, and stacks. This doc covers what it doesn't: **why** the scheduler makes the fairness decisions it does, the scheduling theory behind them, and the runtime APIs you can use to interact with the scheduler.
>
> Based on [GopherCon 2021: Queues, Fairness, and The Go Scheduler — Madhav Jivrajani](https://youtu.be/wQpC99Xu1U4), extended with additional research.

## N:M scheduling — why Go does it this way

Three models for mapping user-space concurrency onto OS threads:

| Model | What it means | Who uses it | Tradeoff |
|---|---|---|---|
| **1:1** | Each user thread = one OS thread | Java (pre-Loom), C pthreads, Rust std::thread | Simple but expensive. ~1 MB stack per thread, kernel context switch ~1–10μs. Can't scale past a few thousand threads. |
| **N:1** | All user threads on one OS thread | Early Ruby, Python (GIL), Node.js event loop | Lightweight, but no multicore parallelism. One blocking syscall blocks everything. |
| **N:M** | N user threads multiplexed onto M OS threads | Go, Erlang BEAM, Rust Tokio, Java Loom (JDK 21+) | Multicore parallelism + lightweight goroutines + resilience to blocking syscalls. Most complex to implement. |

Go uses N:M. The critical advantage for us (building an epoll server): when a goroutine blocks in a syscall, the scheduler detaches its P and hands it to another M — goroutines in the local run queue keep running. See [gmp-scheduler.md — Blocking syscalls and P handoff](gmp-scheduler.md#blocking-syscalls-and-p-handoff) for the full mechanics.

Solaris tried N:M in the 90s with LWPs and abandoned it for 1:1 because the implementation was too complex. Go's P abstraction (Vyukov, 2012) solved the key problems Solaris hit: unbounded thread scanning for work stealing and wasted per-thread state during syscalls.

## The convoy effect — why fairness matters

Imagine a supermarket checkout (FIFO queue). One customer has 25 items. Every customer behind them waits. The cashier spends disproportionate time on that one customer while short-order customers — who'd take 30 seconds each — wait minutes.

This is the **convoy effect**: a resource-intensive task at the head of a FIFO queue delays all subsequent tasks, regardless of their own resource requirements.

In scheduling:
```go
go func() {
    for {
        // tight CPU loop, no function calls, no channel ops
        x = x*x + 1
    }
}()
```

Without preemption, this goroutine never yields its P. Every other goroutine on that P starves. This is exactly the problem that drove Go's preemption evolution from purely cooperative (Go 1.0) → function-call preemption (Go 1.2) → async preemption via SIGURG (Go 1.14). See [gmp-scheduler.md — Preemption](gmp-scheduler.md#preemption) and [eintr-preemption.md](eintr-preemption.md) for the mechanism.

The convoy effect also shows up in lock scheduling — if a thread holds a mutex and gets preempted mid-critical-section, all threads waiting on that lock form a convoy that resynchronizes on every future scheduling cycle.

## Where preempted goroutines go — and why

When sysmon preempts a goroutine for running >10ms, it goes to the **global run queue**, not back to its P's local queue.

Why not local? Consider what happens if it goes back to the local queue:

```
Local queue: [preempted-G] [G1] [G2] [G3]

→ G1 runs (short, finishes in 1ms)
→ G2 runs (short, finishes in 1ms)
→ G3 runs (short, finishes in 1ms)
→ preempted-G runs again (burns another full 10ms slice)
→ G4 has to wait 10ms+ before its turn
```

Short-lived goroutines repeatedly get stuck behind 10ms slices from the hog. By sending preempted goroutines to the global queue, they go to the back of the global line — other Ps' goroutines get priority.

## Why schedtick checks the global queue every 61 events (not 64)

[gmp-scheduler.md](gmp-scheduler.md#run-queues-local-vs-global) mentions that each P checks the global queue every 61 scheduling events. Here's **why 61** specifically:

The `schedtick` counter on each P increments every time the scheduler finds and executes a runnable goroutine. The check is:

```go
if _p_.schedtick%61 == 0 && sched.runqsize > 0 {
    // take from global queue instead of local
}
```

Three constraints:

1. **Not too large** — goroutines in the global queue starve
2. **Not too small** — constant lock contention on the global mutex, unfair to local goroutines
3. **Must be prime** — this is the critical one

Applications tend to call into the scheduler at frequencies aligned with powers of two — 16, 32, 64 iterations per scheduling event. If the check interval were also a power of two (say 64), it would **synchronize** with the application pattern:

```
Application frequency: 8  →  calls scheduler at ticks 8, 16, 24, 32, 40, 48, 56, 64, ...
Check interval: 64         →  fires at ticks 64, 128, 192, ...
Check interval: 61         →  fires at ticks 61, 122, 183, ...
```

Frequency 64 collides with frequency 8 at tick 64 (and every 64 ticks after). Frequency 61 rarely collides with any power-of-two frequency because 61 shares no factors with 2, 4, 8, 16, 32, or 64.

Same principle as choosing prime hash table sizes to minimize collision clustering. Madhav Jivrajani illustrates this in the talk with a sine wave visualization: frequency-8 and frequency-64 waves cross zero together far more often than frequency-8 and frequency-61.

## FIFO vs LIFO — and how Go uses both

When a goroutine spawns or unblocks another goroutine, where does the child go?

| Approach | Advantage | Problem |
|---|---|---|
| **FIFO** (put at tail) | Fair — first created, first run | Terrible locality. A channel receiver unblocked by a sender goes to the tail. If 200 goroutines are ahead, each burning up to 10ms, the receiver waits up to **2 seconds** to process a message that's already there. |
| **LIFO** (put at head) | Great locality — producer/consumer ping-pong with hot caches | Starvation. Two goroutines bouncing over a channel can monopolize a P forever. |

### Go's hybrid: `runnext` + time slice inheritance

Each P has a `runnext` slot — a single-goroutine priority lane separate from the 256-entry `runq` ring buffer. When a goroutine readies another goroutine (via `go func()` or channel send that unblocks a receiver):

1. The newly readied goroutine goes into `runnext`
2. The previous occupant of `runnext` (if any) gets pushed into `runq`
3. The scheduler checks `runnext` before `runq` — LIFO-like priority

**The starvation fix: time slice inheritance.** The goroutine in `runnext` inherits the **remaining** time slice of the goroutine that readied it, rather than getting a fresh 10ms.

If goroutine A spawns goroutine B at the 3ms mark:
- B gets `runnext` priority
- B inherits 7ms (10ms − 3ms)
- A and B together share one 10ms window
- After 10ms total, one gets preempted → other goroutines run

Without this, a sender-receiver pair could each get full 10ms slices, spending 20ms per round-trip while 200 other goroutines wait.

### The benchmark

From [commit `e870f06`](https://github.com/golang/go/commit/e870f06c3f49ed63960a2575e330c2c75fc54a34) that implemented `runnext` + time slice inheritance:

```
BenchmarkPingPongHog   1,607,649 ns/op → 1,963 ns/op   (99.88% improvement)
```

Improvements across the suite: GC ~6%, JSON processing 28–36%.

The `runnext` field in `runtime2.go` has this comment: *"runnext, if non-nil, is a runnable G that was ready'd by the current G and should be run next instead of what's in runq if there's time remaining in the running G's time slice. It will inherit the time left in the current time slice."*

This is domain-specific optimization — not a textbook scheduling algorithm. It's tuned for Go's channel-based producer-consumer patterns, and the benchmarks prove it works.

## Runtime APIs beyond GOMAXPROCS

[gmp-scheduler.md](gmp-scheduler.md#gomaxprocs--what-it-actually-controls) covers GOMAXPROCS in detail. Here are the other scheduler-related APIs:

### `runtime.Gosched()`

Voluntarily yields the processor. The calling goroutine goes to the **global run queue** (not local — it goes to the back of the global line).

If you're considering using this for performance, the improvement is almost always achievable through better algorithm design instead. Madhav's advice from the talk: "use it only if absolutely necessary."

### `runtime.Goexit()`

Terminates only the calling goroutine. Runs all deferred functions before exiting (unlike `os.Exit` which terminates the process immediately). If called from `main`, the runtime detects deadlock when all remaining goroutines finish with nothing to return to.

Used internally by `testing.Fatal()` — that's why deferred cleanup runs even after `t.Fatal()`.

### `runtime.LockOSThread()` / `runtime.UnlockOSThread()`

Wires the calling goroutine to its current OS thread. No other goroutine can run on this M, and no new threads can be spawned from it.

**When you need it**: when your goroutine changes the underlying thread's state. The critical example for us (given we're building an epoll server on Linux): **setting a network namespace**.

```go
runtime.LockOSThread()
defer runtime.UnlockOSThread()

// Set this thread's network namespace
netns, _ := os.Open("/var/run/netns/my-namespace")
unix.Setns(int(netns.Fd()), unix.CLONE_NEWNET)

// All subsequent syscalls on this goroutine use the new namespace
// Without LockOSThread, the scheduler could migrate this goroutine
// to a different M that's still in the default namespace
```

**Weave Works case study**: they published two articles about debugging exactly this — goroutines changing thread namespace state without `LockOSThread`, causing subtle networking bugs that were nearly impossible to reproduce because they depended on whether the scheduler happened to reschedule the goroutine onto a different thread:
- [Linux Namespaces and Go Don't Mix](https://www.weave.works/blog/linux-namespaces-and-go-don-t-mix)
- [Linux Namespaces and Go Started to Mix](https://www.weave.works/blog/linux-namespaces-golang-followup)

**Gotcha**: if a goroutine locked to a thread spawns a new goroutine, the child has **no guarantee** of running on the locked thread. Don't assume child goroutines inherit the thread lock.

`LockOSThread` acts as a taint: the runtime will not create child threads from this locked M for goroutine execution.

### `runtime.NumGoroutine()`

Returns the current number of goroutines. Useful for monitoring goroutine leaks — if this number grows monotonically, you're probably leaking goroutines somewhere.

## Observing the scheduler

### `GODEBUG=schedtrace`

```bash
GODEBUG=schedtrace=1000 ./myapp
```

Emits scheduler state every 1000ms to stderr:

```
SCHED 1004ms: gomaxprocs=4 idleprocs=0 threads=5 spinningthreads=0
  idlethreads=1 runqueue=12 [3 4 0 1]
```

Reading this: 4 Ps, none idle, 5 total threads (1 idle), 0 spinning, 12 goroutines in global queue, `[3 4 0 1]` goroutines in each P's local queue.

More detail:
```bash
GODEBUG=schedtrace=1000,scheddetail=1 ./myapp
```

Dumps per-P, per-M, and per-G state. Verbose but invaluable when debugging scheduling issues.

### `go tool trace`

Visual timeline of goroutine scheduling in the browser:

```go
import "runtime/trace"

f, _ := os.Create("trace.out")
trace.Start(f)
defer trace.Stop()
```

```bash
go tool trace trace.out
```

Shows exactly when each goroutine ran, on which P, when it was blocked, when it was stolen, and more. This is how you'd verify that our epoll server's goroutines are being scheduled as expected.

### Linux ftrace — observing SIGURG directly

Since preemption uses SIGURG (signal 23), you can observe it with Linux's tracing subsystem:

```bash
# Enable signal tracing (needs root)
echo 1 > /sys/kernel/debug/tracing/events/signal/signal_generate/enable
echo 'sig==23' > /sys/kernel/debug/tracing/events/signal/signal_generate/filter
echo 1 > /sys/kernel/debug/tracing/tracing_on

# Run your Go binary
./myapp &

# Watch the signals
cat /sys/kernel/debug/tracing/trace_pipe | grep myapp
```

You'll see SIGURG signals sent to specific thread IDs at ~10ms intervals — direct evidence of the scheduler preempting goroutines. Try this with our epoll server to see how often `epoll_wait` gets interrupted (producing the EINTR we handle in our event loop — see [eintr-preemption.md](eintr-preemption.md)).

### GSE — Madhav's Prometheus exporter

[GSE (Go Scheduler Exporter)](https://github.com/MadhavJivrajani/gse) is the tool Madhav built for the GopherCon talk. It takes `GODEBUG=schedtrace` output and exports it as Prometheus metrics so you can visualize local/global queue lengths in Grafana over time. In the talk he shows queue depths rising and falling in sync with preemption — seeing the 10ms sawtooth pattern in real-time.

## Resources

### Talks

| Talk | Speaker | What it adds |
|------|---------|-------------|
| [Queues, Fairness, and The Go Scheduler](https://youtu.be/wQpC99Xu1U4) | Madhav Jivrajani (GopherCon 2021) | This doc's source. Fairness, convoy effect, schedtick/61, time slice inheritance, live Grafana demo. |
| [Go Scheduler: Implementing language with lightweight concurrency](https://www.youtube.com/watch?v=-K11rY57K7k) | Dmitry Vyukov | From the original designer. Design philosophy and tradeoffs. |

(See [gmp-scheduler.md — Resources](gmp-scheduler.md#resources) for Kavya Joshi, Austin Clements, and the design docs.)

### Articles not in gmp-scheduler.md

| Article | What it adds |
|---------|-------------|
| [Go Asynchronous Preemption: A Deep Look](https://medium.com/@workspace.behnam/go-asynchronous-preemption-a-deep-look-136a39122a4f) | Code-level walkthrough of async preemption mechanics |
| [Scheduler Tracing In Go](https://www.ardanlabs.com/blog/2015/02/scheduler-tracing-in-go.html) | Practical guide to `GODEBUG=schedtrace` |
| [GopherCon 2021 Speaker Notes](https://hackmd.io/@MadhavJivrajani/HJOudkJPu) | Madhav's own notes with additional context |
| [Go Wiki: LockOSThread](https://go.dev/wiki/LockOSThread) | Official docs on thread pinning |

### Key commits

| Commit | What it introduced |
|--------|-------------------|
| [`e870f06`](https://github.com/golang/go/commit/e870f06c3f49ed63960a2575e330c2c75fc54a34) | `runnext` slot + time slice inheritance (99.88% benchmark improvement) |
| [`f9066fe`](https://github.com/golang/go/commit/f9066fe1c0a7181242f77d8534e0b6e112c982a9) | Async preemption via SIGURG (Go 1.14) |
| [`bc31bcc`](https://github.com/golang/go/commit/bc31bcccd3b94ec8dd324e523c4c7ae9180b937f) | Function-call preemption via stack growth check (Go 1.2) |

### Academic

| Paper | What it adds |
|-------|-------------|
| [Scheduling Multithreaded Computations by Work Stealing](https://dl.acm.org/doi/10.1145/324133.324234) — Blumofe & Leiserson (JACM 1999) | Foundational paper on work-stealing. Proves expected time O(T₁/P + T∞). Go's work stealing descends from this. |
