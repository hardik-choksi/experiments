package main

import (
	"fmt"
	"net"
)

// DetectSubnet finds the local machine's primary subnet by inspecting network interfaces.
//
// Strategy:
//   - Iterate all network interfaces looking for one that is UP and not loopback.
//   - From its addresses, pick the first IPv4 address with a subnet mask.
//   - Compute the network address by AND-ing the IP with the mask.
//   - Return in CIDR notation (e.g. "192.168.1.0/24").
//
// This lets users run the scanner without specifying --subnet manually.
func DetectSubnet() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("listing interfaces: %w", err)
	}

	for _, iface := range ifaces {
		// Skip interfaces that are down or loopback (lo).
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			// Interface addresses come as *net.IPNet (with mask) or *net.IPAddr (without).
			// We need the mask to determine the subnet, so only *net.IPNet works.
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			// Skip IPv6 and loopback addresses.
			if ipNet.IP.To4() == nil || ipNet.IP.IsLoopback() {
				continue
			}

			// Compute the network address: IP AND mask.
			// E.g. 192.168.1.42 AND 255.255.255.0 = 192.168.1.0
			ones, _ := ipNet.Mask.Size()
			networkIP := ipNet.IP.Mask(ipNet.Mask)

			return fmt.Sprintf("%s/%d", networkIP.String(), ones), nil
		}
	}

	return "", fmt.Errorf("no suitable network interface found (need a non-loopback IPv4 interface that is UP)")
}

// FindInterfaceForSubnet returns the network interface whose IP falls within the given subnet.
// This is needed for ARP scanning, which must bind to a specific interface.
//
// For example, if subnet is "192.168.1.0/24" and eth0 has IP 192.168.1.42,
// this returns the eth0 interface.
func FindInterfaceForSubnet(subnet string) (*net.Interface, error) {
	_, targetNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", subnet, err)
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("listing interfaces: %w", err)
	}

	for i := range ifaces {
		iface := &ifaces[i]
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			// Check if this interface's IP is within our target subnet.
			if targetNet.Contains(ipNet.IP) {
				return iface, nil
			}
		}
	}

	return nil, fmt.Errorf("no interface found with an IP in subnet %s", subnet)
}
