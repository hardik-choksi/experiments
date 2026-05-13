# eBPF Hello World Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        USER SPACE                                │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  ┌──────────────┐         ┌───────────────────────────────┐    │
│  │  main.go     │         │  Any Process (e.g., ls, echo) │    │
│  │              │         │                                │    │
│  │  1. Load     │         │  Calls execve() syscall       │    │
│  │  2. Attach   │         │         │                      │    │
│  │  3. Wait     │         │         │                      │    │
│  └──────┬───────┘         └─────────┼─────────────────────┘    │
│         │                           │                            │
│         │ eBPF Syscalls             │ execve()                   │
│         │                           │                            │
├─────────┼───────────────────────────┼────────────────────────────┤
│         │     KERNEL SPACE          │                            │
│         │                           ▼                            │
│         │              ┌────────────────────────┐                │
│         │              │  sys_enter_execve      │                │
│         │              │  Tracepoint            │                │
│         │              └──────────┬─────────────┘                │
│         │                         │                              │
│         ▼                         │                              │
│  ┌─────────────────┐              │                              │
│  │ eBPF Verifier   │              │ Triggers                     │
│  │ (Safety Check)  │              │                              │
│  └────────┬────────┘              ▼                              │
│           │              ┌──────────────────┐                    │
│           │              │  hello_world()   │                    │
│           │              │  eBPF Program    │                    │
│           │              │                  │                    │
│           │              │  bpf_printk()    │                    │
│           │              └────────┬─────────┘                    │
│           │                       │                              │
│           │                       │ Writes                       │
│           │                       ▼                              │
│           │              ┌──────────────────┐                    │
│           └─────────────→│  Loaded eBPF     │                    │
│              Loads       │  Program         │                    │
│                          └──────────────────┘                    │
│                                   │                              │
│                                   │ Outputs to                   │
│                                   ▼                              │
│                          ┌──────────────────┐                    │
│                          │  trace_pipe      │                    │
│                          │  (ring buffer)   │                    │
│                          └────────┬─────────┘                    │
│                                   │                              │
├───────────────────────────────────┼──────────────────────────────┤
│         USER SPACE                │                              │
│                                   │ Read via:                    │
│                                   │ cat /sys/kernel/debug/       │
│                                   │     tracing/trace_pipe       │
│                                   ▼                              │
│                          ┌──────────────────┐                    │
│                          │  Terminal Output │                    │
│                          │  "Hello World!"  │                    │
│                          └──────────────────┘                    │
│                                                                   │
└───────────────────────────────────────────────────────────────────┘

                            DATA FLOW
                            ═════════

┌────────────┐      ┌──────────┐      ┌─────────────┐
│  ebpf.c    │──────│ Clang/   │──────│ eBPF        │
│  (Source)  │ C→IR │ LLVM     │ IR→  │ Bytecode    │
└────────────┘      └──────────┘      └──────┬──────┘
                                             │
                                             │ Embedded by bpf2go
                                             ▼
                                      ┌──────────────┐
                                      │ Go Binary    │
                                      │ (ebpf-hello) │
                                      └──────────────┘
```

## Components

### Compile Time
1. `ebpf.c` → Clang/LLVM → eBPF bytecode
2. `bpf2go` embeds bytecode into `bpf_bpfel_x86.go`
3. Go compiler builds final binary

### Runtime
1. Go program loads eBPF bytecode via syscalls
2. Kernel verifier validates the program
3. Program attached to `sys_enter_execve` tracepoint
4. Every `execve()` call triggers the eBPF program
5. `bpf_printk()` writes to kernel ring buffer
6. User reads from `/sys/kernel/debug/tracing/trace_pipe`

## Security
- eBPF programs run in a sandboxed VM
- Kernel verifier ensures memory safety
- Cannot crash the kernel
- Cannot access arbitrary memory
- Limited instruction set
