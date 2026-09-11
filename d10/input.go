package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode"
)

// Keep ISIG enabled so Ctrl+C still cancels HTTP requests through NotifyContext.
// Terminal settings are restored by runTerminal on every return path.
func prepareInput(in io.Reader, out io.Writer) (bool, func(), error) {
	f, ok := in.(*os.File)
	if !ok {
		return false, func() {}, nil
	}
	target, ok := out.(*os.File)
	if !ok {
		return false, func() {}, nil
	}
	info, err := target.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false, func() {}, nil
	}
	cmd := exec.Command("stty", "-g")
	cmd.Stdin = f
	state, err := cmd.Output()
	if err != nil {
		return false, func() {}, nil
	} // Pipes use the ordinary scanner.
	restore := func() { c := exec.Command("stty", strings.TrimSpace(string(state))); c.Stdin = f; _ = c.Run() }
	// VTIME lets the input goroutine periodically switch between line editing
	// and listening for Esc while an HTTP request is running.
	cmd = exec.Command("stty", "-icanon", "-echo", "min", "0", "time", "1")
	cmd.Stdin = f
	if err := cmd.Run(); err != nil {
		restore()
		return false, func() {}, fmt.Errorf("настроить ввод терминала: %w", err)
	}
	return true, restore, nil
}

type lineEditor struct {
	complete  completer
	onScroll  func(pages int)
	options   []completion
	selected  int
	dismissed bool
	text      []rune
	escape    string
	history   []string
	position  int
	draft     []rune
}

func (e *lineEditor) feed(r rune) (ready, eof bool) {
	before := string(e.text)
	defer func() {
		if before != string(e.text) {
			e.selected = 0
			e.dismissed = false
			e.refreshCompletions()
		}
	}()

	if e.escape == "\x1b" && r != '[' && r != 'O' {
		e.dismissMenu()
		return e.feed(r)
	}
	if e.escape != "" {
		e.escape += string(r)
		if len(e.escape) == 2 && (r == '[' || r == 'O') {
			return
		}
		if len(e.escape) > 2 && (r < '@' || r > '~') && len(e.escape) < 32 {
			return
		}
		switch e.escape {
		case "\x1b[A", "\x1bOA":
			if len(e.options) > 0 {
				e.selected = (e.selected + len(e.options) - 1) % len(e.options)
				break
			}
			if e.position > 0 {
				if e.position == len(e.history) {
					e.draft = append([]rune(nil), e.text...)
				}
				e.position--
				e.text = []rune(e.history[e.position])
			}
		case "\x1b[B", "\x1bOB":
			if len(e.options) > 0 {
				e.selected = (e.selected + 1) % len(e.options)
				break
			}
			if e.position < len(e.history) {
				e.position++
				if e.position == len(e.history) {
					e.text = append([]rune(nil), e.draft...)
				} else {
					e.text = []rune(e.history[e.position])
				}
			}
		case "\x1b[127;5u", "\x1b[8;5u", "\x1b[27;5;127~", "\x1b[27;5;8~", "\x1b[3;5~":
			e.text = nil
		case "\x1b[5~", "\x1b[5;2~":
			if e.onScroll != nil {
				e.onScroll(1) // PageUp: back through the dialogue.
			}
		case "\x1b[6~", "\x1b[6;2~":
			if e.onScroll != nil {
				e.onScroll(-1)
			}
		}
		e.escape = ""
		return
	}
	switch r {
	case 27:
		e.escape = "\x1b"
	case '\n', '\r':
		return e.acceptCompletion(true), false
	case '\t':
		e.acceptCompletion(false)
	case 4:
		if len(e.text) == 0 {
			return false, true
		}
	case 8, 21:
		e.text = nil // Ctrl+Backspace (BS), Ctrl+U.
	case 127:
		if len(e.text) > 0 {
			e.text = e.text[:len(e.text)-1]
		}
	default:
		if !unicode.IsControl(r) && len(e.text) < 256*1024 {
			e.text = append(e.text, r)
		}
	}
	return
}
func readEditedLine(reader *bufio.Reader, out io.Writer, history ...string) (string, error) {
	return readEditedLinePolling(reader, out, false, history...)
}

func readEditedLinePolling(reader *bufio.Reader, out io.Writer, poll bool, history ...string) (string, error) {
	return readEditedLineLayout(reader, out, poll, false, history...)
}

func readEditedLineLayout(reader *bufio.Reader, out io.Writer, poll, fixed bool, history ...string) (string, error) {
	return readEditedLineCompletions(reader, out, poll, fixed, nil, history...)
}
func readEditedLineCompletions(reader *bufio.Reader, out io.Writer, poll, fixed bool, complete completer, history ...string) (string, error) {
	return readEditedLineCompletionsContext(context.Background(), reader, out, poll, fixed, complete, history...)
}
func readEditedLineCompletionsContext(ctx context.Context, reader *bufio.Reader, out io.Writer, poll, fixed bool, complete completer, history ...string) (string, error) {
	return readEditedLineSession(ctx, reader, out, poll, fixed, complete, editSession{}, history...)
}

// editSession carries what a caller needs beyond plain line entry: a reaction
// to a bare Esc, so a request can be cancelled without leaving the editor.
type editSession struct {
	onEscape func()
	onScroll func(pages int)
}

func readEditedLineSession(ctx context.Context, reader *bufio.Reader, out io.Writer, poll, fixed bool, complete completer, session editSession, history ...string) (string, error) {
	footer, anchored := out.(*bottomTerminal)

	if anchored {
		if err := footer.drawEditor(&lineEditor{}); err != nil {
			return "", err
		}
	} else if fixed {
		_, rows := terminalSize(out)
		fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K", rows)
	} else {
		fieldHeader(out, "ВВОД", "33")
	}
	if !anchored {
		fmt.Fprint(out, "│ ")
	}
	e := lineEditor{history: history, position: len(history), complete: complete, onScroll: session.onScroll}
	menuRows := 0
	redraw := func() error {
		if anchored {
			return footer.drawEditor(&e)
		}
		var err error
		menuRows, err = renderInputMenu(out, &e, menuRows)
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		r, _, err := reader.ReadRune()
		if err != nil {
			if poll && err == io.EOF {
				// A lone Esc is only distinguishable from an arrow key once the
				// poll times out with nothing following it.
				if e.escape == "\x1b" {
					open := len(e.options) > 0
					e.dismissMenu()
					if !open && session.onEscape != nil {
						session.onEscape()
					}
					err = redraw()
					if err != nil {
						return "", err
					}
				}
				continue
			}
			return "", err
		}
		ready, eof := e.feed(r)
		if eof {
			return "", io.EOF
		}
		if ready {
			if anchored {
				err := footer.drawEditor(&lineEditor{})
				return string(e.text), err
			}
			if menuRows > 0 {
				e.options = nil
				if _, err := renderInputMenu(out, &e, menuRows); err != nil {
					return "", err
				}
			}
			if fixed {
				_, err := fmt.Fprint(out, "\r\x1b[2K")
				return string(e.text), err
			}
			_, err := fmt.Fprint(out, "\r\x1b[2K\x1b[1A\r\x1b[2K")
			return string(e.text), err
		}
		if anchored || complete != nil {
			err = redraw()
			if err != nil {
				return "", err
			}
		} else {
			visible := inputViewport(e.text, terminalColumns(out)-3)
			if _, err := fmt.Fprintf(out, "\r\x1b[2K│ %s", terminalControls.ReplaceAllString(visible, "")); err != nil {
				return "", err
			}
		}
	}
}
