# Project Summary

## What We Built

A complete eBPF "Hello World" program with:

1. **eBPF C Program** (`ebpf.c`)
   - Attaches to the `sys_enter_execve` tracepoint
   - Prints "Hello World from eBPF! execve syscall called" to the kernel trace pipe
   - Uses modern libbpf and CO-RE (Compile Once, Run Everywhere)

2. **Go Loader Program** (`main.go`)
   - Uses the cilium/ebpf library (industry standard)
   - Auto-generates Go bindings using bpf2go
   - Loads and attaches the eBPF program to the kernel
   - Handles cleanup gracefully

## Key Files

```
ebpf/
├── ebpf.c              # eBPF C program source
├── main.go             # Go loader program
├── go.mod              # Go dependencies
├── Makefile            # Build automation
├── README.md           # User documentation
├── test.sh             # Test script
├── .gitignore          # Git ignore rules
├── vmlinux.h           # Kernel type definitions (generated)
├── bpf_bpfel_x86.go    # Generated Go bindings (build artifact)
├── bpf_bpfel_x86.o     # Compiled eBPF bytecode (build artifact)
└── ebpf-hello          # Final binary (build artifact)
```

## How to Use

### First Time Setup
```bash
cd /home/hardik/experiments/ebpf
make setup              # Download Go dependencies
```

### Build
```bash
make build              # Compiles everything
```

### Run
Terminal 1 (run the program):
```bash
sudo ./ebpf-hello
```

Terminal 2 (view output):
```bash
sudo cat /sys/kernel/debug/tracing/trace_pipe
```

Terminal 3 (trigger execve):
```bash
ls
echo "test"
# Any command triggers execve and prints "Hello World"
```

### Test
```bash
sudo ./test.sh         # Automated test
```

## Technical Details

### eBPF Program Flow
1. Compiled to eBPF bytecode using Clang/LLVM
2. Bytecode is embedded into Go binary via bpf2go
3. Go program loads bytecode into kernel using eBPF syscalls
4. Kernel verifies the bytecode for safety
5. Program is attached to the sys_enter_execve tracepoint
6. Every execve syscall triggers the eBPF program
7. bpf_printk() writes to /sys/kernel/debug/tracing/trace_pipe

### Why This Approach?
- **CO-RE (Compile Once, Run Everywhere)**: Uses vmlinux.h from your kernel
- **Type-safe Go bindings**: bpf2go generates Go structs matching eBPF types
- **Production-ready**: Uses cilium/ebpf, the most mature Go eBPF library
- **Minimal dependencies**: Just Clang, libbpf headers, and Go

### Dependencies
- **clang/llvm**: Compiles C to eBPF bytecode
- **libbpf-dev**: eBPF helper headers
- **linux-headers**: Kernel header files
- **bpftool**: Generates vmlinux.h from kernel BTF
- **cilium/ebpf**: Go library for loading/managing eBPF programs

## Makefile Targets

- `make vmlinux` - Generate vmlinux.h from kernel (auto-detected)
- `make generate` - Generate Go bindings from C code
- `make build` - Build the complete program
- `make run` - Build and run (requires sudo)
- `make clean` - Remove generated files and binary
- `make distclean` - Also remove vmlinux.h
- `make setup` - Install Go dependencies

## Troubleshooting

**"permission denied"**: Run with sudo
**"bpftool not found"**: Install linux-tools-generic
**"vmlinux.h not found"**: Run `make vmlinux` first
**Linter errors about .c file**: Ignore - this is expected, build works fine

## Next Steps

To extend this:
1. Add eBPF maps to share data between kernel and userspace
2. Parse execve arguments (filename, argv, envp)
3. Filter by process name or UID
4. Use ring buffer for high-performance data transfer
5. Add multiple tracepoints or kprobes
