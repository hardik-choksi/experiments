package main

import (
	"context"
	"fmt"
	"net"
	"time"
)

// ScanPort attempts a TCP connect to ip:port with the given timeout.
//
// How TCP connect scanning works:
//  1. We initiate a full TCP three-way handshake: SYN → SYN-ACK → ACK
//  2. If the handshake completes within the timeout → port is OPEN
//  3. If we get RST (connection refused) → port is CLOSED
//  4. If we get no response (timeout) → port is FILTERED (firewall dropping packets)
//
// We use net.Dialer.DialContext which respects context cancellation.
// If the user hits Ctrl+C, in-flight dials are cancelled immediately.
//
// Trade-offs of TCP connect scanning:
//   - Reliable: uses the kernel's TCP stack, no raw sockets needed
//   - Detectable: completes the full handshake, leaving logs on the target
//   - Slower: each probe takes up to `timeout` duration
//
// Phase 3 will implement SYN scanning (half-open) which sends only the SYN,
// checks for SYN-ACK, and never completes the handshake — faster and stealthier.
func ScanPort(ctx context.Context, ip string, port int, timeout time.Duration) bool {
	address := net.JoinHostPort(ip, fmt.Sprint(port))

	// net.Dialer with a timeout performs the TCP handshake.
	// DialContext additionally hooks into the context, so cancellation
	// (e.g. from Ctrl+C / SIGINT) aborts the dial immediately.
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
