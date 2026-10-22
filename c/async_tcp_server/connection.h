#ifndef CONNECTION_H
#define CONNECTION_H

#include <netinet/in.h>
#include <stddef.h>
#include <sys/types.h>

/*
 * Connection wraps a raw file descriptor plus the two ends of the TCP 4-tuple.
 *
 * The Go version of this project implements net.Conn so the type is a drop-in
 * for anything in the stdlib. C has no such interface to satisfy, so this is
 * just the data + a handful of thin syscall wrappers. The point is the same:
 * keep "what is this socket" in one place instead of passing a bare int around.
 */
typedef struct {
    int fd;
    int in_use;                   /* slot occupancy flag for the fd-indexed table */
    struct sockaddr_in laddr;     /* our end   — what getsockname(2) would report */
    struct sockaddr_in raddr;     /* their end — what getpeername(2) would report */
} connection;

/*
 * The connection table.
 *
 * Go used map[int]*Connection. In C we exploit a POSIX guarantee instead: the
 * kernel always hands out the LOWEST unused fd number. So fds stay densely
 * packed near zero and a flat array indexed by fd is both simpler and faster
 * than any hash map — O(1) with no hashing and no allocation.
 *
 * Bounded by the RLIMIT_NOFILE we set at startup.
 */
#define MAX_CONNECTIONS 4096

void        conn_table_init(void);
connection *conn_open(int fd, const struct sockaddr_in *raddr,
                      const struct sockaddr_in *laddr);
connection *conn_lookup(int fd);
void        conn_release(connection *c);

/* Thin syscall wrappers — see connection.c for what the kernel does in each. */
ssize_t conn_read(connection *c, void *buf, size_t len);
ssize_t conn_write(connection *c, const void *buf, size_t len);
int     conn_close(connection *c);

/* Formats addr as "ip:port" into buf. Handy for logging. */
const char *conn_addr_str(const struct sockaddr_in *addr, char *buf, size_t len);

#endif /* CONNECTION_H */
