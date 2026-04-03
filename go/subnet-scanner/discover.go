package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/mdlayher/arp"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// CIDRToIPs expands a CIDR notation (e.g. "192.168.1.0/24") into all usable host IPs.
//
// How CIDR bit masking works:
//   - A /24 mask means the first 24 bits identify the network, last 8 bits are hosts.
//   - Mask = 0xFFFFFF00, so ^mask = 0x000000FF gives us 255 host addresses.
//   - We skip the first (network address, e.g. 192.168.1.0) and last (broadcast, e.g. 192.168.1.255).
//   - Everything in between is a usable host: 192.168.1.1 through 192.168.1.254.
func CIDRToIPs(cidr string) ([]string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}

	// We need IPv4 to pack into a uint32 for efficient iteration.
	ip4 := ipNet.IP.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("only IPv4 is supported, got %q", cidr)
	}

	// Convert IP and mask to uint32 for bit arithmetic.
	// An IPv4 address is 4 bytes — packing into uint32 lets us simply increment
	// to walk through every address in the range.
	mask := binary.BigEndian.Uint32(ipNet.Mask)
	start := binary.BigEndian.Uint32(ip4)

	// Broadcast = network address OR'd with the inverted mask.
	// Example: 192.168.1.0 | 0x000000FF = 192.168.1.255
	broadcast := start | ^mask

	// Pre-allocate: we know exactly how many hosts there are.
	count := int(broadcast - start - 1)
	if count <= 0 {
		return nil, fmt.Errorf("subnet %q has no usable host addresses", cidr)
	}
	ips := make([]string, 0, count)

	for addr := start + 1; addr < broadcast; addr++ {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, addr)
		ips = append(ips, net.IP(b).String())
	}

	return ips, nil
}

// DiscoverDevices finds live hosts on the given subnet using multiple strategies.
//
// Fallback chain:
//  1. ICMP ping sweep — standard approach, requires root/CAP_NET_RAW
//  2. ARP scan — Layer 2 discovery, also needs root but works when ICMP is firewalled
//  3. TCP connect probe — no root needed, tries common ports to detect hosts
//
// Each method is attempted only if the previous one fails entirely (socket error).
// If a method succeeds but finds zero hosts, that's a valid result (empty network).
func DiscoverDevices(ctx context.Context, subnet string) ([]string, error) {
	allIPs, err := CIDRToIPs(subnet)
	if err != nil {
		return nil, err
	}

	fmt.Printf("Expanded %s into %d host addresses\n", subnet, len(allIPs))

	// Strategy 1: ICMP Echo (ping)
	liveIPs, err := icmpSweep(ctx, allIPs)
	if err == nil {
		return liveIPs, nil
	}
	fmt.Fprintf(os.Stderr, "ICMP sweep failed: %v\n", err)

	// Strategy 2: ARP (Layer 2)
	liveIPs, err = arpScan(ctx, subnet, allIPs)
	if err == nil {
		return liveIPs, nil
	}
	fmt.Fprintf(os.Stderr, "ARP scan failed: %v\n", err)

	// Strategy 3: TCP connect probe (slowest but works without root)
	fmt.Println("Falling back to TCP connect probes...")
	return tcpProbe(ctx, allIPs), nil
}

// icmpSweep sends ICMP Echo Requests to all IPs and collects replies.
//
// Architecture: "send all, then listen" pattern.
//   - One goroutine sends pings sequentially with a small delay to avoid flooding.
//   - The main goroutine reads all incoming replies from the shared socket.
//   - We match replies by checking the ICMP Echo ID (our PID) and source IP.
//
// Why not one goroutine per ping?
//   - All goroutines would share the same ICMP socket.
//   - ReadFrom is not safe to call concurrently on a single connection.
//   - The send-all-then-listen pattern avoids this entirely.
//
// Requires root/CAP_NET_RAW because ICMP uses raw sockets (ip4:icmp).
func icmpSweep(ctx context.Context, ips []string) ([]string, error) {
	// "ip4:icmp" opens a raw ICMP socket. "0.0.0.0" = listen on all interfaces.
	// This is a privileged operation — unprivileged users will get "permission denied".
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, fmt.Errorf("cannot open ICMP socket (need root/CAP_NET_RAW): %w", err)
	}
	defer conn.Close()

	// Track which IPs have replied so we don't double-count.
	replied := make(map[string]bool, len(ips))
	pid := os.Getpid() & 0xffff // ICMP Echo ID is 16 bits; use our PID to identify our packets.

	// Sender goroutine: fires off all pings sequentially.
	// 1ms delay between pings prevents overwhelming the local network stack
	// and avoids packet drops from the kernel's rate limiter.
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		for seq, ip := range ips {
			select {
			case <-ctx.Done():
				return
			default:
			}

			dst := net.ParseIP(ip)
			if dst == nil {
				continue
			}

			// ICMP Echo Request: Type 8, Code 0.
			// The ID field (our PID) lets us match replies to our process.
			// The Seq field lets us identify which host this ping was for.
			msg := icmp.Message{
				Type: ipv4.ICMPTypeEcho,
				Code: 0,
				Body: &icmp.Echo{
					ID:   pid,
					Seq:  seq,
					Data: []byte("SCAN"),
				},
			}

			b, err := msg.Marshal(nil)
			if err != nil {
				continue
			}

			conn.WriteTo(b, &net.IPAddr{IP: dst})
			time.Sleep(time.Millisecond)
		}
	}()

	// Receiver: read replies until overall timeout expires.
	// We use a 3-second window which is generous — LAN replies typically arrive in < 1ms.
	// The 100ms read deadline on each ReadFrom call lets us check the overall timeout
	// and context cancellation frequently.
	var liveIPs []string
	overallTimeout := time.After(3 * time.Second)
	buf := make([]byte, 1500) // Standard MTU size buffer

	for {
		select {
		case <-overallTimeout:
			return liveIPs, nil
		case <-ctx.Done():
			return liveIPs, ctx.Err()
		default:
		}

		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			// Read timeout is expected — just loop and check the overall deadline.
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			continue
		}

		// Parse the ICMP message. Protocol number 1 = ICMP.
		parsed, err := icmp.ParseMessage(1, buf[:n])
		if err != nil {
			continue
		}

		// Only care about Echo Replies (Type 0) that match our ID.
		if parsed.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echoReply, ok := parsed.Body.(*icmp.Echo)
		if !ok || echoReply.ID != pid {
			continue
		}

		peerIP := peer.String()
		if !replied[peerIP] {
			replied[peerIP] = true
			liveIPs = append(liveIPs, peerIP)
		}
	}
}

// arpScan discovers hosts using ARP (Address Resolution Protocol) at Layer 2.
//
// Why ARP is powerful for LAN scanning:
//   - ARP operates at the data link layer (Layer 2), below IP.
//   - Every device on an Ethernet LAN must respond to ARP — it's how Ethernet works.
//   - Firewalls can't block ARP without breaking networking entirely.
//   - We also get the MAC address, which identifies the hardware manufacturer.
//
// How it works:
//   - We send "Who has <IP>? Tell <our MAC>" to the broadcast address.
//   - If the host exists, it replies with its MAC address.
//   - The mdlayher/arp package's Resolve() handles this request-reply cycle.
//
// Limitation: ARP only works on the local LAN segment (same broadcast domain).
func arpScan(ctx context.Context, subnet string, ips []string) ([]string, error) {
	iface, err := FindInterfaceForSubnet(subnet)
	if err != nil {
		return nil, fmt.Errorf("finding interface for %s: %w", subnet, err)
	}

	client, err := arp.Dial(iface)
	if err != nil {
		return nil, fmt.Errorf("opening ARP socket on %s: %w", iface.Name, err)
	}
	defer client.Close()

	var liveIPs []string

	// ARP client is not thread-safe, so we scan sequentially.
	// With a 100ms timeout per host, a /24 takes ~25s worst case.
	// In practice, live hosts reply in < 1ms so it's much faster.
	for _, ip := range ips {
		select {
		case <-ctx.Done():
			return liveIPs, ctx.Err()
		default:
		}

		addr, err := netip.ParseAddr(ip)
		if err != nil {
			continue
		}

		client.SetDeadline(time.Now().Add(100 * time.Millisecond))

		hwAddr, err := client.Resolve(addr)
		if err == nil {
			liveIPs = append(liveIPs, ip)
			fmt.Printf("  %s is alive (MAC: %s)\n", ip, hwAddr)
		}
	}

	return liveIPs, nil
}

// tcpProbe discovers live hosts by attempting TCP connections to common ports.
//
// This is the fallback when we don't have root privileges for ICMP or ARP.
// For each IP, we try connecting to a few well-known ports. If any port accepts
// the connection (completes TCP handshake), the host is alive.
//
// Trade-offs:
//   - No root needed (uses regular TCP sockets)
//   - Slower than ICMP/ARP (must attempt full TCP handshake per port)
//   - Will miss hosts that have all probed ports closed/filtered
//   - Concurrent probing with a semaphore to avoid socket exhaustion
func tcpProbe(ctx context.Context, ips []string) []string {
	probeports := []int{80, 443, 22, 8080, 3389}

	var (
		mu      sync.Mutex
		liveIPs []string
		wg      sync.WaitGroup
	)

	// Semaphore limits concurrent TCP connections.
	// Too many simultaneous dials can exhaust file descriptors
	// or trigger kernel connection tracking limits.
	sem := make(chan struct{}, 100)

	for _, ip := range ips {
		select {
		case <-ctx.Done():
			break
		default:
		}

		wg.Add(1)
		sem <- struct{}{}

		go func(targetIP string) {
			defer wg.Done()
			defer func() { <-sem }()

			for _, port := range probeports {
				select {
				case <-ctx.Done():
					return
				default:
				}

				addr := net.JoinHostPort(targetIP, fmt.Sprint(port))
				conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
				if err == nil {
					conn.Close()
					mu.Lock()
					liveIPs = append(liveIPs, targetIP)
					mu.Unlock()
					return
				}
			}
		}(ip)
	}

	wg.Wait()
	return liveIPs
}
