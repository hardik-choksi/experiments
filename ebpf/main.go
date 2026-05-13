package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
)

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target amd64 bpf ebpf.c -- -I/usr/include/bpf -I.

func main() {
	// Remove resource limits for eBPF
	if err := rlimit.RemoveMemlock(); err != nil {
		log.Fatalf("Failed to remove memlock limit: %v", err)
	}

	// Load pre-compiled eBPF programs and maps
	objs := bpfObjects{}
	if err := loadBpfObjects(&objs, nil); err != nil {
		log.Fatalf("Failed to load eBPF objects: %v", err)
	}
	defer objs.Close()

	// Attach to the tracepoint
	tp, err := link.Tracepoint("syscalls", "sys_enter_execve", objs.HelloWorld, nil)
	if err != nil {
		log.Fatalf("Failed to attach tracepoint: %v", err)
	}
	defer tp.Close()

	fmt.Println("eBPF program loaded and attached successfully!")
	fmt.Println("Hello World will be printed to /sys/kernel/debug/tracing/trace_pipe whenever execve is called")
	fmt.Println("Run this in another terminal to see the output:")
	fmt.Println("  sudo cat /sys/kernel/debug/tracing/trace_pipe")
	fmt.Println("\nPress Ctrl+C to exit...")

	// Wait for signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	fmt.Println("\nCleaning up and exiting...")
}
