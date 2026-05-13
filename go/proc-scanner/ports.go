// ports.go detects listening TCP/UDP ports for processes by correlating
// /proc/<pid>/net/{tcp,tcp6,udp,udp6} socket inodes with /proc/<pid>/fd/*
// symlink targets of the form "socket:[<inode>]".
//
// This avoids shelling out to ss/netstat/lsof and works with the same
// /proc mount the rest of the discovery pipeline relies on. Processes
// in their own network namespace (e.g. containers) are handled correctly
// because we read /proc/<pid>/net/* from the target PID's perspective.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Listener describes a single listening socket owned by a process.
type Listener struct {
	Protocol string // "tcp", "tcp6", "udp", "udp6"
	Address  string // local bind address (e.g. "0.0.0.0", "::", "127.0.0.1")
	Port     uint16
}

func (l Listener) String() string {
	return fmt.Sprintf("%s://%s:%d", l.Protocol, l.Address, l.Port)
}

// tcpStateListen is the /proc/net/tcp hex state value for LISTEN.
const tcpStateListen = "0A"

// ListListeners returns every listening TCP socket and bound UDP socket
// owned by the given PID. It reads the process's own network namespace
// view via /proc/<pid>/net/*, so containerized processes report the
// ports they actually expose inside their netns.
//
// Returns an empty slice (no error) when the process has no listening
// sockets or when /proc entries are unreadable — the common case for
// short-lived or privileged processes we can't inspect.
func ListListeners(pid int32) []Listener {
	inodes := readSocketInodes(pid)
	if len(inodes) == 0 {
		return nil
	}

	var out []Listener
	for _, proto := range []string{"tcp", "tcp6", "udp", "udp6"} {
		entries := parseProcNet(pid, proto)
		for _, e := range entries {
			if !inodes[e.inode] {
				continue
			}
			// TCP: only LISTEN state. UDP has no LISTEN concept;
			// a bound UDP socket is effectively "listening".
			if (proto == "tcp" || proto == "tcp6") && e.state != tcpStateListen {
				continue
			}
			out = append(out, Listener{
				Protocol: proto,
				Address:  e.addr,
				Port:     e.port,
			})
		}
	}
	return out
}

// readSocketInodes returns the set of socket inodes owned by the given
// PID by walking /proc/<pid>/fd and reading symlinks of the form
// "socket:[<inode>]".
func readSocketInodes(pid int32) map[uint64]bool {
	fdDir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil
	}

	inodes := make(map[uint64]bool, len(entries))
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err != nil {
			continue
		}
		// Expected form: "socket:[12345]"
		if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
			continue
		}
		raw := target[len("socket:[") : len(target)-1]
		ino, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			continue
		}
		inodes[ino] = true
	}
	return inodes
}

// procNetEntry is a parsed row from /proc/<pid>/net/{tcp,tcp6,udp,udp6}.
type procNetEntry struct {
	addr  string
	port  uint16
	state string
	inode uint64
}

// parseProcNet reads /proc/<pid>/net/<proto> and returns each row's
// local address, port, state, and socket inode.
//
// Line format (header first, then one entry per line):
//
//	sl  local_address rem_address   st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode ...
//	 0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000  0 12345 ...
func parseProcNet(pid int32, proto string) []procNetEntry {
	path := fmt.Sprintf("/proc/%d/net/%s", pid, proto)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return nil
	}

	isV6 := proto == "tcp6" || proto == "udp6"
	out := make([]procNetEntry, 0, len(lines)-1)

	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		addr, port, ok := parseHexAddrPort(fields[1], isV6)
		if !ok {
			continue
		}
		ino, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}

		out = append(out, procNetEntry{
			addr:  addr,
			port:  port,
			state: fields[3],
			inode: ino,
		})
	}
	return out
}

// parseHexAddrPort parses entries like "0100007F:1F90" (IPv4) or
// "00000000000000000000000000000000:1F90" (IPv6). The address portion
// is in little-endian hex; the port is big-endian hex.
func parseHexAddrPort(s string, isV6 bool) (string, uint16, bool) {
	rawAddr, rawPort, ok := strings.Cut(s, ":")
	if !ok {
		return "", 0, false
	}

	port64, err := strconv.ParseUint(rawPort, 16, 16)
	if err != nil {
		return "", 0, false
	}

	var addr string
	if isV6 {
		if len(rawAddr) != 32 {
			return "", 0, false
		}
		addr = formatIPv6(rawAddr)
	} else {
		if len(rawAddr) != 8 {
			return "", 0, false
		}
		a, ok := formatIPv4(rawAddr)
		if !ok {
			return "", 0, false
		}
		addr = a
	}
	return addr, uint16(port64), true
}

// formatIPv4 decodes a little-endian hex IPv4 address like "0100007F"
// into "127.0.0.1".
func formatIPv4(hex string) (string, bool) {
	b := make([]byte, 4)
	for i := range 4 {
		v, err := strconv.ParseUint(hex[i*2:i*2+2], 16, 8)
		if err != nil {
			return "", false
		}
		// kernel stores as LE u32: byte 0 is LSB
		b[3-i] = byte(v) // little-endian → reverse
	}
	return fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3]), true
}

// formatIPv6 decodes a 32-char hex IPv6 address from /proc/net/tcp6.
// The kernel writes four 32-bit little-endian words, so each 8-char
// group must be byte-reversed before being laid out as standard IPv6.
func formatIPv6(hex string) string {
	bytes := make([]byte, 16)
	for word := range 4 {
		for i := range 4 {
			v, err := strconv.ParseUint(hex[word*8+i*2:word*8+i*2+2], 16, 8)
			if err != nil {
				return ""
			}
			bytes[word*4+(3-i)] = byte(v)
		}
	}

	// Render as eight colon-separated 16-bit groups, stripping leading zeros.
	parts := make([]string, 8)
	for i := range 8 {
		parts[i] = fmt.Sprintf("%x", uint16(bytes[i*2])<<8|uint16(bytes[i*2+1]))
	}
	return strings.Join(parts, ":")
}
