package main

import (
	"encoding/binary"
	"fmt"
	"net"
)

func BinaryDemo() {
	cidr := "192.168.4.104/24"
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(fmt.Sprintf("invalid CIDR %q: %v", cidr, err))
	}

	networkIp4 := ipNet.IP.To4()
	ip4 := ip.To4()

	networkIpN := binary.BigEndian.Uint32(networkIp4)
	ipN := binary.BigEndian.Uint32(ip4)

	fmt.Printf("CIDR: %s\n", cidr)
	fmt.Printf("IP: %s\n", ip4)
	fmt.Printf("Network IP: %s\n", networkIp4)
	fmt.Printf("IP (uint32): %d\n", ipN)
	fmt.Printf("Network IP (uint32): %d\n", networkIpN)

	ipN++

	newIp := make(net.IP, 4)

	binary.BigEndian.PutUint32(newIp, ipN)
	fmt.Printf("New IP: %s\n", newIp)
}
