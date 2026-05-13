.PHONY: all generate build run clean vmlinux

all: generate build

# Generate vmlinux.h from kernel BTF (only needed once)
vmlinux:
	@if [ ! -f vmlinux.h ]; then \
		echo "Generating vmlinux.h..."; \
		if command -v bpftool > /dev/null; then \
			bpftool btf dump file /sys/kernel/btf/vmlinux format c > vmlinux.h; \
		else \
			BPFTOOL=$$(find /usr/lib/linux-* -name bpftool 2>/dev/null | head -1); \
			if [ -n "$$BPFTOOL" ]; then \
				$$BPFTOOL btf dump file /sys/kernel/btf/vmlinux format c > vmlinux.h; \
			else \
				echo "Error: bpftool not found. Please install linux-tools."; \
				exit 1; \
			fi; \
		fi; \
	fi

# Generate Go bindings from eBPF C code
generate: vmlinux
	go generate ./...

# Build the Go program
build: generate
	go build -o ebpf-hello main.go bpf_bpfel_x86.go

# Run the program (requires root)
run: build
	sudo ./ebpf-hello

# Clean generated files and binaries
clean:
	rm -f ebpf-hello bpf_bpfel*.go bpf_bpfel*.o bpf_bpfeb*.go bpf_bpfeb*.o

# Clean everything including vmlinux.h
distclean: clean
	rm -f vmlinux.h

# Setup dependencies
setup:
	go mod download
	go install github.com/cilium/ebpf/cmd/bpf2go@latest
