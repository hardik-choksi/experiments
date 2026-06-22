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

## How the netpoller knows which goroutine to wake

When `epoll_wait` returns "fd 7 is readable," how does the runtime find the specific goroutine waiting on that FD? The answer is a pointer chain set up at registration time — no searching, no hash lookup, O(1) per ready FD.

### The epoll user-data trick

`epoll_ctl` lets you attach **arbitrary user data** to every FD you register. The C struct has a union for this:

```c
// from <sys/epoll.h>
typedef union epoll_data {
    void    *ptr;       // arbitrary pointer (8 bytes on 64-bit)
    int      fd;        // or just store the FD number
    uint32_t u32;
    uint64_t u64;
} epoll_data_t;

struct epoll_event {
    uint32_t     events;   // EPOLLIN, EPOLLOUT, etc.
    epoll_data_t data;     // 8 bytes — kernel stores it, hands it back untouched
};
```

When `epoll_wait` fires, it returns the same `data` you stored. Most C programs stuff the FD number in there. Go's runtime does something smarter — it stores a **pointer to the `pollDesc`**.

### Two different Go structs for the same C struct

Go has **two different** representations of `struct epoll_event`, and the difference matters:

**`syscall.EpollEvent`** — what userspace code (including our server) uses:
```go
// syscall/ztypes_linux_amd64.go
type EpollEvent struct {
    Events uint32
    Fd     int32
    Pad    int32
}
```
The 8-byte `data` union is hardcoded as `Fd int32 + Pad int32`. Simple and convenient — you just set `ev.Fd = serverFD` — but you **cannot store a pointer**. The `Fd` field is only 4 bytes, and `Pad` is inaccessible filler.

**`runtime.epollevent`** — what the netpoller uses internally:
```go
// runtime/defs_linux_amd64.go
type epollevent struct {
    events uint32
    data   [8]byte   // raw 8 bytes — large enough for a 64-bit pointer
}
```
The runtime defines its own struct with `data [8]byte` instead of `Fd + Pad`. This gives it access to the full 8-byte union, so it can stuff a `pollDesc` pointer in there via `unsafe.Pointer`:

```go
// runtime/netpoll_epoll.go — netpollopen()
// This is what the runtime actually does (simplified)
var ev epollevent
ev.events = _EPOLLIN | _EPOLLOUT | _EPOLLRDHUP | _EPOLLET
*(**pollDesc)(unsafe.Pointer(&ev.data)) = pd  // stash pollDesc pointer in the 8-byte data field
epollctl(epfd, _EPOLL_CTL_ADD, int32(fd), &ev)
```

Both structs compile down to the same 12 bytes in memory — the kernel doesn't care. The difference is purely how Go exposes those bytes:

| Struct | Where defined | data bytes interpreted as | Can store a pointer? | Used by |
|---|---|---|---|---|
| `syscall.EpollEvent` | `syscall/ztypes_linux_amd64.go` | `Fd int32` + `Pad int32` | No — only 4 bytes for FD | Our server, any userspace Go code |
| `runtime.epollevent` | `runtime/defs_linux_amd64.go` | `data [8]byte` | Yes — via `unsafe.Pointer` | Go's netpoller internally |

**Why two structs?** The `syscall` package is designed for regular Go programs — `Fd int32` covers 99% of use cases and is type-safe. The runtime needs raw byte access to store pointers, so it defines its own version. The runtime can't use `syscall.EpollEvent` because it can't fit a pointer into a 4-byte `int32` field on a 64-bit system.

**Why this matters for our server:** We use `syscall.EpollEvent` and store the FD in `ev.Fd`, then look up the connection in our `Connections` map. The netpoller skips the map lookup entirely — it gets the `pollDesc` pointer directly from `ev.data`, which is O(1). We can't do the same trick with `syscall.EpollEvent` without redefining the struct ourselves.

Source: [`runtime/netpoll_epoll.go` — `netpollopen()`](https://github.com/golang/go/blob/master/src/runtime/netpoll_epoll.go), [`runtime/defs_linux_amd64.go`](https://github.com/golang/go/blob/master/src/runtime/defs_linux_amd64.go), [`syscall/ztypes_linux_amd64.go`](https://github.com/golang/go/blob/master/src/syscall/ztypes_linux_amd64.go)

### The full wakeup chain

**Step 1 — Registration** (happens once, when `net.Listen()` or `Dial()` creates a connection):
```
net.Listen("tcp", ":8080")
  → socket() returns fd=7
  → runtime allocates pollDesc for fd 7
  → epoll_ctl(ADD, fd=7, data=&pollDesc)    ← pollDesc pointer stored in epoll
```

**Step 2 — Parking** (goroutine G42 calls `conn.Read()`, gets EAGAIN):
```
G42: conn.Read(buf)
  → syscall.Read(fd=7) returns EAGAIN
  → runtime stores G42's pointer in pollDesc.rg:
      pollDesc {
          fd:  7
          rg:  → G42    // "G42 is blocked on read for fd 7"
          wg:  nil      // nobody blocked on write
      }
  → gopark(G42)         // remove from run queue, M picks up next G
```

Source: [`internal/poll/fd_unix.go` — `FD.Read()`](https://github.com/golang/go/blob/master/src/internal/poll/fd_unix.go) does the EAGAIN retry, then calls into [`runtime/netpoll.go` — `poll_runtime_pollWait()`](https://github.com/golang/go/blob/master/src/runtime/netpoll.go) which calls `netpollblock()` to park.

**Step 3 — Data arrives** (client sends bytes, kernel buffers them on fd 7):
```
epoll_wait() returns:
  event { events: EPOLLIN, data: *pollDesc_0xc0000a4000 }
                                  │
         kernel hands back the    │
         exact pointer we stored  │
                                  ▼
                          pollDesc_0xc0000a4000 {
                              rg: → G42     ← the goroutine to wake
                              wg: nil
                          }
```

Source: [`runtime/netpoll_epoll.go` — `netpoll()`](https://github.com/golang/go/blob/master/src/runtime/netpoll_epoll.go) calls `epoll_wait`, then for each event extracts the `pollDesc` pointer from `ev.data` (the runtime's `[8]byte` field, not `syscall.EpollEvent.Fd`).

**Step 4 — Wakeup**:
```
runtime: extract G42 from pollDesc.rg
runtime: set pollDesc.rg = 0 (nobody waiting anymore)
runtime: mark G42 as runnable (Grunnable status)
runtime: inject G42 into a P's local run queue
  → next scheduling cycle, some M picks up G42
  → G42 resumes at the point after gopark()
  → retries read(fd=7) → succeeds, returns data to user
```

Source: [`runtime/netpoll.go` — `netpollunblock()`](https://github.com/golang/go/blob/master/src/runtime/netpoll.go) extracts and clears the goroutine, [`netpollready()`](https://github.com/golang/go/blob/master/src/runtime/netpoll.go) adds it to the return list for the scheduler.

### Why `rg` and `wg` are separate

`pollDesc` has two goroutine slots because read and write are independent TCP operations. One goroutine can be blocked reading while another is blocked writing on the **same FD**:

```
pollDesc {
    rg: → G42    // waiting for EPOLLIN  (data to read)
    wg: → G88    // waiting for EPOLLOUT (write buffer space)
}
```

When `epoll_wait` returns with `EPOLLIN`, only G42 is woken. G88 stays parked until `EPOLLOUT` fires. The runtime checks `ev.Events` to decide which slot(s) to drain:

```go
// runtime/netpoll_epoll.go — netpoll(), simplified
var mode int32
if ev.Events&(syscall.EPOLLIN|syscall.EPOLLRDHUP|syscall.EPOLLHUP|syscall.EPOLLERR) != 0 {
    mode += 'r'  // wake the reader
}
if ev.Events&(syscall.EPOLLOUT|syscall.EPOLLHUP|syscall.EPOLLERR) != 0 {
    mode += 'w'  // wake the writer
}
netpollready(&toRun, pd, mode)
```

Note: `EPOLLHUP` and `EPOLLERR` wake **both** — if the connection is broken, both reader and writer need to know.

### Contrast with our server

In our server, we use the same epoll mechanism but without the goroutine plumbing:

| Aspect | Our server | Go's netpoller |
|---|---|---|
| What's stored in `epoll_event.Data` | Nothing (we look up FD in our `Connections` map) | Pointer to `pollDesc` (O(1) to goroutine) |
| What we find when FD is ready | `*Connection` struct | `*pollDesc` → parked goroutine |
| How we "wake" | Directly handle in event loop | Put goroutine back on scheduler run queue |
| Concurrent read+write | Not supported (single-threaded loop) | Two goroutines via `rg`/`wg` slots |

### Why this is O(1) — no searching

The critical design insight: **the pointer chain is set up at registration, not at lookup time.** When data arrives:
1. `epoll_wait` hands back the `pollDesc` pointer — no map lookup, the kernel stored it for us
2. `pollDesc.rg` is a direct goroutine pointer — no searching through run queues
3. Adding a goroutine to a run queue is O(1) — it's a linked list append

Compare this to a naive approach where you'd need: `fd → hash map lookup → connection → find goroutine → search scheduler queues`. The netpoller avoids all of that.

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

### Go runtime source files (goroutine wakeup path, in call order)
1. [`internal/poll/fd_unix.go`](https://github.com/golang/go/blob/master/src/internal/poll/fd_unix.go) — `FD.Read()` / `FD.Write()`: EAGAIN retry loop, calls `pollDesc.waitRead()`/`waitWrite()` to park
2. [`internal/poll/fd_poll_runtime.go`](https://github.com/golang/go/blob/master/src/internal/poll/fd_poll_runtime.go) — bridge to runtime: `pollDesc.init()` registers FD, `waitRead()`/`waitWrite()` call `poll_runtime_pollWait()`
3. [`runtime/netpoll.go`](https://github.com/golang/go/blob/master/src/runtime/netpoll.go) — platform-agnostic core: `pollDesc` struct, `netpollblock()` (parks goroutine into `rg`/`wg`), `netpollunblock()` (extracts goroutine), `netpollready()` (adds to runnable list)
4. [`runtime/netpoll_epoll.go`](https://github.com/golang/go/blob/master/src/runtime/netpoll_epoll.go) — Linux epoll backend: `netpollopen()` (stores `pollDesc` pointer in `epoll_event.Data`), `netpoll()` (calls `epoll_wait`, extracts `pollDesc` from returned events, checks `EPOLLIN`/`EPOLLOUT` to decide `rg` vs `wg`)
5. `runtime/netpoll_kqueue.go` — macOS/BSD backend (same interface, different syscalls)
6. `runtime/netpoll_windows.go` — Windows IOCP backend
7. [`net/fd_unix.go`](https://github.com/golang/go/blob/master/src/net/fd_unix.go) — `netFD` wrapper connecting `net.Conn` methods to `internal/poll.FD`
8. [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go) — `sysmon()` calls `netpoll()` periodically, `findRunnable()` calls `netpoll(0)` non-blocking before parking

### Linux man pages (epoll internals)
- [`man 7 epoll`](https://man7.org/linux/man-pages/man7/epoll.7.html) — epoll overview, documents `epoll_data_t` union (the user-data field the netpoller uses to store `pollDesc` pointers)
- [`man 2 epoll_ctl`](https://man7.org/linux/man-pages/man2/epoll_ctl.2.html) — documents `struct epoll_event` and the `data` field: "data that the kernel should save and then return (via `epoll_wait`) when this file descriptor becomes ready"
- [`man 2 epoll_wait`](https://man7.org/linux/man-pages/man2/epoll_wait.2.html) — documents that returned events contain the same `data` stored via `epoll_ctl`

### Linux kernel source
- [`fs/eventpoll.c`](https://github.com/torvalds/linux/blob/master/fs/eventpoll.c) — epoll implementation: the `ep_item` struct stores user data, `ep_send_events()` copies it back to userspace on `epoll_wait`

### Blog posts
- [Daniel Morsing — "The Go Netpoller" (2013)](https://morsmachine.dk/netpoller) — best first read on the netpoller specifically
- [Daniel Morsing — "The Go Scheduler" (2013)](https://morsmachine.dk/go-scheduler) — companion post, needed for context
- [SoByte — "Explaining the Golang I/O Multiplexing Netpoller Model"](https://www.sobyte.net/post/2022-01/go-netpoller/) — detailed modern walkthrough, traces the full `Read()` → `netpollblock()` → `epoll_wait` → wakeup path
- [DataDog — go-profiler-notes: Goroutine Scheduler](https://datadoghq.dev/go-profiler-notes/mental-model-for-go/goroutine-scheduler.html) — covers netpoller integration with scheduler
- [Ardan Labs — "Scheduling In Go" Part II](https://www.ardanlabs.com/blog/2018/08/scheduling-in-go-part2.html) — covers how network I/O interacts with the scheduler
- [Stoney Jackson — "Go's Netpoller and goroutine parking"](https://stonyjack.com/posts/go-netpoller/) — walks through `pollDesc.rg`/`wg` mechanics and the EAGAIN→park→wake cycle

### Conference talks
- [GopherCon 2018 — Kavya Joshi — "The Scheduler Saga"](https://www.youtube.com/watch?v=YHRO5WQGh0k) — covers netpoller as part of the scheduler deep dive
