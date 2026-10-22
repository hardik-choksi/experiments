# Async TCP/HTTP Server in C — Learnings

A direct port of [`../../go/epoll-tcp-server`](../../go/epoll-tcp-server). Same
architecture: one thread, one epoll set, level-triggered, non-blocking sockets.
This file records what the syscalls actually do and where C forced a decision
that Go made for us.

## Files

| File | Contents |
|---|---|
| `server.c` | Socket setup and the epoll event loop |
| `connection.h` / `connection.c` | The connection struct, the fd-indexed table, syscall wrappers |
| `Makefile` | `make run` to build and run, `make debug` for ASan/UBSan |

## The event loop in one paragraph

Create a non-blocking listening socket, register it with epoll, then block in
`epoll_wait` forever. Each wakeup hands back only the fds that are ready. If the
listener is ready, drain the accept queue and register each new client. If a
client is ready, read the request, write the response, and close it unless the
client asked for keep-alive. Nothing ever blocks, so one thread drives every
connection.

## Why epoll instead of select/poll

`select` and `poll` take the entire watch list as an argument on every call, so
the kernel walks all N fds each time to find the few that are ready — O(N) per
call, with the list copied in and out across the syscall boundary every time.

epoll splits that into two pieces. `epoll_ctl` maintains a persistent set inside
the kernel (a red-black tree keyed by fd). Readiness is then driven by callbacks
from the socket layer, which push ready fds onto a separate ready list.
`epoll_wait` just pops that list, so it is O(ready), not O(watched), and the
watch set is never re-copied. With 10,000 idle connections and 5 active ones,
poll examines 10,000 entries and epoll examines 5. That difference is the entire
reason this design scales.

## Level-triggered vs edge-triggered

We use the default, level-triggered: epoll reports an fd as long as the
condition holds. If data is still unread, the next `epoll_wait` says so again.
Forgiving — a short read is not a lost event.

Edge-triggered (`EPOLLET`) reports only on a *transition*, so one notification
arrives when data arrives, and no more until new data arrives on top. That
obliges you to read in a loop until `EAGAIN` on every wakeup, because anything
left in the buffer will never be announced again. Fewer syscalls when it is
right, silent stalled connections when it is wrong.

One place we borrow the edge-triggered habit anyway: `accept_connections` loops
until `EAGAIN` rather than accepting once. Level-triggered would make a single
accept correct, but draining the queue in one wakeup saves a syscall round trip
per connection under load.

## What C made us decide that Go did not

### The connection table

Go used `map[int]*Connection`. C has no map, but it turns out not to need one:
POSIX guarantees the kernel returns the **lowest unused fd**, so fd numbers stay
densely packed near zero. A flat array indexed by fd is O(1) with no hashing and
no allocation — strictly better than a hash map here. The cost is a fixed
ceiling (`MAX_CONNECTIONS`, 4096) and an explicit bounds check on every accept.

A further step we did not take: `epoll_event.data` is a union, so a production
server stores the connection *pointer* there instead of the fd and skips the
lookup entirely. We keep the fd so the flow reads like the Go original.

### SIGPIPE

Writing to a socket whose peer has closed raises `SIGPIPE`, and its default
action is to **kill the process**. A client hanging up mid-response would take
the whole server down. `signal(SIGPIPE, SIG_IGN)` converts it into a `write()`
returning `EPIPE`, which is just an ordinary error.

The Go runtime installs this handler for you. In C, forgetting this one line is
a classic way to lose a server to a badly behaved client.

### RLIMIT_NOFILE

Every connection is an fd, so this rlimit is the hard ceiling on concurrency.
The default soft limit is often 1024, which means a C10K server that never
touches it starts failing `accept()` with `EMFILE` long before CPU or network
is the constraint. Go raises it at startup; we do it explicitly in
`raise_fd_limit()`.

### accept4 vs accept

`accept4(..., SOCK_NONBLOCK)` sets the flag atomically. The portable two-step —
`accept()` then `fcntl(O_NONBLOCK)` — costs an extra syscall per connection and
leaves a window where the fd is blocking. Linux-only, which is fine since epoll
is Linux-only anyway.

## Socket options and constants worth knowing

**`SO_REUSEADDR`** — after a close, the kernel holds the 4-tuple in `TIME_WAIT`
for 2×MSL (60s on Linux) to absorb stray packets from the old connection.
Without this option, restarting the server fails with `EADDRINUSE` for that
whole minute.

**`listen(fd, 511)`** — the backlog sizes the queue of *completed* connections
waiting to be accepted. Linux keeps two queues: the SYN queue for half-open
handshakes, and this accept queue for finished ones. The value is clamped by
`/proc/sys/net/core/somaxconn`, so asking for more is silently capped. 511 is
the nginx/redis convention: 512 minus one, since some allocators round 512 up
into a larger size class.

**`epoll_create1(0)`** — the argument is *flags*, not a size hint. The older
`epoll_create(size)` took a capacity estimate that the kernel has ignored since
2.6.8; `epoll_create1` replaced it and adds `EPOLL_CLOEXEC`.

## Reading a socket: the three outcomes

```c
ssize_t n = read(fd, buf, len);
```

| Result | Meaning | Action |
|---|---|---|
| `n > 0` | got bytes | process them |
| `n == 0` | peer sent FIN — graceful close | tear down |
| `n < 0`, `errno == EAGAIN` | nothing available; spurious wakeup | skip, wait for the next event |
| `n < 0`, other errno | real error (RST → `ECONNRESET`, etc.) | tear down |

Conflating `n == 0` with an error, or treating `EAGAIN` as one, are the two
classic bugs. `n == 0` is specifically FIN and nothing else.

## Teardown ordering

Always `EPOLL_CTL_DEL`, then `close()`. Closing an fd does remove it from epoll
sets automatically, but only when that was the *last* reference — epoll holds
the underlying open file description, not the fd number. The moment an fd is
shared via `dup()` or `fork()`, relying on implicit removal leaves a stale
registration behind. Explicit deletion costs one syscall and always works.

`EPOLLHUP` and `EPOLLERR` are delivered whether or not you ask for them. HUP
means the peer is fully gone; ERR means the socket hit an error, typically an
RST. Both mean tear down.

## What this server does NOT do

Stated plainly, because a partial implementation that hides its gaps is worse
than none.

**No HTTP parsing.** We `strstr` for `"HTTP/1.1"` to pick keep-alive vs close —
the same shortcut the Go version takes. We do not parse the request line, and we
ignore an explicit `Connection: close` on a 1.1 request.

**No request framing.** We assume one request arrives in one `read()`. TCP is a
byte stream with no message boundaries: a request can span several reads, and
two pipelined requests can arrive in one. Doing it properly needs a
per-connection read buffer that accumulates until the `\r\n\r\n` header
terminator, plus a parser state machine.

**No output buffering.** `conn_write` loops on short writes, which handles a
partial send. It does not handle the send buffer being *entirely* full
(`EAGAIN`), which would require parking the remainder in a per-connection output
buffer and waiting for `EPOLLOUT` to retry. With a 13-byte body against a 128KB+
send buffer this cannot happen — but any server sending large bodies needs the
full `EPOLLOUT` dance.

**No timeouts.** A client can open a connection and hold it forever. This is the
same gap the Go version documents in its deadline stubs, and the fix is the
same: a min-heap of deadlines, and passing `earliest_deadline - now` as
`epoll_wait`'s timeout instead of `-1`. When the wait returns on timeout rather
than I/O, walk the heap and drop everything expired. That is roughly what Go's
netpoller does internally.

**Single-threaded.** One core. The usual scale-out is `SO_REUSEPORT` with one
epoll loop per worker thread, letting the kernel hash incoming connections
across listeners — no shared accept queue, no thundering herd.

## Verified behavior

```
curl --http1.1  → connection reused across two requests, Connection: keep-alive
curl --http1.0  → served, then closed
500 requests @ 60 concurrent → all 200, no connections left established
```
