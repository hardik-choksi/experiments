# Epoll TCP Server — Project Instructions

## Teaching & Explanation Rules

- When explaining any concept, syscall, pattern, or technique: give the **complete picture**. Cover what it does, why it exists, how it works in the real world, and what we're doing differently (and why).
- Never present a partial implementation without explaining what's missing and why. If something "doesn't really apply" or "won't work in our context," say so upfront — don't bury it.
- When implementing net/stdlib interfaces, explain which methods are meaningful for our use case and which are stubs, and why.
- Always add new learnings and explanations to `learnings.md` so they accumulate as a reference.
- For deep topics (netpoller internals, scheduler model, etc.) that need more than a couple of paragraphs: create a dedicated file in `docs/` and link to it from `learnings.md`. Keep `learnings.md` as the index with short summaries + links. The `docs/` folder holds the deep dives.

## Code Style

- Use meaningful comments that explain the "why" — especially for syscalls, explain what the syscall does, what the kernel does with it, and how production systems handle it differently.
- When wrapping syscalls, note the real-world behavior (e.g., Go's net package, nginx, etc.) vs our simplified approach.
