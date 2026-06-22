# Event Loop TCP Server — Learnings

## How epoll works

1. Create an epoll instance (`epoll_create1`)
2. Register FDs you care about (`epoll_ctl` with `EPOLL_CTL_ADD`)
3. Block on `epoll_wait` — kernel wakes you when any registered FD has activity
4. Loop through ready FDs: if it's the server FD, accept a new connection; if it's a client FD, read/write data
5. Go back to step 3

Epoll maintains an internal red-black tree of watched FDs and a ready list. When a socket receives data, the kernel adds it to the ready list via a callback — so `epoll_wait` is O(ready FDs), not O(total FDs). This is what makes it scale to thousands of connections where `select`/`poll` can't.

## Pitfalls encountered

### `net.ParseIP` returns 16 bytes, not 4
`net.ParseIP("127.0.0.1")` returns a 16-byte IPv4-in-IPv6 representation. Bytes 0–3 are zeros, not the IPv4 address. Must call `.To4()` to get the 4-byte form, or the server silently binds to `0.0.0.0`.

### `O_NONBLOCK` vs `SOCK_NONBLOCK`
Both happen to be `0x800` on Linux, but `SOCK_NONBLOCK` is the correct flag for `socket()`. `O_NONBLOCK` is for `open()`/`fcntl()`. Non-portable to rely on them sharing a value.

### Accepted client FDs are blocking by default
The non-blocking flag on the listening socket only makes `accept()` itself non-blocking. The returned client FD is a **new** FD in blocking mode. Must either:
- Use `accept4()` with `SOCK_NONBLOCK` (Linux-specific)
- Call `SetNonblock(clientFd, true)` after `accept()`

If client FDs are blocking, any `read()`/`write()` on them stalls the entire event loop.

### Level-triggered vs edge-triggered epoll

Epoll has two notification modes. We use level-triggered (the default).

**Level-triggered (LT)** — "tell me whenever the FD **is** ready." If data is sitting in the buffer and you don't read it, `epoll_wait` fires again. And again. It keeps telling you as long as the condition holds — like an alarm that rings until you acknowledge it.

**Edge-triggered (ET)** — "tell me when the FD **becomes** ready." Fires once when data arrives. If you don't read all of it, you won't get another notification until *new* data arrives. Only fires on the **transition** from not-ready to ready — like a doorbell, one ring per visitor. Enabled by adding `EPOLLET` flag: `Events: syscall.EPOLLIN | syscall.EPOLLET`.

| | Level-triggered (default) | Edge-triggered (`EPOLLET`) |
|---|---|---|
| Notification | Fires repeatedly while data exists | Fires once per state change |
| Read strategy | Read some, get notified again for the rest | Must drain entire buffer in a loop until EAGAIN |
| Risk if you miss data | None — epoll re-fires | Data sits unread forever, connection stalls |
| Simpler to use | Yes | No — must handle partial reads carefully |
| Performance | More `epoll_wait` returns (slight overhead) | Fewer wakeups, but more complex code |
| Used by | Redis, libevent (default), libev, Node.js (libuv), HAProxy, most epoll tutorials | nginx, Go's netpoller (epoll ET on Linux, kqueue ET on macOS), Tokio (Rust), Java NIO (via epoll ET on Linux) |

**Why this matters for our server:** We read once per notification (`buf := make([]byte, 1024)`). If a client sends 4KB, we read 1KB, and level-triggered epoll fires again for the remaining 3KB. With edge-triggered, we'd need a drain loop:
```go
for {
    n, err := conn.Read(buf)
    if err == syscall.EAGAIN {
        break // buffer drained, wait for next notification
    }
    // process buf[:n]
}
```
Without that drain loop in ET mode, the remaining 3KB would sit unread forever — epoll wouldn't notify again because no *new* data arrived. The connection would appear to stall.

**Why does edge-triggered exist if it's harder?** Performance. In level-triggered mode, if you have 10,000 connections and 5,000 have unread data, every `epoll_wait` returns all 5,000 — even if you can only process 100 per loop iteration. Edge-triggered only returns FDs where *new* data arrived since the last check, which is usually a much smaller set. nginx uses ET for this reason.

### `SO_REUSEADDR` prevents "address already in use"
Without it, restarting the server within ~60s fails because the OS holds the socket in TIME_WAIT. Set before `bind()`:
```go
syscall.SetsockoptInt(serverFD, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
```

## Handling long-running work in a single-threaded event loop

The event loop is a single cashier — if one customer takes long, everyone waits. Three approaches:

### 1. Thread pool (epoll + worker threads)
- Epoll loop stays single-threaded and fast — only does I/O multiplexing
- When a client FD is ready, hand it to a worker thread via a queue
- Worker does the slow work, signals main loop to close/re-arm the FD
- Win is **resource control**: 10k connections don't need 10k threads, only threads for FDs with actual work
- Used by: **memcached** (libevent + worker threads), **Redis 6+** (I/O threads for read/write, main thread for commands), **PostgreSQL** (postmaster process accepts, forks worker per connection — not epoll-based but same dispatcher pattern), **Java Netty** (boss group accepts, worker group handles I/O), **gRPC C-core** (epoll poller + thread pool for RPC handlers)

### 2. State machine (pure non-blocking)
- Each connection has a state struct tracking its progress (reading headers → processing → writing response)
- Handler advances the state by one small step, then returns to the event loop
- Every stage must be short — if inherently slow, break into chunks or hybrid with a thread pool
- Most performant but hardest to write; you're hand-coding what async/await does in higher-level languages
- Used by: **nginx** (every stage is I/O-bound and fast, full state machine per connection), **HAProxy** (connection state machine with multi-step request/response processing), **Envoy** (filter chain where each filter advances the connection state), **Node.js** (libuv event loop — JS callbacks are the state transitions, `async`/`await` is syntactic sugar over the state machine), **Rust Tokio** (`.await` compiles down to a state machine that yields back to the executor)

### 3. io_uring (Linux 5.1+)
- Instead of "notify me when ready, then I'll syscall," you submit the syscall itself and the kernel completes it async
- Eliminates the two-step (poll → syscall) overhead of epoll
- Steepest learning curve, most modern approach
- Used by: **Tigerbeetle** (financial transactions database, built entirely on io_uring), **Tokio** (experimental io_uring backend via `tokio-uring`), **DPDK/SPDK** (high-performance networking/storage, uses io_uring for async disk I/O), **liburing** (the standard C wrapper everyone uses to talk to io_uring)

### 4. Goroutine-per-connection (Go's approach)
- Not really a fourth pattern — it's the thread-per-connection model made cheap by Go's runtime
- Each connection gets its own goroutine (~2-4KB stack). Goroutines block on `Read()`/`Write()` but the runtime parks them and multiplexes onto a small number of OS threads via the netpoller (epoll under the hood)
- You write blocking-style code, the runtime turns it into an event loop internally
- Used by: **Go's net/http**, **every Go server** — the standard `go handleConn(conn)` pattern. Also similar to **Erlang/Elixir** (lightweight processes on BEAM VM) and **Java 21 virtual threads** (Project Loom — same idea, goroutines for JVM)

### Thread pool vs thread-per-connection
Not about per-request speed. Thread-per-connection with 10k clients = 10k threads × ~8MB stack = 80GB+ memory pressure plus kernel scheduling overhead. Thread pool with 64 workers handles the same 10k connections because **most connections are idle at any moment** — epoll tells you which ones actually have data. The pool only works on active FDs.

## EINTR — interrupted system call

`EINTR` means a signal was delivered to the process while it was blocked in a syscall (like `epoll_wait`). The kernel aborts the syscall and returns `EINTR` to give the process a chance to handle the signal before retrying.

In Go, the runtime sends `SIGURG` frequently for goroutine preemption. This means `epoll_wait` gets interrupted regularly — it's not an error, just noise. The standard fix is to silently retry:

```go
n, err := syscall.EpollWait(epollerFd, events, -1)
if err != nil {
    if err == syscall.EINTR {
        continue // signal interrupted us, just retry
    }
    // real error
}
```

This pattern applies to any blocking syscall (`read`, `write`, `accept`, `epoll_wait`, etc.) — `EINTR` is always safe to retry. In C you'll see the same `while (ret == -1 && errno == EINTR)` loop everywhere.

**Important:** Retrying on EINTR does NOT prevent goroutine preemption — preemption already happened before we see the EINTR. The `continue` just cleans up the interrupted syscall. Also, "sending SIGURG to a goroutine" is imprecise — the runtime uses `tgkill` to target the specific OS thread (M) running that goroutine, not the goroutine itself. Signals are still process/thread-level, not goroutine-level.

**Deep dive:** [docs/eintr-preemption.md](docs/eintr-preemption.md) — full preemption timeline, tgkill vs kill, why SIGURG, gsignal stacks

## EAGAIN and detecting client disconnect

### EAGAIN (errno 11)
`EAGAIN` means "this operation would block, but you asked for non-blocking, so I'm returning immediately — try again later." On non-blocking sockets, `read()` returns `EAGAIN` when there's no data available right now.

Even with epoll, EAGAIN can happen:
- Spurious wakeup — kernel is allowed to wake you even if the condition resolved between notification and syscall
- Multiple threads polling the same epoll — one reads the data before the other (not applicable for single-threaded loops, but good to know)
- Kernel consumed the data — e.g., TCP RST arrived between epoll returning and your `read()`

`EAGAIN` and `EWOULDBLOCK` are the same value (11) on Linux. POSIX allows them to differ but no real system does.

When `EAGAIN` occurs: don't close the connection. Just return to the event loop — epoll will notify again when data is actually available.

### How `read()` signals different disconnect scenarios

| Scenario | read() returns |
|---|---|
| Client calls `close()` | `n == 0, err == nil` (FIN received) |
| Client calls `shutdown(SHUT_WR)` | `n == 0, err == nil` (FIN, half-close — can still write back) |
| Client crashes / killed | `err == ECONNRESET` (RST packet on next read) |
| Network drops | `err == ETIMEDOUT` after TCP retransmit timeout (minutes) |

`n == 0` with no error is the **only** clean "done sending" signal. It always means a FIN was received. All messy disconnects come through as errors.

### The read condition in the event loop
```go
// n==0: client sent FIN. err!=EAGAIN: real error. EAGAIN: no data, skip.
if n == 0 || (err != nil && err != syscall.EAGAIN) {
    conn.Close()           // close + deregister
}
if err == syscall.EAGAIN {
    continue               // not a disconnect, just no data
}
```

## Observing threads: what `ps -T` shows on our server

Running `ps -T -p <pid>` on our Go epoll server shows the per-thread view. On a 16-core machine with GOMAXPROCS=16, you might see only ~6 threads. Here's what the output means:

```
PID    SPID   TTY      STAT   TIME  COMMAND
12345  12345  pts/0    Sl+    0:00  ./epoll-tcp-server
12345  12346  pts/0    Sl+    0:00  ./epoll-tcp-server
12345  12347  pts/0    Sl+    0:00  ./epoll-tcp-server
...
```

**SPID** = System Process ID. On Linux, every thread has its own kernel `task_struct` with a unique TID (thread ID). `SPID` is that TID. The runtime uses this with `tgkill(pid, spid, SIGURG)` to send preemption signals to specific threads.

**STAT column decoded** — each character means something:

| Char | Meaning |
|---|---|
| `S` | **Sleeping** — `TASK_INTERRUPTIBLE`. Thread is waiting on something (futex, epoll_wait, syscall). Zero CPU. Can be woken by a signal. |
| `l` | **Multi-threaded** — process has multiple threads (all Go programs do). |
| `+` | **Foreground process group** — running in the foreground of its terminal. |

So `Sl+` = sleeping multi-threaded foreground process. Nearly all threads in a Go program show `S` because they're either:
- **Parked Ms** — waiting in `notesleep()` → `futex(FUTEX_WAIT)` for work
- **Sysmon** — sleeping between iterations (adaptive 20μs–10ms delay)
- **Blocked in epoll_wait** — our main goroutine's M, waiting for network events

**Why only ~6 threads when GOMAXPROCS=16?** The runtime creates threads **lazily**. At startup it only creates M0 + sysmon (2 threads). Additional threads are created on demand by `startm()` → `newm()` as goroutines need them. A lightly loaded server with few concurrent goroutines never needs all 16 Ps to have active Ms. The unused Ps sit in `sched.pidle` waiting.

### OS thread states (what the kernel tracks)

Every thread in Linux is in one of these kernel states:

| State | `ps` shows | Meaning |
|---|---|---|
| `TASK_RUNNING` | `R` | On CPU or in kernel's run queue ready to go |
| `TASK_INTERRUPTIBLE` | `S` | Sleeping, can be woken by signal (most Go threads) |
| `TASK_UNINTERRUPTIBLE` | `D` | Sleeping, CANNOT be woken by signal (disk I/O, NFS) |
| `__TASK_STOPPED` | `T` | Stopped by debugger (SIGSTOP/ptrace) |
| `EXIT_ZOMBIE` | `Z` | Thread exited but parent hasn't called wait() yet |

`D` state is the scary one — a thread in `D` can't be killed even with `SIGKILL`. It's waiting on something that the kernel insists must complete (usually disk I/O or an NFS mount). If you see many `D` threads, you likely have a storage problem.

## Systems programming glossary — the layer cake

```
┌─────────────────────────────────────┐
│         Your Code (C / Go / etc)    │
├─────────────────────────────────────┤
│         libc  (glibc / musl)        │  ← user-space library
├─────────────────────────────────────┤
│         Syscall interface           │  ← boundary
├─────────────────────────────────────┤
│         Kernel (Linux)              │
└─────────────────────────────────────┘
```

### Kernel & Syscalls (the bottom layer)

The kernel is the OS. It controls hardware, memory, processes, networking. Your code can't talk to hardware directly — it asks the kernel via **syscalls**. A syscall is a numbered function: you put the number in a CPU register, put args in other registers, and execute the `syscall` CPU instruction. Linux has ~450 of them.

Examples: `read(2)`, `write(2)`, `open(2)`, `socket(2)`, `epoll_create1(2)`, `fcntl(2)`.

The `(2)` in man pages means "section 2 = syscalls." Run `man 2 read` to see the syscall docs.

### libc (the wrapper layer)

Nobody wants to write raw assembly to invoke syscalls. **libc** is a C library that wraps every syscall into a normal C function. When you call `read()` in C, you're calling libc's `read()`, which does the register setup + `syscall` instruction for you.

But libc does **more** than just wrap syscalls. It also provides higher-level functions built on top of them:

| libc function | Built on syscall(s) |
|---|---|
| `printf()` | `write` |
| `malloc()` / `free()` | `brk` / `mmap` |
| `fopen()` / `fread()` | `open` / `read` (with buffering) |
| `getaddrinfo()` | `socket` / DNS resolution |

**Implementations of libc:**
- **glibc** (GNU) — most Linux distros, feature-rich, large
- **musl** — Alpine Linux, lightweight, static-linking friendly
- **bionic** — Android
- **macOS libSystem** — Apple's version

They all provide the same functions but with different internals and performance characteristics.

### C Standard vs POSIX — two different specs

**C Standard** (C99, C11, C17, C23) — defined by the language itself. Every C compiler on every OS must provide these. Pure language stuff, nothing OS-specific.

| Header | What's in it |
|---|---|
| `<stdio.h>` | `printf`, `scanf`, `fopen`, `fread` |
| `<stdlib.h>` | `malloc`, `free`, `atoi`, `exit`, `qsort` |
| `<string.h>` | `strlen`, `memcpy`, `strcmp` |
| `<math.h>` | `sin`, `cos`, `sqrt` |

**POSIX** — defined by IEEE/The Open Group. Extends the C standard with OS-level stuff. Linux and macOS are (mostly) POSIX-compliant. Windows is not.

| Header | What's in it |
|---|---|
| `<unistd.h>` | `read`, `write`, `close`, `fork`, `exec`, `pipe` |
| `<fcntl.h>` | `fcntl`, `open` flags (`O_NONBLOCK`, `O_CREAT`) |
| `<sys/socket.h>` | `socket`, `bind`, `listen`, `accept`, `connect` |
| `<sys/epoll.h>` | `epoll_create1`, `epoll_ctl`, `epoll_wait` (Linux-only, not even POSIX) |
| `<pthread.h>` | `pthread_create`, `pthread_mutex_lock` |

So when someone says "stdlib" they usually mean `<stdlib.h>` specifically (malloc, exit, etc.), but sometimes loosely mean "the C standard library" (all the C standard headers). **libc is the library that implements both** — the C standard functions AND the POSIX functions AND the syscall wrappers. It's all one library.

### fcntl, ioctl, setsockopt — the catch-all syscalls

Three "multi-purpose" syscalls that each control a different domain:

**`fcntl`** — "file control." Manipulates properties of file descriptors.
```c
fcntl(fd, F_SETFL, O_NONBLOCK);   // set FD to non-blocking
fcntl(fd, F_DUPFD, 0);            // duplicate an FD
fcntl(fd, F_SETFD, FD_CLOEXEC);   // close FD on exec
```

**`ioctl`** — "I/O control." Device-specific operations. A dumping ground for anything that doesn't fit elsewhere.
```c
ioctl(fd, TIOCGWINSZ, &size);     // get terminal window size
ioctl(fd, SIOCGIFADDR, &ifr);     // get network interface config
```

**`setsockopt`** — socket-specific options. What we used for `SO_REUSEADDR`.
```c
setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &val, sizeof(val));
setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &val, sizeof(val));
```

### Where Go fits

Go is unusual — it **bypasses libc entirely** on Linux. Go's `syscall.Read()` doesn't call glibc's `read()`. It directly executes the `syscall` CPU instruction. This is why Go binaries are statically linked and don't depend on glibc.

```
C program:      your code → glibc read() → syscall instruction → kernel
Go program:     your code → syscall.Read() → syscall instruction → kernel
```

This is also why Go uses `SIGURG` for goroutine preemption (causing the `EINTR` noise) — it can't rely on libc's signal handling.

## Implementing net.Conn — and why deadlines don't work in an event loop

### What net.Conn requires

`net.Conn` is Go's universal connection interface. Any code that does network I/O (HTTP servers, TLS, proxies) accepts `net.Conn`. It has 8 methods:

| Method | Purpose |
|---|---|
| `Read(b []byte) (int, error)` | Receive data — wraps `read(2)` syscall |
| `Write(b []byte) (int, error)` | Send data — wraps `write(2)` syscall |
| `Close() error` | Done with connection — wraps `close(2)`, sends TCP FIN |
| `LocalAddr() net.Addr` | Our side of the connection (server IP:port) |
| `RemoteAddr() net.Addr` | Their side (client IP:ephemeral port) |
| `SetDeadline(t time.Time) error` | Set both read + write deadline |
| `SetReadDeadline(t time.Time) error` | How long read() waits before giving up |
| `SetWriteDeadline(t time.Time) error` | How long write() waits before giving up |

### Address methods — two syscalls we avoid

Every TCP connection is a 4-tuple: `(localIP:localPort, remoteIP:remotePort)`.

- **`getsockname(fd)`** — "what is MY address on this socket?" → gives the local side
- **`getpeername(fd)`** — "who is on the OTHER end?" → gives the remote side

Both are syscalls you can call anytime on a connected socket. But since addresses never change for a given connection, we store them once at `Accept` time (remote addr comes from `Accept`'s return value, local addr is our known bind address). No need to syscall on every `LocalAddr()`/`RemoteAddr()` call.

The client's port is an **ephemeral port** (typically 32768–60999) assigned by the client's OS kernel. Combined with the client IP, this uniquely identifies who you're talking to — useful for logging, rate limiting, or IP-based access control.

### Why deadline methods are no-ops in our server

Our FDs are **non-blocking**. `read()` never waits — it returns instantly with data or `EAGAIN`. Kernel socket timeouts (`SO_RCVTIMEO`/`SO_SNDTIMEO`) only fire when `read()` is actually **blocking and waiting** for data. If you never block, there's nothing to time out.

Two approaches to real deadlines:

**Approach 1: Kernel socket timeouts (`SO_RCVTIMEO`/`SO_SNDTIMEO`)**
```c
// setsockopt sets a per-socket receive timeout
setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeval, sizeof(timeval));
```
When `read()` blocks longer than the specified duration, the kernel returns `EAGAIN`. Simple but only works with **blocking** FDs. Useless for our non-blocking event loop.

**Approach 2: Epoll-based deadlines (what Go's netpoller actually does)**

This is the real solution and it's elegant:

1. Each connection stores a deadline timestamp in a **timer heap** (min-heap sorted by earliest expiry)
2. Before each `epoll_wait`, compute: `timeout = earliest_deadline - now`
3. Pass that as `epoll_wait`'s timeout argument (instead of `-1` which means "wait forever")
4. When `epoll_wait` returns due to timeout (not because an FD had data), walk the heap, find all expired deadlines, and wake those connections with `os.ErrDeadlineExceeded`
5. Every `SetReadDeadline()` call inserts or updates an entry in the heap

The key insight: **epoll_wait's timeout parameter becomes the deadline mechanism.** The event loop itself wakes up on schedule — it's not a per-socket kernel feature, it's application-level timer management layered on top of epoll.

**Why we didn't implement this:** It requires a timer heap data structure, heap maintenance on every deadline set/clear/expire, and timeout-vs-I/O disambiguation after each `epoll_wait` return. That's essentially reimplementing Go's netpoller scheduler — a significant chunk of infrastructure. Our stubs return `nil` and satisfy the interface honestly.

## Go's netpoller and scheduler

Go's `net` package uses epoll (Linux) / kqueue (macOS) internally via the **netpoller**. When a goroutine calls `conn.Read()` and there's no data, the goroutine is **parked** (~4KB cost, no OS thread blocked). When data arrives, the netpoller wakes it via `epoll_wait` and the goroutine resumes as if `Read()` just returned. You get thread-per-connection programming with event-loop efficiency.

The netpoller is deeply integrated with Go's **G-M-P scheduler** — goroutines (G), OS threads (M), and processors (P) work together so that parked goroutines don't waste threads, and the sysmon background thread handles netpolling, preemption, and deadline expiry.

Building our epoll loop manually teaches what the netpoller does invisibly. In production Go you'd just use `net.Listen` + goroutine per connection and let the runtime handle multiplexing.

**Deep dives:**
- [docs/go-scheduler-deep-dive.md](docs/go-scheduler-deep-dive.md) — narrative walkthrough of the Go scheduler based on GopherCon 2021 talk: GMP model, run queues, work stealing, preemption evolution, time slice inheritance, LockOSThread, suggested learning path
- [docs/gmp-scheduler.md](docs/gmp-scheduler.md) — struct-level GMP details, run queues, work stealing, syscall handoff, preemption (cooperative + SIGURG), sysmon, GOMAXPROCS, spinning threads, parking/sleeping/futex, channel blocking, startup sequence, stack growth
- [docs/scheduler-fairness.md](docs/scheduler-fairness.md) — N:M models, convoy effect, why schedtick uses 61, FIFO vs LIFO, time slice inheritance, LockOSThread deep mechanics (stoplockedm/startlockedm), runtime APIs, observability
- [docs/netpoller.md](docs/netpoller.md) — data structures (pollDesc, pollCache, timer heap), step-by-step Read() trace, sysmon, platform backends (epoll/kqueue/IOCP), pollDesc lifecycle, deadline expiry, comparison with our server
- [docs/eintr-preemption.md](docs/eintr-preemption.md) — SIGURG preemption timeline, tgkill vs kill, why epoll_wait returns EINTR, gsignal stacks
- [docs/go-sched-que.md](docs/go-sched-que.md) — Q&A format: parking/spinning/sleeping explained from scratch, OS thread states, futex, semaphores, all G/M/P states, schedule()/execute()/goready() internals, channels vs pipes, LockOSThread edge cases

## Resources

### Linux / POSIX man pages
- [`man 7 epoll`](https://man7.org/linux/man-pages/man7/epoll.7.html) — epoll overview, LT vs ET behavior, usage patterns
- [`man 2 epoll_create1`](https://man7.org/linux/man-pages/man2/epoll_create1.2.html) — create an epoll instance
- [`man 2 epoll_ctl`](https://man7.org/linux/man-pages/man2/epoll_ctl.2.html) — add/modify/remove FDs from epoll
- [`man 2 epoll_wait`](https://man7.org/linux/man-pages/man2/epoll_wait.2.html) — wait for events on an epoll instance
- [`man 2 socket`](https://man7.org/linux/man-pages/man2/socket.2.html) — create a socket (SOCK_STREAM, SOCK_NONBLOCK, etc.)
- [`man 2 bind`](https://man7.org/linux/man-pages/man2/bind.2.html) — assign an address to a socket
- [`man 2 listen`](https://man7.org/linux/man-pages/man2/listen.2.html) — mark socket as passive (accepting connections)
- [`man 2 accept`](https://man7.org/linux/man-pages/man2/accept.2.html) — accept a connection, returns new FD + client address
- [`man 2 read`](https://man7.org/linux/man-pages/man2/read.2.html) — read from an FD (EAGAIN, EINTR behavior)
- [`man 2 write`](https://man7.org/linux/man-pages/man2/write.2.html) — write to an FD
- [`man 2 close`](https://man7.org/linux/man-pages/man2/close.2.html) — close an FD, sends TCP FIN
- [`man 2 setsockopt`](https://man7.org/linux/man-pages/man2/setsockopt.2.html) — set socket options (SO_REUSEADDR, SO_RCVTIMEO, etc.)
- [`man 2 getsockname`](https://man7.org/linux/man-pages/man2/getsockname.2.html) — get local address of a socket
- [`man 2 getpeername`](https://man7.org/linux/man-pages/man2/getpeername.2.html) — get remote address of a socket
- [`man 2 fcntl`](https://man7.org/linux/man-pages/man2/fcntl.2.html) — file descriptor control (O_NONBLOCK, FD_CLOEXEC, etc.)
- [`man 7 tcp`](https://man7.org/linux/man-pages/man7/tcp.7.html) — TCP protocol, socket options (TCP_NODELAY, TIME_WAIT, etc.)
- [`man 7 socket`](https://man7.org/linux/man-pages/man7/socket.7.html) — socket API overview, SO_* options
- [`man 7 signal`](https://man7.org/linux/man-pages/man7/signal.7.html) — signal overview (relevant for EINTR, SIGURG)
- [`man 2 tgkill`](https://man7.org/linux/man-pages/man2/tgkill.2.html) — send signal to a specific thread (used by Go for preemption)

### Linux kernel source
- [`fs/eventpoll.c`](https://github.com/torvalds/linux/blob/master/fs/eventpoll.c) — epoll implementation (red-black tree, ready list, LT/ET logic)
- [`net/ipv4/tcp.c`](https://github.com/torvalds/linux/blob/master/net/ipv4/tcp.c) — TCP implementation

### Go stdlib source (net package + runtime)
- [`net/net.go`](https://github.com/golang/go/blob/master/src/net/net.go) — `Conn` interface definition, `OpError`, core types
- [`net/tcpsock.go`](https://github.com/golang/go/blob/master/src/net/tcpsock.go) — `TCPConn`, `TCPAddr`, `TCPListener` — the types you get from `net.Listen("tcp", ...)`
- [`net/fd_posix.go`](https://github.com/golang/go/blob/master/src/net/fd_posix.go) — `netFD` struct, the real FD wrapper that `TCPConn` delegates to
- [`net/fd_unix.go`](https://github.com/golang/go/blob/master/src/net/fd_unix.go) — Unix-specific `netFD` methods: `Read()`, `Write()`, `Accept()`, `Close()` — where syscalls actually happen
- [`net/sock_posix.go`](https://github.com/golang/go/blob/master/src/net/sock_posix.go) — `socket()` call, SO_REUSEADDR, bind, listen — the setup path
- [`net/dial.go`](https://github.com/golang/go/blob/master/src/net/dial.go) — `Dial()`, `DialContext()` — client-side connection
- [`internal/poll/fd_poll_runtime.go`](https://github.com/golang/go/blob/master/src/internal/poll/fd_poll_runtime.go) — bridge between `net` and the runtime netpoller (`pollDesc` init, wait, deadline setting)
- [`internal/poll/fd_unix.go`](https://github.com/golang/go/blob/master/src/internal/poll/fd_unix.go) — `FD.Read()`, `FD.Write()` with EAGAIN retry loops — this is where "try syscall → park goroutine → retry" lives
- [`runtime/netpoll.go`](https://github.com/golang/go/blob/master/src/runtime/netpoll.go) — netpoller core: `pollDesc`, `netpollblock()`, timer heap for deadlines
- [`runtime/netpoll_epoll.go`](https://github.com/golang/go/blob/master/src/runtime/netpoll_epoll.go) — Linux epoll backend: `netpollinit()`, `netpollopen()`, `netpoll()`
- [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go) — the G-M-P scheduler: `schedule()`, `findRunnable()`, `sysmon()`
- [`runtime/runtime2.go`](https://github.com/golang/go/blob/master/src/runtime/runtime2.go) — `g`, `m`, `p` struct definitions
- [`syscall/syscall_linux.go`](https://github.com/golang/go/blob/master/src/syscall/syscall_linux.go) — Go's raw syscall wrappers (what we use: `syscall.Socket`, `syscall.Bind`, etc.)

### Design documents
- [Scalable Go Scheduler Design Doc — Dmitry Vyukov (2012)](https://docs.google.com/document/d/1TTj4T2JO42uD5ID9e89oa0sLKhJYD0Y_kqxDv3I3XMw/edit) — the original proposal that introduced P
- [Non-cooperative goroutine preemption — Austin Clements](https://go.googlesource.com/proposal/+/master/design/24543-non-cooperative-preemption.md) — design doc for async preemption (SIGURG)

### Go runtime source files (read in this order)
1. [`runtime/runtime2.go`](https://github.com/golang/go/blob/master/src/runtime/runtime2.go) — `g`, `m`, `p` struct definitions
2. [`runtime/proc.go`](https://github.com/golang/go/blob/master/src/runtime/proc.go) — the scheduler itself: `schedule()`, `findRunnable()`, `sysmon()`, `retake()`
3. [`runtime/netpoll.go`](https://go.dev/src/runtime/netpoll.go) — netpoller core: `pollDesc`, `netpollblock()`
4. `runtime/netpoll_epoll.go` — Linux epoll backend
5. `runtime/stack.go` — stack growth, copying, shrinking
6. [`runtime/preempt.go`](https://go.dev/src/runtime/preempt.go) — async preemption logic

### Blog posts
- [Daniel Morsing — "The Go Scheduler" (2013)](https://morsmachine.dk/go-scheduler) — best first read, short and clear
- [Daniel Morsing — "The Go Netpoller" (2013)](https://morsmachine.dk/netpoller) — companion post
- [Ardan Labs — "Scheduling In Go" Part I](https://www.ardanlabs.com/blog/2018/08/scheduling-in-go-part1.html) and [Part II](https://www.ardanlabs.com/blog/2018/08/scheduling-in-go-part2.html) — best modern deep-dive series
- [Jaana Dogan — "Go's Work-Stealing Scheduler"](https://rakyll.org/scheduler/) — well-illustrated
- [Cloudflare — "How Stacks are Handled in Go"](https://blog.cloudflare.com/how-stacks-are-handled-in-go/) — segmented-to-copying stack transition
- [DataDog — go-profiler-notes: Goroutine Scheduler](https://datadoghq.dev/go-profiler-notes/mental-model-for-go/goroutine-scheduler.html) — exceptional detail with real diagrams
- [Hidetatz — "Preemption in Go"](https://hidetatz.github.io/goroutine_preemption/) — traces through the SIGURG signal path
- [Dave Cheney — "Why is a Goroutine's Stack Infinite?"](https://dave.cheney.net/2013/06/02/why-is-a-goroutines-stack-infinite)

### Conference talks
- [GopherCon 2018 — Kavya Joshi — "The Scheduler Saga"](https://www.youtube.com/watch?v=YHRO5WQGh0k) — the definitive talk, builds a scheduler from scratch

### Academic papers
- [Columbia University — "Analysis of the Go Runtime Scheduler"](http://www.cs.columbia.edu/~aho/cs6998/reports/12-12-11_DeshpandeSponslerWeiss_GO.pdf) — academic analysis of the scheduler's work-stealing behavior
