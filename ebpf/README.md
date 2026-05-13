# eBPF Hello World

A simple eBPF program that prints "Hello World" whenever the `execve` syscall is called.

## Prerequisites

### Ubuntu/Debian
```bash
sudo apt-get install -y clang llvm libbpf-dev linux-headers-$(uname -r) linux-tools-generic
```

### Fedora/RHEL
```bash
sudo dnf install -y clang llvm libbpf-devel kernel-devel bpftool
```

## Setup

First, generate the `vmlinux.h` header file (only needed once):

```bash
# Find your bpftool location
bpftool btf dump file /sys/kernel/btf/vmlinux format c > vmlinux.h

# If bpftool command not found, use the full path:
# Ubuntu example:
sudo /usr/lib/linux-hwe-6.17-tools-6.17.0-20/bpftool btf dump file /sys/kernel/btf/vmlinux format c > vmlinux.h
```

Install Go dependencies:
```bash
make setup
```

## Build and Run

1. Build the program:
```bash
make build
```

2. Run the program (requires root):
```bash
sudo ./ebpf-hello
```

3. In another terminal, view the kernel trace output:
```bash
sudo cat /sys/kernel/debug/tracing/trace_pipe
```

4. Trigger the `execve` syscall by running any command in a third terminal:
```bash
ls
echo "test"
# Any command will trigger execve and print "Hello World"
```

You should see output like:
```
<...>-12345 [000] .... 123456.123456: bpf_trace_printk: Hello World from eBPF! execve syscall called
```

## Project Structure

- `ebpf.c`: The eBPF C program that attaches to the `sys_enter_execve` tracepoint
- `main.go`: The Go program that:
  - Uses `bpf2go` to generate Go bindings from the C code
  - Loads the eBPF program into the kernel
  - Attaches it to the tracepoint
  - Keeps it running until interrupted
- `vmlinux.h`: Kernel type definitions (generated from your running kernel)
- `bpf_bpfel_x86.go`: Auto-generated Go bindings (created during build)

## How It Works

1. The eBPF program is written in C and compiled to eBPF bytecode
2. The `bpf2go` tool generates Go code that embeds the bytecode and provides type-safe access
3. The Go program loads the eBPF bytecode into the kernel
4. The program is attached to the `sys_enter_execve` tracepoint
5. Every time any process calls `execve`, the eBPF program runs and prints to the trace pipe

## Clean Up

```bash
make clean
```

## Troubleshooting

If you get "permission denied" errors, make sure to run with `sudo`.

If the build fails with missing headers, ensure you have the kernel headers installed:
```bash
sudo apt-get install -y linux-headers-$(uname -r)
```
