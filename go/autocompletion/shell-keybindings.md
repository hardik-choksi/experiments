# Shell Keybindings Reference

Standard keybindings found in most interactive shells/REPLs.
These come from the GNU Readline library used by bash, Python REPL, psql, etc.

## Cursor Movement

| Key             | Action                          |
|-----------------|---------------------------------|
| Left Arrow      | Move cursor back one character  |
| Right Arrow     | Move cursor forward one character |
| Ctrl+A / Home   | Jump to beginning of line       |
| Ctrl+E / End    | Jump to end of line             |
| Alt+F           | Move forward one word           |
| Alt+B           | Move backward one word          |

## Editing

| Key             | Action                                           |
|-----------------|--------------------------------------------------|
| Backspace       | Delete character before cursor                   |
| Ctrl+D          | Delete character under cursor (EOF if line empty) |
| Ctrl+U          | Delete from cursor to beginning of line          |
| Ctrl+K          | Delete from cursor to end of line                |
| Ctrl+W          | Delete the word before cursor                    |
| Alt+D           | Delete the word after cursor                     |
| Ctrl+Y          | Paste last deleted text (yank)                   |
| Ctrl+T          | Swap two characters before cursor                |
| Alt+U           | Uppercase the current word                       |
| Alt+L           | Lowercase the current word                       |

## History

| Key             | Action                             |
|-----------------|------------------------------------|
| Up Arrow        | Previous history entry             |
| Down Arrow      | Next history entry                 |
| Ctrl+R          | Reverse search through history     |
| Ctrl+P / Ctrl+N | Same as Up/Down arrows            |

## Screen / Session

| Key    | Action                                      |
|--------|---------------------------------------------|
| Ctrl+L | Clear screen, redraw prompt                 |
| Ctrl+C | Interrupt / cancel current input            |
| Ctrl+D | Send EOF (exit shell if line is empty)      |
| Ctrl+Z | Suspend process (SIGTSTP)                   |
| Tab    | Autocomplete                                |

## Implementation Notes

### Raw mode and `\r\n`

When the terminal is in raw mode (e.g. after `keyboard.Open()`), the terminal driver
stops translating `\n` into `\r\n`. So:

- `\n` alone moves the cursor down but stays in the same column (output looks jagged)
- `\r\n` moves to the beginning of the next line (the normal newline behavior)

Every `fmt.Print` / `fmt.Printf` inside the raw-mode key loop must use `\r\n` instead of `\n`.

### Cursor position tracking

Basic append-only input (like our current `[]rune` + backspace) only needs the slice.
To support left/right arrow movement and mid-line editing, you need:
- A cursor index (`pos int`) tracking where in the slice the cursor sits
- Re-rendering the line after each cursor move or mid-line insert/delete

### Implementation priority

1. **Easy (no cursor tracking needed):** Ctrl+U (clear line), Ctrl+D (EOF on empty), Ctrl+L (clear screen)
2. **Medium (needs cursor index):** Left/Right arrows, Ctrl+A, Ctrl+E, Home/End
3. **Harder (needs cursor + word boundaries):** Ctrl+W, Alt+F, Alt+B
4. **Advanced (needs kill buffer + history):** Ctrl+Y, Ctrl+K, Up/Down history
