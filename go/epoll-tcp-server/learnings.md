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

### Level-triggered epoll re-fires if you don't read
Default epoll is level-triggered: if EPOLLIN fires and you don't read the data, it fires again next `epoll_wait`. Must always drain (or at least read from) ready FDs to avoid busy-looping.

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
- This is what memcached does

### 2. State machine (pure non-blocking)
- Each connection has a state struct tracking its progress (reading headers → processing → writing response)
- Handler advances the state by one small step, then returns to the event loop
- Every stage must be short — if inherently slow, break into chunks or hybrid with a thread pool
- This is what nginx does — every stage is I/O-bound and fast
- Most performant but hardest to write; you're hand-coding what async/await does in higher-level languages

### 3. io_uring (Linux 5.1+)
- Instead of "notify me when ready, then I'll syscall," you submit the syscall itself and the kernel completes it async
- Eliminates the two-step (poll → syscall) overhead of epoll
- Steepest learning curve, most modern approach

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

## How Go's `net` package does it under the hood

Go's standard `net` package uses epoll (Linux) / kqueue (macOS) internally via the **netpoller**. Each goroutine that calls `conn.Read()` parks on the netpoller, and the runtime wakes it when data arrives. You get the thread-per-connection programming model with event-loop efficiency — goroutines are ~4KB vs ~8MB OS threads.

Building the epoll loop manually is valuable for understanding, but in production Go you'd just use `net.Listen` + goroutine per connection and let the runtime handle multiplexing.
