#include "vmlinux.h"
#include "bpf_helpers.h"

SEC("tp/syscalls/sys_enter_execve")
int trace_execve(void *ctx)
{
    char msg[] = "Hello from eBPF! Someone executed a program\n";
    bpf_trace_printk(msg, sizeof(msg));
    return 0;
}

char _license[] SEC("license") = "GPL";
