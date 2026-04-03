# Subnet Scanner

Scan devices on your local network and find open ports — no nmap required.

A Go CLI tool that:

1. Auto-detects your local subnet (or accepts `--subnet`)
2. Discovers live hosts using ICMP ping, ARP scan, or TCP probes (fallback chain)
3. Runs concurrent port scans via a worker pool
4. Outputs clean, structured results grouped by host

## Usage

```bash
# Auto-detect subnet, scan default ports (22, 80, 443, 8080)
sudo go run .

# Specify subnet and ports
sudo go run . --subnet 192.168.1.0/24 --ports 22,80,443,3000,8080

# Tune concurrency and timeout
sudo go run . --workers 100 --timeout 1s
```

Note: `sudo` is needed for ICMP/ARP discovery. Without root, falls back to TCP probes.

## Flags

| Flag        | Default            | Description                              |
|-------------|--------------------|------------------------------------------|
| `--subnet`  | auto-detect        | Target subnet in CIDR notation           |
| `--ports`   | `22,80,443,8080`   | Comma-separated ports to scan            |
| `--workers` | `50`               | Concurrent scan workers                  |
| `--timeout` | `500ms`            | TCP connect timeout per probe            |

## Architecture

```
main.go        CLI flags, signal handling, output formatting
├── network.go    auto-detect local subnet from interfaces
├── discover.go   host discovery (ICMP → ARP → TCP fallback)
├── scanner.go    TCP connect port scanning
└── worker.go     concurrent worker pool with context support
```

## Discovery Strategies

1. **ICMP Ping Sweep** — sends Echo Requests, fastest method (needs root)
2. **ARP Scan** — Layer 2 discovery, works even when ICMP is firewalled (needs root)
3. **TCP Connect Probe** — tries common ports, no root needed (slowest)
