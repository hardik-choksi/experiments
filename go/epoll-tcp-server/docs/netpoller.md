# Go's Netpoller — Deep Dive

## What it is and what problem it solves

The netpoller is Go's **internal runtime component that multiplexes thousands of goroutines onto a small number of OS threads for network I/O**. It sits between your goroutine and the kernel's I/O multiplexer (epoll/kqueue/IOCP).

The problem: you want to write `n, err := conn.Read(buf)` — simple, blocking, synchronous-looking code. But if 10,000 goroutines all do this, you can't have 10,000 OS threads blocked in `read()`. The netpoller makes it so that when a goroutine calls `Read()` and there's no data, **the goroutine is parked** (suspended cheaply, ~4KB), not the OS thread. The OS thread goes and runs a different goroutine. When data arrives, the netpoller wakes the original goroutine and it resumes as if `Read()` just returned.

You get the **thread-per-connection programming model** with **event-loop efficiency**.

## Where it lives in the Go runtime

| File | What's in it |
|---|---|
| `runtime/netpoll.go` | Platform-independent core logic, `pollDesc`, goroutine parking/waking |
| `runtime/netpoll_epoll.go` | Linux implementation (wraps epoll) |
| `runtime/netpoll_kqueue.go` | macOS/BSD implementation (wraps kqueue) |
| `runtime/netpoll_windows.go` | Windows implementation (wraps IOCP) |
| `runtime/time.go` | Timer heap (used for deadlines) |
| `runtime/proc.go` | The sysmon thread that calls `netpoll()` |
| `net/fd_unix.go` | The `netFD` wrapper that user code interacts with |

## Key data structures

### `pollDesc` — one per connection

Every network FD gets a `pollDesc` (poll descriptor) that tracks:
```
pollDesc {
    fd      uintptr     // the OS file descriptor (socket)
    rg      uintptr     // goroutine waiting for READ (nil if nobody's waiting)
    wg      uintptr     // goroutine waiting for WRITE
    rd      int64       // read deadline (nanoseconds since epoch)
    wd      int64       // write deadline (nanoseconds since epoch)
}
```

This is the critical link: **each `pollDesc` knows which goroutine is sleeping on that FD**. When epoll says "fd 7 is readable," the netpoller looks up `pollDesc[7]`, finds the waiting goroutine, and wakes it.

### `pollCache` — global registry

A map from FD number → `pollDesc`. This is what the netpoller searches when epoll returns ready FDs:
```
FD 5  → pollDesc {rg: G42, rd: 1725000000000, ...}    // G42 waiting for read
FD 17 → pollDesc {rg: G99, rd: 0, ...}                // G99 waiting, no deadline
FD 42 → pollDesc {rg: nil, ...}                        // nobody waiting
```

### Timer heap — for deadlines

A min-heap (sorted by earliest deadline) shared across ALL goroutines:
```
Timer Heap:
  [0] deadline=1724999999  → wake G99 with ErrDeadlineExceeded
  [1] deadline=1725000000  → wake G42 with ErrDeadlineExceeded
  [2] deadline=1725000010  → wake G7  with ErrDeadlineExceeded
```

Before each `epoll_wait`, the netpoller computes `timeout = heap[0].deadline - now`. This is how `epoll_wait`'s timeout parameter becomes the deadline mechanism.

## Step-by-step: what happens when `conn.Read()` has no data

Let's trace a goroutine G5 calling `conn.Read(buf)` when no data is available:

```
t=0.000  G5 calls conn.Read(buf)
         ↓
t=0.000  net/fd_unix.go: tries non-blocking syscall.Read(fd, buf)
         ↓
t=0.000  kernel returns EAGAIN (no data on this non-blocking socket)
         ↓
t=0.001  netpoller: stores G5 in pollDesc.rg ("G5 is waiting for read on fd 7")
         netpoller: calls epoll_ctl(EPOLL_CTL_ADD, fd) if not already registered
         scheduler: parks G5 (removes from runnable queue)
         scheduler: M thread picks up a different goroutine G6 to run
         ↓
         ... G5 is sleeping, costs ~4KB, no OS thread tied up ...
         ↓
t=0.050  client sends data on the connection
         kernel: data arrives on fd 7, added to socket buffer
         kernel: fd 7 added to epoll's ready list
         ↓
t=0.050  sysmon thread: calls epoll_wait() → returns fd 7 as readable
         netpoller: looks up pollCache[7] → finds pollDesc with rg=G5
         netpoller: marks G5 as runnable, clears pollDesc.rg
         scheduler: adds G5 back to run queue
         ↓
t=0.051  some M thread picks up G5, resumes at the gopark() return point
         net/fd_unix.go: retries syscall.Read(fd, buf) → this time, data is there!
         returns (n, nil) to user code
         ↓
t=0.052  G5 continues with the data
```

Key insight: **the goroutine makes two `read()` syscalls.** First one returns EAGAIN (triggers parking), second one after wakeup actually gets the data. The user code sees one blocking `Read()` call — the parking/waking is invisible.

## The sysmon thread — who actually calls epoll_wait?

Regular M threads (the GOMAXPROCS worker threads) **never** call `epoll_wait()`. A special **sysmon (system monitor)** thread does it. Sysmon is a single OS thread that runs outside the normal G-M-P scheduling:

```
┌─────────────────────────────────────────┐
│              Go Runtime                 │
├─────────────────────────────────────────┤
│ M1 (OS thread) → P1 → [G0, G1, G2]    │  ← runs user goroutines
│ M2 (OS thread) → P2 → [G3, G4, G5]    │  ← runs user goroutines
│ M3 (OS thread) → P3 → [G6, G7]        │  ← runs user goroutines
│                                         │
│ M_sysmon (OS thread) ← NO P attached   │  ← runs netpoll + housekeeping
└─────────────────────────────────────────┘
```

Sysmon's loop runs roughly every 10-100ms and does:
1. **`netpoll()`** — calls `epoll_wait()`, finds ready FDs, wakes goroutines
2. **Preemption** — sends `SIGURG` to goroutines running too long (>10ms)
3. **Force GC** — triggers garbage collection if needed
4. **Deadline expiry** — checks timer heap, wakes goroutines past their deadline

### When is `netpoll()` called?

Not just by sysmon. Multiple places in the runtime call it to minimize I/O latency:

- **sysmon** — every ~10ms if it hasn't been called recently
- **`findRunnable()`** — at the end of each scheduling loop, calls `netpoll(0)` (non-blocking) to pick up ready goroutines before looking for other work
- **When a P goes idle** — calls `netpoll` before parking
- **Dedicated blocking call** — can block with a timeout matching the earliest deadline

## Integration with the G-M-P scheduler

The netpoller is deeply integrated with Go's scheduler. See [gmp-scheduler.md](gmp-scheduler.md) for the full deep dive. The key points for understanding the netpoller:

**G (Goroutine)** — a lightweight execution unit (~4KB stack, grows as needed). This is what `go func()` creates. Not an OS thread — just a struct with a stack and instruction pointer.

**M (Machine)** — an actual OS thread (~8MB stack, kernel-scheduled). M runs G's. You typically have GOMAXPROCS of these actively running.

**P (Processor)** — a scheduling context that holds a run queue of G's. Each M must acquire a P before running goroutines. P count = GOMAXPROCS (default = CPU cores).

```
M1 grabs P1, runs goroutines from P1's queue:  G0 → G1 → G2 → ...
M2 grabs P2, runs goroutines from P2's queue:  G3 → G4 → G5 → ...
```

When G5 parks on the netpoller:
1. G5 is removed from P2's queue
2. M2 doesn't block — it immediately picks up G6 from P2's queue
3. When G5 is woken by the netpoller, it's added back to some P's queue
4. Next time an M needs work, it picks up G5 and resumes it

**This is why parking is cheap**: no OS thread is blocked. The M thread that was running the parked goroutine just runs something else.

## Platform backends — how the abstraction works

The netpoller has a platform-independent layer (`runtime/netpoll.go`) and platform-specific backends:

**Linux — epoll** (`runtime/netpoll_epoll.go`)
- `epoll_create1()` → create instance
- `epoll_ctl()` → register/deregister FDs
- `epoll_wait()` → wait for ready FDs
- Level-triggered by default. O(ready FDs).

**macOS/BSD — kqueue** (`runtime/netpoll_kqueue.go`)
- `kqueue()` → create instance
- `kevent()` → register + wait (combined in one syscall, unlike epoll's split)
- **Edge-triggered by default** — only fires once per state change, must drain FD completely
- More powerful than epoll (can watch files, timers, signals, not just sockets)

**Windows — IOCP** (`runtime/netpoll_windows.go`)
- `CreateIoCompletionPort()` → create instance
- Fundamentally different: **proactive model**. You submit the I/O operation (read/write) and the kernel completes it, then posts a completion event. With epoll you ask "is data ready?" then read yourself. With IOCP you say "read this for me" and the kernel tells you when it's done.
- Go's runtime converts this to look like epoll internally

All backends implement the same internal interface, so `runtime/netpoll.go` doesn't care which OS you're on.

## The pollDesc lifecycle

**1. Registration (when you call `net.Listen()` or `net.Dial()`)**
```
net.Listen("tcp", ":8080")
  → creates socket FD
  → allocates pollDesc
  → stores in pollCache[fd]
  → calls epoll_ctl(EPOLL_CTL_ADD, fd) to register with epoll
```
The FD is now watched by epoll. Any goroutine that blocks on this FD will be parked.

**2. Waiting (goroutine calls `Read()` and gets EAGAIN)**
```
conn.Read(buf)
  → syscall.Read returns EAGAIN
  → stores current goroutine G in pollDesc.rg
  → parks G (gopark)
  → M thread picks up another G
```
FD is already in epoll (from registration), so the kernel is already watching it.

**3. Wakeup (data arrives)**
```
sysmon: epoll_wait() returns fd as readable
  → looks up pollCache[fd] → finds pollDesc
  → extracts G from pollDesc.rg, clears it
  → marks G as runnable, adds to a P's run queue
```

**4. Cleanup (when you call `conn.Close()`)**
```
conn.Close()
  → epoll_ctl(EPOLL_CTL_DEL, fd)   // deregister from epoll FIRST
  → delete pollCache[fd]            // remove from registry
  → syscall.Close(fd)              // close FD last
```
Order matters: deregister before closing prevents stale FD entries in epoll. If the OS reuses the FD number for a new socket, you don't want epoll firing events for the old `pollDesc`.

## How deadline expiry actually works (the full picture)

When you call `conn.SetReadDeadline(time.Now().Add(5 * time.Second))`:

1. Computes absolute deadline: `now + 5s` in nanoseconds
2. Inserts entry in the global timer heap: `{when: deadline, goroutine: G, pollDesc: pd}`
3. If this deadline is earlier than the current heap root, it becomes the new root

On the next sysmon cycle:
1. Computes `timeout = heap[0].when - now` (earliest deadline minus current time)
2. Calls `epoll_wait(epfd, events, maxEvents, timeout_ms)`
3. If `epoll_wait` returns because timeout expired (0 events, no EINTR):
   - Walk the heap, pop all entries where `when <= now`
   - For each expired entry: wake the goroutine with `os.ErrDeadlineExceeded`
4. If `epoll_wait` returns because I/O is ready:
   - Wake those goroutines normally (data available)
   - Also check the heap for any deadlines that expired while processing

The goroutine wakes up and `waitRead()` checks why it was woken:
```go
// pseudo-code inside the runtime
if deadline_expired {
    return os.ErrDeadlineExceeded  // → user sees "i/o timeout"
}
return nil  // → user retries Read(), gets data
```

## How our epoll server compares to the netpoller

| Aspect | Our server | Go's netpoller |
|---|---|---|
| Event loop | Our code, single-threaded, we wrote it | sysmon thread, integrated into runtime |
| When goroutine blocks on I/O | N/A — we don't use goroutines per connection | Goroutine is parked (~4KB), OS thread freed |
| FD tracking | `Connections` map[int]*Connection | `pollCache` map[fd]pollDesc with goroutine refs |
| Deadlines | No-op stubs | Timer heap, `epoll_wait` timeout, automatic expiry |
| Fairness | If one handler is slow, all clients wait | Scheduler preempts after ~10ms via SIGURG |
| Memory per connection | ~50 bytes (Connection struct) | ~4KB (goroutine stack) + ~200 bytes (pollDesc) |
| Partial writes | Single write, no retry | Retry loop until all bytes sent |
| Close cleanup | Manual: Close + EpollCtl DEL + delete from map | Automatic: deregister → remove → close, in correct order |
| Programming model | Explicit event loop (you manage state) | Synchronous-looking code (runtime manages state) |

## Why this matters

Building the epoll loop manually teaches you what the netpoller does invisibly. In production Go, you'd write:

```go
listener, _ := net.Listen("tcp", ":8080")
for {
    conn, _ := listener.Accept()
    go handleClient(conn)  // goroutine per connection — the netpoller makes this efficient
}
```

This looks like thread-per-connection but performs like an event loop, because under the hood the netpoller is doing exactly what we're doing: `epoll_create1`, `epoll_ctl`, `epoll_wait`, non-blocking FDs. The difference is it also manages goroutine parking/waking, deadlines via a timer heap, platform abstraction (epoll/kqueue/IOCP), and integration with the scheduler's G-M-P model — about 20,000+ lines of runtime code that you get for free.

## Resources

### Design documents
- [Non-cooperative goroutine preemption — Austin Clements](https://go.googlesource.com/proposal/+/master/design/24543-non-cooperative-preemption.md) — covers SIGURG, relevant to netpoller wakeup

### Go runtime source files
1. [`runtime/netpoll.go`](https://go.dev/src/runtime/netpoll.go) — platform-agnostic core: `pollDesc`, `netpollblock()`, `netpollunblock()`
2. `runtime/netpoll_epoll.go` — Linux epoll backend (the one we're reimplementing)
3. `runtime/netpoll_kqueue.go` — macOS/BSD backend
4. `runtime/netpoll_windows.go` — Windows IOCP backend
5. `net/fd_unix.go` — the `netFD` wrapper connecting `net.Conn` to the netpoller
6. [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go) — `sysmon()` and `findRunnable()` which call `netpoll()`

### Blog posts
- [Daniel Morsing — "The Go Netpoller" (2013)](https://morsmachine.dk/netpoller) — best first read on the netpoller specifically
- [Daniel Morsing — "The Go Scheduler" (2013)](https://morsmachine.dk/go-scheduler) — companion post, needed for context
- [SoByte — "Explaining the Golang I/O Multiplexing Netpoller Model"](https://www.sobyte.net/post/2022-01/go-netpoller/) — detailed modern walkthrough
- [DataDog — go-profiler-notes: Goroutine Scheduler](https://datadoghq.dev/go-profiler-notes/mental-model-for-go/goroutine-scheduler.html) — covers netpoller integration with scheduler
- [Ardan Labs — "Scheduling In Go" Part II](https://www.ardanlabs.com/blog/2018/08/scheduling-in-go-part2.html) — covers how network I/O interacts with the scheduler

### Conference talks
- [GopherCon 2018 — Kavya Joshi — "The Scheduler Saga"](https://www.youtube.com/watch?v=YHRO5WQGh0k) — covers netpoller as part of the scheduler deep dive
