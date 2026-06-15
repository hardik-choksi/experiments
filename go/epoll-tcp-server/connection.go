package main

import (
	"net"
	"syscall"
	"time"
)

// NOTE on net.Conn compliance:
// We implement the full net.Conn interface so our Connection can be passed to any
// code expecting net.Conn (http.ReadRequest, bufio.NewReader, tls.Server, etc.).
// However, the three deadline methods are no-ops — see the comment above them for why.

// Compile-time check: Connection must implement net.Conn.
// If any method is missing or has the wrong signature, this line fails at compile time.
var _ net.Conn = (*Connection)(nil)

// Active connections tracked by FD so the event loop can look up a Connection
// when epoll says "fd 7 has data."
var Connections = make(map[int]*Connection)

// Connection wraps a raw file descriptor into a net.Conn drop-in replacement.
//
// Go's real net.Conn is built on the internal netpoller (which itself uses epoll/kqueue).
// We're reimplementing it on top of raw syscalls to understand what happens underneath.
//
// net.Conn requires 8 methods:
//
//	Read, Write, Close           — data transfer (we already had these)
//	LocalAddr, RemoteAddr        — socket identity
//	SetDeadline, SetReadDeadline, SetWriteDeadline — timeout control
type Connection struct {
	Fd    int
	laddr net.TCPAddr // server-side address (our end)
	raddr net.TCPAddr // client-side address (their end)
}

// NewConnection creates a Connection from an accepted client FD and its sockaddr.
//
// We capture both addresses at accept time so LocalAddr()/RemoteAddr() don't need
// to make syscalls later. In the real world you could also fetch these anytime:
//   - syscall.Getsockname(fd) → "what is MY address on this socket?"
//   - syscall.Getpeername(fd) → "who is on the OTHER end?"
//
// But calling those on every request is wasteful — the addresses never change for
// a given connection, so storing them once is the right move.
func NewConnection(clientFd int, clientAddr syscall.Sockaddr, serverIP net.IP, serverPort int) *Connection {
	conn := &Connection{
		Fd: clientFd,
		laddr: net.TCPAddr{
			IP:   serverIP,
			Port: serverPort,
		},
	}

	// clientAddr comes from Accept as a generic syscall.Sockaddr interface.
	// For IPv4 connections, the concrete type is *syscall.SockaddrInet4
	// which has Addr [4]byte (raw IP bytes) and Port int.
	if sa, ok := clientAddr.(*syscall.SockaddrInet4); ok {
		conn.raddr = net.TCPAddr{
			IP:   net.IPv4(sa.Addr[0], sa.Addr[1], sa.Addr[2], sa.Addr[3]),
			Port: sa.Port,
		}
	}

	return conn
}

// --- Data transfer (these wrap raw syscalls) ---

// Read wraps read(2) — the fundamental "receive bytes from a file descriptor" syscall.
//
// Real world: Go's net.Conn.Read() parks the calling goroutine on the netpoller until
// data arrives (epoll wakes it up internally). It's blocking from the goroutine's
// perspective but non-blocking at the OS level.
//
// What we do: call the syscall directly. On our non-blocking FDs, this returns EAGAIN
// if no data is available — our event loop handles that by skipping and waiting for
// epoll to notify us again.
func (c *Connection) Read(p []byte) (n int, err error) {
	return syscall.Read(c.Fd, p)
}

// Write wraps write(2) — the fundamental "send bytes to a file descriptor" syscall.
//
// Real world: Go's net.Conn.Write() handles partial writes internally. If the kernel's
// TCP send buffer is full (peer is slow to ACK), write() only accepts a part of your data.
// Go retries in a loop until everything is sent.
//
// What we do: single write, no retry loop. Works fine for our small HTTP responses
// because the kernel send buffer is typically 128KB+ and our response is ~80 bytes.
// A production server would need: while (written < len) { write(remaining) }.
func (c *Connection) Write(p []byte) (n int, err error) {
	return syscall.Write(c.Fd, p)
}

// Close wraps close(2) — tells the kernel we're done with this file descriptor.
//
// What happens inside the kernel:
//  1. Sends TCP FIN to the peer ("I'm done sending")
//  2. Frees the FD number for reuse by future open/socket/accept calls
//  3. Releases kernel-side socket buffers (~128KB send + ~128KB receive)
//
// Real world: Go's net.Conn.Close() also removes the FD from the netpoller and wakes
// any goroutine blocked on Read/Write with a "use of closed connection" error.
// We handle epoll removal separately in the event loop (EpollCtl EPOLL_CTL_DEL).
func (c *Connection) Close() error {
	return syscall.Close(c.Fd)
}

// --- Address methods ---

// LocalAddr returns the server-side address of this connection (our IP:port).
//
// Underlying syscall: getsockname(2) — "what is MY address on this socket?"
// Every TCP connection is a 4-tuple: (localIP:localPort, remoteIP:remotePort).
// The local side is always our server's bind address (127.0.0.1:8080).
//
// We stored it at accept time so this is just a field lookup — no syscall.
func (c *Connection) LocalAddr() net.Addr {
	return &c.laddr
}

// RemoteAddr returns the client-side address of this connection (their IP:port).
//
// Underlying syscall: getpeername(2) — "who is on the OTHER end of this socket?"
// The client's port is an ephemeral port (typically 32768-60999) assigned by their OS.
// This is how you identify which client is talking to you — useful for logging,
// rate limiting, or IP-based access control.
//
// We stored it at accept time from Accept's returned Sockaddr — no syscall needed.
func (c *Connection) RemoteAddr() net.Addr {
	return &c.raddr
}

// --- Deadline methods (no-ops) ---
//
// These are empty stubs. They exist solely to satisfy the net.Conn interface.
//
// WHY THEY DON'T WORK IN OUR SERVER:
// Our FDs are non-blocking. read() never waits — it returns instantly with data or EAGAIN.
// Kernel socket timeouts (SO_RCVTIMEO/SO_SNDTIMEO) only fire when read() is actually
// blocking and waiting for data. If you never block, there's nothing to time out.
//
// TWO APPROACHES TO REAL DEADLINES:
//
// 1. Kernel socket timeouts — setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeval)
//    Sets a per-socket timeout. When read() blocks longer than the duration, kernel
//    returns EAGAIN. Only works with BLOCKING FDs — useless for non-blocking event loops.
//
// 2. Epoll-based deadlines (what Go's netpoller does):
//    - Each connection stores a deadline timestamp in a TIMER HEAP (min-heap sorted
//      by earliest expiry)
//    - Before each epoll_wait, compute: timeout = earliest_deadline - now
//    - Pass that as epoll_wait's timeout argument (instead of our -1 = forever)
//    - When epoll_wait returns due to timeout (not I/O), walk the heap, find all
//      expired connections, and wake them with os.ErrDeadlineExceeded
//    - Every SetReadDeadline call inserts/updates an entry in the heap
//
//    This is the real solution — epoll_wait's timeout parameter becomes the deadline
//    mechanism. But implementing it requires a timer heap, heap maintenance on every
//    deadline set/clear, and timeout-vs-I/O disambiguation after each epoll_wait.
//    That's basically reimplementing Go's netpoller scheduler — way beyond our scope.

func (c *Connection) SetDeadline(t time.Time) error      { return nil }
func (c *Connection) SetReadDeadline(t time.Time) error   { return nil }
func (c *Connection) SetWriteDeadline(t time.Time) error  { return nil }
