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

func prepareInput(in io.Reader, out io.Writer) (bool, func(), error) {
	input, ok := in.(*os.File)
	if !ok {
		return false, func() {}, nil
	}
	output, ok := out.(*os.File)
	if !ok {
		return false, func() {}, nil
	}
	info, err := output.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false, func() {}, nil
	}
	command := exec.Command("stty", "-g")
	command.Stdin = input
	state, err := command.Output()
	if err != nil {
		return false, func() {}, nil
	}
	restore := func() {
		command := exec.Command("stty", strings.TrimSpace(string(state)))
		command.Stdin = input
		_ = command.Run()
	}
	command = exec.Command("stty", "-icanon", "-echo", "min", "0", "time", "1")
	command.Stdin = input
	if err := command.Run(); err != nil {
		restore()
		return false, func() {}, fmt.Errorf("настроить ввод терминала: %w", err)
	}
	return true, restore, nil
}

type lineEditor struct {
	complete  completer
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
		e.text = nil
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

func readEditedLine(ctx context.Context, reader *bufio.Reader, out io.Writer, complete completer, history []string) (string, error) {
	editor := &lineEditor{history: history, position: len(history), complete: complete}
	menuRows := 0
	fmt.Fprint(out, "\n│ ")
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		r, _, err := reader.ReadRune()
		if err == io.EOF {
			if editor.escape == "\x1b" {
				editor.dismissMenu()
				menuRows, err = renderInputMenu(out, editor, menuRows)
				if err != nil {
					return "", err
				}
			}
			continue
		}
		if err != nil {
			return "", err
		}
		ready, eof := editor.feed(r)
		if eof {
			return "", io.EOF
		}
		if ready {
			if menuRows > 0 {
				editor.options = nil
				if _, err := renderInputMenu(out, editor, menuRows); err != nil {
					return "", err
				}
			}
			_, err := fmt.Fprint(out, "\r\x1b[2K")
			return string(editor.text), err
		}
		menuRows, err = renderInputMenu(out, editor, menuRows)
		if err != nil {
			return "", err
		}
	}
}
