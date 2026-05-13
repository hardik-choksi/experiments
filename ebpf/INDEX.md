# eBPF Hello World - Complete Index

## 📦 Project Overview
A production-ready eBPF "Hello World" program that prints a message whenever the `execve` syscall is called. Written in C (eBPF) with a Go loader using the cilium/ebpf library.

---

## 📁 File Structure

### Source Files
| File | Size | Description |
|------|------|-------------|
| `ebpf.c` | 277 B | eBPF C program that attaches to execve tracepoint |
| `main.go` | 1.3 KB | Go program that loads and manages the eBPF program |

### Generated Files (Build Artifacts)
| File | Size | Description |
|------|------|-------------|
| `vmlinux.h` | 3.3 MB | Kernel type definitions (generated once from BTF) |
| `bpf_bpfel_x86.go` | 2.5 KB | Auto-generated Go bindings from C code |
| `bpf_bpfel_x86.o` | 2.0 KB | Compiled eBPF bytecode (little-endian x86) |
| `ebpf-hello` | 4.7 MB | Final executable binary |

### Configuration & Build
| File | Size | Description |
|------|------|-------------|
| `go.mod` | 213 B | Go module dependencies |
| `go.sum` | 1.4 KB | Go dependency checksums |
| `Makefile` | 1.2 KB | Build automation with targets |
| `.gitignore` | 270 B | Git ignore patterns |

### Documentation
| File | Size | Description |
|------|------|-------------|
| `README.md` | 2.4 KB | User guide with setup and usage instructions |
| `SUMMARY.md` | 3.5 KB | Technical summary and project overview |
| `QUICKREF.md` | 1.5 KB | Quick reference card for common commands |
| `ARCHITECTURE.md` | 7.5 KB | Architecture diagrams and data flow |
| `INDEX.md` | (this file) | Complete project index |

### Scripts
| File | Size | Description |
|------|------|-------------|
| `test.sh` | 1.2 KB | Automated test script |

---

## 📚 Documentation Guide

### For First-Time Users
1. Start with `README.md` - Setup and basic usage
2. Run `make build` then `sudo ./ebpf-hello`
3. Check `QUICKREF.md` for common commands

### For Understanding the Code
1. Read `ebpf.c` - Simple 11-line eBPF program
2. Read `main.go` - 49-line Go loader
3. Review `ARCHITECTURE.md` - System architecture

### For Technical Details
1. `SUMMARY.md` - Complete technical overview
2. `ARCHITECTURE.md` - Data flow and component interaction
3. `Makefile` - Build process details

---

## 🚀 Quick Start

```bash
# Build
make build

# Run (Terminal 1)
sudo ./ebpf-hello

# View output (Terminal 2)
sudo cat /sys/kernel/debug/tracing/trace_pipe

# Trigger events (Terminal 3)
ls
echo "test"
```

---

## 🔧 Make Targets

```bash
make vmlinux    # Generate kernel headers (auto-detected)
make generate   # Generate Go bindings from C
make build      # Build the complete program
make run        # Build and run (requires sudo)
make clean      # Remove generated files and binary
make distclean  # Also remove vmlinux.h
make setup      # Install Go dependencies
```

---

## 🏗️ Build Process

```
ebpf.c ──┐
         │
         ├─→ [Clang/LLVM] ──→ eBPF Bytecode
         │
         └─→ [bpf2go] ──────→ bpf_bpfel_x86.go (with embedded bytecode)
                                      │
                                      │
main.go ─────────────────────────────┴──→ [Go compiler] ──→ ebpf-hello
```

---

## 🎯 Key Concepts

| Concept | Description |
|---------|-------------|
| **eBPF** | Extended Berkeley Packet Filter - runs code in kernel |
| **Tracepoint** | Kernel hook point (sys_enter_execve) |
| **bpf_printk()** | eBPF function to print to trace_pipe |
| **bpf2go** | Tool to generate Go bindings from C |
| **vmlinux.h** | All kernel types in one header (CO-RE) |
| **cilium/ebpf** | Production-grade Go library for eBPF |

---

## 📋 Dependencies

### System Packages
- clang / llvm (C to eBPF compiler)
- libbpf-dev (eBPF helper headers)
- linux-headers (kernel headers)
- linux-tools-generic (bpftool for vmlinux.h)

### Go Packages
- github.com/cilium/ebpf v0.12.3
- github.com/cilium/ebpf/cmd/bpf2go (code generator)

---

## 🎓 Learning Path

### Beginner
1. Read `README.md`
2. Run the program and see it work
3. Read `ebpf.c` - understand the C code
4. Read `main.go` - understand the Go code

### Intermediate
1. Study `ARCHITECTURE.md` for system design
2. Modify `ebpf.c` to print different messages
3. Experiment with different tracepoints
4. Add command-line flags to `main.go`

### Advanced
1. Add eBPF maps for data sharing
2. Parse execve arguments (filename, argv)
3. Implement filtering by process name
4. Use ring buffers for high-performance data transfer
5. Add multiple tracepoints or kprobes

---

## 📊 Project Stats

- **Total Source Code**: ~1.6 KB (11 lines C + 49 lines Go)
- **Total Documentation**: ~15 KB across 5 files
- **Build Artifacts**: ~10 MB (mostly vmlinux.h + binary)
- **Dependencies**: 1 Go library (cilium/ebpf)
- **Build Time**: ~5 seconds
- **Runtime Performance**: Near-zero overhead

---

## ✅ Verification

Run the automated test:
```bash
sudo ./test.sh
```

Expected output:
```
=== eBPF Hello World Test ===
eBPF program loaded and attached successfully!
Checking trace output...
<...>-12345 [000] .... : bpf_trace_printk: Hello World from eBPF! execve syscall called
```

---

## 🐛 Troubleshooting

| Issue | Solution |
|-------|----------|
| Permission denied | Run with `sudo` |
| bpftool not found | Install `linux-tools-generic` |
| vmlinux.h not found | Run `make vmlinux` |
| Linter errors about .c | Ignore - expected, build works fine |

---

## 🔗 Related Reading

- [eBPF Documentation](https://ebpf.io/)
- [cilium/ebpf Library](https://github.com/cilium/ebpf)
- [BPF CO-RE](https://nakryiko.com/posts/bpf-portability-and-co-re/)
- [Linux Tracepoints](https://www.kernel.org/doc/html/latest/trace/tracepoints.html)

---

## 📝 License

GPL (required for eBPF programs loaded into the kernel)

---

**Created**: April 7, 2026  
**Last Updated**: April 7, 2026  
**Status**: ✅ Complete and Working
