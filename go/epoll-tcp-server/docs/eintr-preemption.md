# EINTR, SIGURG, and Goroutine Preemption

## "We ignore EINTR — does that mean our goroutine can't be preempted?"

No. The goroutine **is still preempted**. EINTR is the *aftermath* of preemption, not a request for it. Here's the exact sequence:

```
1. Our goroutine is blocked in epoll_wait
2. Sysmon decides it's been running too long
   → calls tgkill(pid, M.tid, SIGURG) targeting the OS thread
3. The OS thread's signal handler fires
   → injects asyncPreempt into the goroutine's execution
   → goroutine is yanked off the M and parked
4. The M's epoll_wait syscall gets interrupted, returns EINTR
   — but our goroutine doesn't see it yet, it's parked
5. The scheduler runs other goroutines on this M+P
6. Eventually the scheduler re-runs our goroutine
   → it resumes at the return from epoll_wait, sees EINTR
7. We `continue`, retry epoll_wait, life goes on
```

The critical point: **preemption already happened between steps 3 and 6.** By the time our code sees `EINTR` and decides to `continue`, the runtime already preempted us, ran other goroutines, and came back. The `continue` just cleans up the interrupted syscall — it doesn't prevent or allow preemption.

If we *didn't* handle EINTR (treated it as a fatal error), preemption would still work — we'd just crash our event loop needlessly.

## "How can the runtime send a signal to a goroutine? Signals are process-wide."

Signals ARE process-wide in the default POSIX model. `kill(pid, sig)` delivers a signal to the process, and the kernel picks an arbitrary thread to handle it.

But Linux has **`tgkill(tgid, tid, sig)`** — a syscall that sends a signal to a **specific thread** within a process. "tgkill" stands for "thread group kill." This is the per-thread targeting mechanism that Go relies on.

The runtime doesn't "send a signal to a goroutine." It sends a signal to the **OS thread (M) that's running that goroutine**. Since the runtime knows the G→M mapping at all times, it can target precisely:

```
Sysmon finds: G7 has been running >10ms on M3
  → preemptM(m3)
    → tgkill(pid, m3.tid, SIGURG)
      → SIGURG delivered to M3 specifically
        → M3's signal handler runs on M3's gsignal stack
          → handler injects asyncPreempt into G7
            → G7 is parked, M3 is free
```

### Three levels of signal delivery on Linux

| Mechanism | Scope | Used by |
|---|---|---|
| `kill(pid, sig)` | Process-wide — kernel picks a thread | Terminal (Ctrl+C → SIGINT), `kill` command |
| `tgkill(tgid, tid, sig)` | Specific thread | Go runtime (SIGURG for preemption) |
| `tkill(tid, sig)` | Specific thread (deprecated) | Older Linux, superseded by tgkill |

`tgkill` requires both the thread group ID (process ID) and the thread ID. This two-key design prevents a race condition: if a thread dies and its TID is recycled, the TGID won't match, and the signal won't be misdelivered to a random thread in a different process.

### Why SIGURG specifically?

Go chose SIGURG because it's **safe to hijack**:

- Not used by glibc or musl (no libc function sends or handles it)
- Doesn't interfere with debuggers (gdb/lldb don't use it)
- Not commonly used by application code (its original purpose — TCP out-of-band data notification — is nearly extinct)
- Default disposition is "ignore" — if something goes wrong and the handler isn't installed, the signal is silently dropped rather than killing the process

### Each M has its own signal stack

Every OS thread (M) has a `gsignal` goroutine — a special goroutine with its own stack, dedicated to handling signals. When SIGURG arrives at M3:

1. The kernel switches M3 to the `gsignal` stack (set up via `sigaltstack` at thread creation)
2. The signal handler runs there, not on G7's stack — so it can safely inspect and modify G7's state
3. The handler pushes an `asyncPreempt` frame onto G7's stack
4. When the handler returns, execution "resumes" into `asyncPreempt`, which calls `gopreempt_m()` → G7 is parked

This is why our `epoll_wait` returns EINTR: the signal interrupted the syscall, and by the time the OS thread returns to user space, the goroutine that issued the syscall has been preempted. The EINTR is just the kernel saying "your syscall was interrupted by a signal" — it has no idea about goroutines.
