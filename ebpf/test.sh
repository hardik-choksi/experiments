#!/bin/bash
# test.sh - Script to verify eBPF program works

set -e

echo "=== eBPF Hello World Test ==="
echo

# Check if running as root
if [ "$EUID" -ne 0 ]; then 
    echo "This script must be run as root (use sudo)"
    exit 1
fi

# Check if binary exists
if [ ! -f "./ebpf-hello" ]; then
    echo "ebpf-hello binary not found. Building..."
    make build
fi

echo "Starting eBPF program in background..."
./ebpf-hello &
EBPF_PID=$!

# Give it time to attach
sleep 2

echo "eBPF program started with PID: $EBPF_PID"
echo

# Trigger some execve syscalls
echo "Triggering execve syscalls..."
ls > /dev/null
echo "test" > /dev/null
date > /dev/null

# Check trace pipe for output
echo
echo "Checking trace output (last 10 lines with 'Hello World'):"
cat /sys/kernel/debug/tracing/trace_pipe | grep "Hello World" | head -10 &
TRACE_PID=$!

# Let it capture some output
sleep 3

# Cleanup
echo
echo "Cleaning up..."
kill $TRACE_PID 2>/dev/null || true
kill $EBPF_PID 2>/dev/null || true
wait $EBPF_PID 2>/dev/null || true

echo
echo "=== Test Complete ==="
echo "If you saw 'Hello World from eBPF' messages above, the program works!"
