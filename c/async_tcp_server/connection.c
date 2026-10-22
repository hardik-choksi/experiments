#include "connection.h"

#include <arpa/inet.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

static connection connections[MAX_CONNECTIONS];

void conn_table_init(void)
{
    memset(connections, 0, sizeof(connections));
}

/*
 * conn_open records an accepted client fd and both ends of its 4-tuple.
 *
 * We capture the addresses at accept time so lookups later need no syscall.
 * In the real world you could fetch them on demand:
 *   getsockname(fd) -> "what is MY address on this socket?"
 *   getpeername(fd) -> "who is on the OTHER end?"
 * But they never change for a given connection, so storing once is the right
 * move — the same reasoning the Go version uses.
 */
connection *conn_open(int fd, const struct sockaddr_in *raddr,
                      const struct sockaddr_in *laddr)
{
    if (fd < 0 || fd >= MAX_CONNECTIONS)
        return NULL;

    connection *c = &connections[fd];
    c->fd = fd;
    c->in_use = 1;
    c->raddr = *raddr;
    c->laddr = *laddr;
    return c;
}

connection *conn_lookup(int fd)
{
    if (fd < 0 || fd >= MAX_CONNECTIONS)
        return NULL;
    connection *c = &connections[fd];
    return c->in_use ? c : NULL;
}

void conn_release(connection *c)
{
    if (c)
        c->in_use = 0;
}

/*
 * conn_read wraps read(2) — "receive bytes from a file descriptor".
 *
 * Go's net.Conn.Read parks the calling goroutine on the netpoller until data
 * arrives; it looks blocking to the goroutine but is non-blocking to the OS.
 * There is no scheduler to park on here, so this is the bare syscall: on our
 * non-blocking fds it returns immediately with data, 0 (peer sent FIN), or -1
 * with errno EAGAIN. The event loop is what decides which of those means what.
 */
ssize_t conn_read(connection *c, void *buf, size_t len)
{
    return read(c->fd, buf, len);
}

/*
 * conn_write wraps write(2) — "send bytes to a file descriptor".
 *
 * Real world: if the kernel's TCP send buffer is full (slow peer, not ACKing)
 * write() accepts only part of your data and returns a short count. Production
 * servers loop until everything is sent, and when the buffer is entirely full
 * they park the remainder in a per-connection output buffer and wait for
 * EPOLLOUT before retrying.
 *
 * We loop on short writes, which covers the common case. We do NOT buffer on
 * EAGAIN — with a ~13 byte response against a send buffer of 128KB+ it cannot
 * happen in practice. A server sending large bodies would need the full
 * EPOLLOUT dance.
 */
ssize_t conn_write(connection *c, const void *buf, size_t len)
{
    const char *p = buf;
    size_t written = 0;

    while (written < len) {
        ssize_t n = write(c->fd, p + written, len - written);
        if (n < 0)
            return -1;            /* caller inspects errno */
        written += (size_t)n;
    }
    return (ssize_t)written;
}

/*
 * conn_close wraps close(2) — "we are done with this fd".
 *
 * Inside the kernel this:
 *   1. sends a TCP FIN to the peer ("I'm done sending")
 *   2. frees the fd number for reuse by future socket/accept calls
 *   3. releases the kernel-side socket buffers (~128KB each way)
 *
 * Note on ordering: closing an fd removes it from every epoll set it belongs
 * to automatically. We still call EPOLL_CTL_DEL explicitly before closing,
 * because relying on the implicit removal breaks the moment an fd is shared
 * (dup, fork) — the set holds the underlying file description, not the number.
 */
int conn_close(connection *c)
{
    int rc = close(c->fd);
    conn_release(c);
    return rc;
}

const char *conn_addr_str(const struct sockaddr_in *addr, char *buf, size_t len)
{
    char ip[INET_ADDRSTRLEN];
    inet_ntop(AF_INET, &addr->sin_addr, ip, sizeof(ip));
    snprintf(buf, len, "%s:%d", ip, ntohs(addr->sin_port));
    return buf;
}
