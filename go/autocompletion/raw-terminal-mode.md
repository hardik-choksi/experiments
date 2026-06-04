# Raw Terminal Mode & the `eiannone/keyboard` Library

## Cooked vs Raw Mode

By default, your terminal runs in **cooked (canonical) mode**. The kernel's terminal driver (the "line discipline") sits between your keyboard and your program and does a lot of work:

- **Line buffering**: Keypresses are held in a kernel buffer. Your program receives nothing until Enter is pressed. This is why `bufio.Reader.ReadString('\n')` waits for a full line.
- **Echo**: The driver automatically displays each character you type on screen.
- **Signal generation**: Ctrl+C sends SIGINT, Ctrl+Z sends SIGTSTP. Your program never sees the byte — the kernel intercepts it.
- **CR/LF translation**: `\r` (carriage return) can be translated to `\n` on input. On output, `\n` gets `\r` prepended automatically.
- **Special character processing**: Backspace erases the previous character in the buffer; Ctrl+W deletes a word — all handled by the kernel.

**Raw mode** strips all of this away. The program gets every byte the moment it's typed, with no buffering, no echo, and no signal conversion.

This is what interactive programs (vim, htop, shells with readline) need.

## `termios` — The Control Structure

`termios` is the kernel interface for configuring a terminal. It's a struct with flag fields that control behavior. The key flags:

| Flag     | Field   | What it does when cleared                           |
|----------|---------|-----------------------------------------------------|
| `ICANON` | Lflag   | Disables line buffering — bytes arrive immediately  |
| `ECHO`   | Lflag   | Disables auto-echo of typed characters              |
| `ISIG`   | Lflag   | Disables signal generation (Ctrl+C won't send SIGINT) |
| `IEXTEN` | Lflag   | Disables extended processing (Ctrl+V quoting etc.)  |
| `ICRNL`  | Iflag   | Stops CR→NL translation on input                    |
| `IXON`   | Iflag   | Disables Ctrl+S / Ctrl+Q flow control               |
| `OPOST`  | Oflag   | Disables output processing (including `\n` → `\r\n` translation) |

Two additional settings:
- `VMIN=1` — block until at least 1 byte is available
- `VTIME=0` — no timeout

This makes reads blocking but byte-by-byte immediate.

## How `keyboard.Open()` Works

1. Opens `/dev/tty` directly (not stdin) — this ensures it reaches the actual controlling terminal even if stdin is redirected (e.g. piped input).

2. Saves the current `termios` state into `orig_tios` — this snapshot is used to restore later.

3. Modifies the `termios` copy to raw mode by clearing `ICANON`, `ECHO`, `ISIG`, `IEXTEN`, `ICRNL`, `IXON`, etc.

4. Applies the raw settings via `ioctl(fd, TCSETS, &new_tios)`.

5. Sets `O_ASYNC` and `F_SETOWN` on the fd via `fcntl` — this makes the kernel send a `SIGIO` signal to the process whenever new input bytes are available (async I/O).

6. Launches two goroutines:
   - **Reader goroutine**: listens for SIGIO, does non-blocking `unix.Read()` calls, pushes raw bytes into `input_buf` channel.
   - **Parser goroutine** (`inputEventsProducer`): consumes `input_buf`, parses bytes into `KeyEvent` structs, sends them to `inputComm` channel.

## How `keyboard.GetKey()` Works

Simply blocks on `<-inputComm` and returns the next parsed `(rune, Key, error)`.

The parser (`extract_event()`) uses a decision tree:

1. **First byte is `\x1b` (ESC)?** → It's an escape sequence. Match against known terminal key strings (from terminfo database). If matched, return the corresponding `Key` constant (e.g. `KeyArrowUp`). If only two bytes and second isn't `[`, treat as Alt+letter.

2. **Byte value `<= 0x20` or `== 0x7F`?** → It's a control character. These map directly to ASCII:
   - `0x01` = Ctrl+A, `0x02` = Ctrl+B, ... `0x1A` = Ctrl+Z
   - `0x09` = Tab, `0x0D` = Enter, `0x7F` = Backspace

3. **Otherwise** → Decode as a UTF-8 rune. Return as a printable character.

## ANSI Escape Sequences

Special keys are encoded as multi-byte sequences starting with `\x1b` (ESC):

| Key         | Byte sequence |
|-------------|---------------|
| Arrow Up    | `\x1b[A`      |
| Arrow Down  | `\x1b[B`      |
| Arrow Right | `\x1b[C`      |
| Arrow Left  | `\x1b[D`      |
| Home        | `\x1b[H`      |
| End         | `\x1b[F`      |
| Delete      | `\x1b[3~`     |
| F1          | `\x1bOP`      |

These vary by terminal type (xterm, rxvt, linux console). The library loads the right mappings from the terminfo database (`/usr/share/terminfo/`) or falls back to hardcoded tables.

## How `keyboard.Close()` Works

1. Signals both goroutines to stop and waits for them to exit.
2. Calls `ioctl(fd, TCSETS, &orig_tios)` — writes back the saved original `termios` state.

**This step is critical.** Without it, the terminal stays in raw mode after the program exits — no echo, no line editing, the shell becomes unusable. This is why our Ctrl+C handler calls `keyboard.Close()` before `os.Exit(0)`.

## The Full Flow

```
keyboard.Open()
  → open /dev/tty
  → save termios (orig_tios)
  → set raw mode: clear ICANON, ECHO, ISIG
  → set O_ASYNC on fd (SIGIO-driven reads)
  → goroutine 1: SIGIO → unix.Read() → input_buf channel
  → goroutine 2: input_buf → extract_event() → inputComm channel

keyboard.GetKey()
  → blocks on <-inputComm
  → returns (rune, Key, error)

keyboard.Close()
  → stop goroutines
  → restore orig_tios via ioctl TCSETS
```

## Why This Matters for Our Code

- **`\r\n` not `\n`**: Since `OPOST` is off, the terminal won't auto-translate `\n` → `\r\n`. We must print `\r\n` explicitly for proper newlines.
- **No echo**: We must manually `fmt.Print(string(char))` to show what the user types.
- **No signals**: Ctrl+C won't send SIGINT — we must handle `keyboard.KeyCtrlC` ourselves.
- **Backspace is just a byte**: The kernel won't erase anything. We handle it with `\b \b` (move back, overwrite with space, move back).
- **Must call `Close()`**: Any exit path (Ctrl+C, panic, normal exit) must restore the terminal, or the user's shell breaks.

## Control Characters — Quick Reference

Since `ISIG` is off, Ctrl+key combos arrive as raw bytes:

| Combo   | Byte  | ASCII name |
|---------|-------|------------|
| Ctrl+A  | 0x01  | SOH        |
| Ctrl+C  | 0x03  | ETX        |
| Ctrl+D  | 0x04  | EOT        |
| Ctrl+E  | 0x05  | ENQ        |
| Ctrl+K  | 0x0B  | VT         |
| Ctrl+L  | 0x0C  | FF         |
| Ctrl+U  | 0x15  | NAK        |
| Ctrl+W  | 0x17  | ETB        |
| Ctrl+Z  | 0x1A  | SUB        |
| Tab     | 0x09  | HT         |
| Enter   | 0x0D  | CR         |
| Esc     | 0x1B  | ESC        |
| Backsp  | 0x7F  | DEL        |
