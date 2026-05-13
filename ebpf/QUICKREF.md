## Quick Reference

### Build & Run Commands
```bash
make build              # Build everything
sudo ./ebpf-hello       # Run the program
sudo ./test.sh          # Run automated test
```

### View eBPF Output
```bash
# In a separate terminal
sudo cat /sys/kernel/debug/tracing/trace_pipe
```

### Trigger Events
```bash
# Any command in another terminal will trigger execve
ls
echo "hello"
date
```

### Rebuild from Scratch
```bash
make distclean          # Remove everything including vmlinux.h
make build              # Rebuild everything
```

### File Descriptions

| File | Purpose |
|------|---------|
| `ebpf.c` | eBPF C program - attaches to execve tracepoint |
| `main.go` | Go loader - loads and manages eBPF program |
| `vmlinux.h` | Kernel type definitions (generated once) |
| `bpf_bpfel_x86.go` | Generated Go bindings (auto-created) |
| `ebpf-hello` | Final executable binary |

### Key Concepts

**eBPF Program**: Runs in kernel space, triggered by syscalls
**Go Loader**: Loads eBPF into kernel, runs in user space
**Tracepoint**: Kernel hook point (sys_enter_execve)
**bpf_printk()**: eBPF function to print to trace_pipe
**bpf2go**: Tool to generate Go bindings from C code
**vmlinux.h**: All kernel types in one header (CO-RE)

### Program Flow
```
1. Go program starts → 2. Loads eBPF bytecode → 3. Kernel verifies
         ↓
4. Attaches to tracepoint → 5. Waits for execve → 6. Prints "Hello World"
```
