/*
 * An asynchronous (epoll-driven) HTTP server in C.
 *
 * This is a direct port of ../../go/epoll-tcp-server, which does the same thing
 * with Go's raw syscall package. Single thread, one epoll set, level-triggered.
 * Everything the Go version explains in comments is explained here too, plus
 * the places where C forces a different choice.
 */

#include "connection.h"

#include <arpa/inet.h>
#include <errno.h>
#include <netinet/in.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/resource.h>
#include <sys/socket.h>
#include <unistd.h>

#define LISTEN_IP    "127.0.0.1"
#define LISTEN_PORT  8080
#define BACKLOG      511          /* same as the Go version; see note below */
#define MAX_EVENTS   511
#define READ_BUF_SZ  4096

static const char RESP_KEEPALIVE[] =
    "HTTP/1.1 200 OK\r\n"
    "Connection: keep-alive\r\n"
    "Content-Length: 13\r\n"
    "\r\n"
    "Hello, world!";

static const char RESP_CLOSE[] =
    "HTTP/1.0 200 OK\r\n"
    "Connection: close\r\n"
    "Content-Length: 13\r\n"
    "\r\n"
    "Hello, world!";

static int epoll_fd = -1;
static int server_fd = -1;

/*
 * Raise the open-file limit to whatever the hard limit allows.
 *
 * Every connection is an fd, so RLIMIT_NOFILE is the real ceiling on
 * concurrency. The default soft limit is often 1024 — a C10K server that never
 * touches this will start failing accept() with EMFILE long before the network
 * or CPU is the bottleneck. Go's runtime does this for you at startup; in C it
 * is on us.
 */
static void raise_fd_limit(void)
{
    struct rlimit rl;
    if (getrlimit(RLIMIT_NOFILE, &rl) != 0) {
        perror("getrlimit");
        return;
    }
    rl.rlim_cur = rl.rlim_max;
    if (setrlimit(RLIMIT_NOFILE, &rl) != 0)
        perror("setrlimit");
}

/*
 * Register an fd with the epoll set for the given events.
 *
 * epoll keeps an internal red-black tree of watched fds plus a ready list. The
 * ADD call is what inserts into that tree; epoll_wait then only ever walks the
 * ready list, which is why it is O(ready) rather than O(watched) like select
 * and poll. That difference is the entire reason this server design scales.
 *
 * We store the fd in ev.data.fd. The union also offers a void* — a production
 * server usually stashes the connection pointer there to skip the lookup
 * entirely. We keep the fd so the flow reads like the Go version.
 */
static int epoll_add(int fd, uint32_t events)
{
    struct epoll_event ev;
    memset(&ev, 0, sizeof(ev));
    ev.events = events;
    ev.data.fd = fd;
    return epoll_ctl(epoll_fd, EPOLL_CTL_ADD, fd, &ev);
}

static void epoll_del(int fd)
{
    /* The event argument is ignored since Linux 2.6.9, but older kernels
     * dereference it, so passing NULL was a portability bug for years. */
    epoll_ctl(epoll_fd, EPOLL_CTL_DEL, fd, NULL);
}

/* Remove from epoll, close the socket, free the table slot. Always in that
 * order — see the note in conn_close about shared file descriptions. */
static void drop_connection(connection *c)
{
    epoll_del(c->fd);
    conn_close(c);
}

/*
 * Accept everything that is pending, not just one connection.
 *
 * With level-triggered epoll a single accept per wakeup would still be
 * correct — the listener stays readable and we would be told again. But under
 * load several clients can land in the accept queue between two epoll_wait
 * calls, and draining the queue in one go saves a syscall round trip per
 * connection. We stop when accept4 returns EAGAIN, which is the kernel saying
 * "queue empty".
 */
static void accept_connections(const struct sockaddr_in *laddr)
{
    for (;;) {
        struct sockaddr_in raddr;
        socklen_t addrlen = sizeof(raddr);

        /*
         * accept4 with SOCK_NONBLOCK sets the flag atomically. The portable
         * two-step (accept then fcntl O_NONBLOCK) leaves a window where the fd
         * is blocking, and it costs an extra syscall on every connection.
         * Go's syscall.Accept does the same thing under the hood on Linux.
         */
        int client_fd = accept4(server_fd, (struct sockaddr *)&raddr, &addrlen,
                                SOCK_NONBLOCK);
        if (client_fd < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK)
                return;                  /* drained the queue — normal exit */
            if (errno == EINTR)
                continue;                /* interrupted by a signal, retry */
            /*
             * EMFILE/ENFILE mean we are out of fds. A production server keeps a
             * spare fd in reserve to close-and-accept so the listener does not
             * spin hot on a queue it cannot drain. We just log and back off.
             */
            perror("accept4");
            return;
        }

        if (client_fd >= MAX_CONNECTIONS) {
            fprintf(stderr, "fd %d exceeds connection table, closing\n", client_fd);
            close(client_fd);
            continue;
        }

        connection *c = conn_open(client_fd, &raddr, laddr);
        if (!c) {
            close(client_fd);
            continue;
        }

        /* Now that we track it, ask epoll to tell us when it has data. */
        if (epoll_add(client_fd, EPOLLIN) != 0) {
            perror("epoll_ctl: add client");
            conn_close(c);
            continue;
        }
    }
}

/* Handle a readable client socket: read the request, reply, maybe close. */
static void handle_client(connection *c)
{
    char buf[READ_BUF_SZ];

    ssize_t n = conn_read(c, buf, sizeof(buf) - 1);

    /*
     * Three outcomes, exactly as in the Go version:
     *   n == 0                 client sent FIN (graceful close)
     *   n < 0, errno EAGAIN    no data after all — a spurious wakeup, skip
     *   n < 0, other errno     real error (RST, ETIMEDOUT, ...)
     */
    if (n == 0 || (n < 0 && errno != EAGAIN && errno != EWOULDBLOCK)) {
        drop_connection(c);
        return;
    }
    if (n < 0)
        return;

    buf[n] = '\0';

    /*
     * The protocol shortcut we inherit from the Go version: sniff the version
     * string instead of parsing. HTTP/1.0 defaults to close, HTTP/1.1 defaults
     * to keep-alive.
     *
     * What this skips, so it is on the record: we do not parse the request
     * line, we ignore an explicit "Connection: close" header on a 1.1 request,
     * and we assume the whole request arrived in one read. A real server
     * accumulates bytes per connection until it sees the "\r\n\r\n" header
     * terminator, because TCP is a byte stream with no message boundaries — one
     * request can span several reads and two pipelined requests can arrive in
     * one. Doing it properly needs a per-connection read buffer and a parser
     * state machine, which is a different project.
     */
    int keep_alive = strstr(buf, "HTTP/1.1") != NULL;

    if (keep_alive) {
        if (conn_write(c, RESP_KEEPALIVE, sizeof(RESP_KEEPALIVE) - 1) < 0) {
            perror("write");
            drop_connection(c);
        }
    } else {
        if (conn_write(c, RESP_CLOSE, sizeof(RESP_CLOSE) - 1) < 0)
            perror("write");
        drop_connection(c);
    }
}

int main(void)
{
    struct sockaddr_in laddr;
    struct epoll_event events[MAX_EVENTS];

    /*
     * Ignore SIGPIPE.
     *
     * Writing to a socket whose peer has closed raises SIGPIPE, whose default
     * action is to kill the process — a client that hangs up mid-response would
     * take the server down. Ignoring it makes write() return EPIPE instead,
     * which we handle as an ordinary error. Go installs this handler for you;
     * in C forgetting it is a classic way to lose a server to a hostile client.
     */
    signal(SIGPIPE, SIG_IGN);

    raise_fd_limit();
    conn_table_init();

    /* Non-blocking listening socket. Blocking accept() would stall the single
     * thread that drives every connection, which is the whole thing we avoid. */
    server_fd = socket(AF_INET, SOCK_STREAM | SOCK_NONBLOCK, 0);
    if (server_fd < 0) {
        perror("socket");
        return 1;
    }

    /*
     * SO_REUSEADDR lets us re-bind while old connections sit in TIME_WAIT.
     * After a close the kernel holds the 4-tuple for 2*MSL (60s on Linux) to
     * absorb stray packets from the old connection; without this option a
     * restart fails with EADDRINUSE for that whole minute.
     */
    int one = 1;
    if (setsockopt(server_fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one)) != 0)
        perror("setsockopt: SO_REUSEADDR");

    memset(&laddr, 0, sizeof(laddr));
    laddr.sin_family = AF_INET;
    laddr.sin_port = htons(LISTEN_PORT);
    if (inet_pton(AF_INET, LISTEN_IP, &laddr.sin_addr) != 1) {
        fprintf(stderr, "bad listen address %s\n", LISTEN_IP);
        return 1;
    }

    if (bind(server_fd, (struct sockaddr *)&laddr, sizeof(laddr)) != 0) {
        perror("bind");
        return 1;
    }

    /*
     * backlog is the size of the ACCEPTED-but-not-yet-accept()ed queue. Linux
     * keeps two queues: the SYN queue for half-open handshakes and this accept
     * queue for completed ones. The value is capped by
     * /proc/sys/net/core/somaxconn (4096 on modern kernels), so asking for more
     * than that is silently clamped. 511 is the nginx/redis convention — 512
     * minus one, since some allocators round 512 up to a larger bucket.
     */
    if (listen(server_fd, BACKLOG) != 0) {
        perror("listen");
        return 1;
    }

    /*
     * epoll_create1(0) — the argument is flags, not a size hint. The old
     * epoll_create(size) took a capacity estimate that the kernel has ignored
     * since 2.6.8; epoll_create1 replaced it and adds EPOLL_CLOEXEC.
     */
    epoll_fd = epoll_create1(0);
    if (epoll_fd < 0) {
        perror("epoll_create1");
        return 1;
    }

    /* Watch the listener for readability — for a listening socket "readable"
     * means "a completed connection is waiting in the accept queue". */
    if (epoll_add(server_fd, EPOLLIN) != 0) {
        perror("epoll_ctl: add listener");
        return 1;
    }

    printf("tcp server started on %s:%d\n", LISTEN_IP, LISTEN_PORT);
    fflush(stdout);

    for (;;) {
        /* -1 = block forever. This is the only place the process sleeps: one
         * thread parked in the kernel until any of N sockets needs attention. */
        int n = epoll_wait(epoll_fd, events, MAX_EVENTS, -1);
        if (n < 0) {
            if (errno == EINTR)
                continue;            /* signal interrupted the wait, not an error */
            perror("epoll_wait");
            continue;
        }

        for (int i = 0; i < n; i++) {
            int fd = events[i].data.fd;

            if (fd == server_fd) {
                accept_connections(&laddr);
                continue;
            }

            connection *c = conn_lookup(fd);
            if (!c) {
                fprintf(stderr, "unknown fd %d in epoll event, skipping\n", fd);
                epoll_del(fd);
                continue;
            }

            /*
             * EPOLLHUP/EPOLLERR arrive whether or not we asked for them. HUP
             * means the peer is fully gone, ERR means the socket hit an error
             * (typically an RST). Either way there is nothing left to do but
             * tear the connection down.
             */
            if (events[i].events & (EPOLLHUP | EPOLLERR)) {
                drop_connection(c);
                continue;
            }

            if (events[i].events & EPOLLIN)
                handle_client(c);
        }
    }

    /* Not reached — the loop above never exits. Kept to document the teardown
     * a signal-driven shutdown path would perform. */
    close(epoll_fd);
    close(server_fd);
    return 0;
}
