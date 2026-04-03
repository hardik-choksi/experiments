package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func main() {
	// CLI flags — all the knobs the user can turn.
	subnetFlag := flag.String("subnet", "", "Target subnet in CIDR notation (e.g. 192.168.1.0/24). Auto-detected if omitted.")
	portsFlag := flag.String("ports", "22,80,443,8080", "Comma-separated list of ports to scan")
	workersFlag := flag.Int("workers", 50, "Number of concurrent scan workers")
	timeoutFlag := flag.Duration("timeout", 500*time.Millisecond, "TCP connection timeout per port probe")
	flag.Parse()

	// signal.NotifyContext creates a context that gets cancelled when the
	// process receives SIGINT (Ctrl+C). This propagates through to all
	// goroutines that accept context — scanners, workers, discovery — giving
	// us graceful shutdown for free.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Determine target subnet: use the flag if provided, otherwise auto-detect.
	subnet := *subnetFlag
	if subnet == "" {
		detected, err := DetectSubnet()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: could not auto-detect subnet: %v\n", err)
			fmt.Fprintf(os.Stderr, "Hint: specify --subnet 192.168.1.0/24\n")
			os.Exit(1)
		}
		subnet = detected
		fmt.Printf("Auto-detected subnet: %s\n", subnet)
	}

	ports, err := parsePorts(*portsFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Phase 1: Host Discovery
	fmt.Printf("\n--- Host Discovery ---\n")
	fmt.Printf("Target: %s\n", subnet)

	ips, err := DiscoverDevices(ctx, subnet)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Discovery failed: %v\n", err)
		os.Exit(1)
	}

	if len(ips) == 0 {
		fmt.Println("No live hosts found.")
		return
	}

	sort.Strings(ips)
	fmt.Printf("\nFound %d live host(s):\n", len(ips))
	for _, ip := range ips {
		fmt.Printf("  %s\n", ip)
	}

	// Phase 2: Port Scanning
	totalProbes := len(ips) * len(ports)
	fmt.Printf("\n--- Port Scanning ---\n")
	fmt.Printf("Scanning %d port(s) on %d host(s) = %d probes [%d workers, %s timeout]\n",
		len(ports), len(ips), totalProbes, *workersFlag, *timeoutFlag)

	// Worker pool setup.
	// Buffer size = number of workers so producers don't block immediately.
	jobs := make(chan Job, *workersFlag)
	results := make(chan Result, *workersFlag)

	var wg sync.WaitGroup
	for i := 0; i < *workersFlag; i++ {
		wg.Add(1)
		go Worker(ctx, jobs, results, &wg, *timeoutFlag)
	}

	// Producer: enqueue all IP:port combinations as jobs.
	// Runs in a goroutine so the main goroutine can start collecting results.
	go func() {
		defer close(jobs)
		for _, ip := range ips {
			for _, port := range ports {
				select {
				case <-ctx.Done():
					return
				case jobs <- Job{IP: ip, Port: port}:
				}
			}
		}
	}()

	// Closer: waits for all workers to finish, then closes the results channel.
	// This signals the main goroutine's range loop to exit.
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results, grouped by IP for clean output.
	openPorts := make(map[string][]int)
	for res := range results {
		if res.Open {
			openPorts[res.IP] = append(openPorts[res.IP], res.Port)
		}
	}

	// Display results sorted by IP, with ports sorted within each host.
	fmt.Printf("\n--- Results ---\n")
	if len(openPorts) == 0 {
		fmt.Println("No open ports found.")
		return
	}

	sortedIPs := make([]string, 0, len(openPorts))
	for ip := range openPorts {
		sortedIPs = append(sortedIPs, ip)
	}
	sort.Strings(sortedIPs)

	for _, ip := range sortedIPs {
		p := openPorts[ip]
		sort.Ints(p)
		fmt.Printf("  %-16s -> %v\n", ip, p)
	}
}

// parsePorts converts a comma-separated port string like "22,80,443" into a slice of ints.
// Validates that each port is in the valid TCP range (1-65535).
func parsePorts(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	ports := make([]int, 0, len(parts))

	for _, p := range parts {
		p = strings.TrimSpace(p)
		port, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid port %q: %w", p, err)
		}
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("port %d out of range (1-65535)", port)
		}
		ports = append(ports, port)
	}

	return ports, nil
}
