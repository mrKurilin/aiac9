package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode"
)

var errInputInterrupted = errors.New("ввод остановлен пользователем")

// prepareInput enables character-by-character editing while keeping ISIG, so
// Ctrl+C still cancels the process through signal.NotifyContext. Disabling
// ICRNL preserves Enter as CR; Ctrl+J and enhanced Shift+Enter remain distinct.
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
	command = exec.Command("stty", "-icanon", "-echo", "-icrnl", "isig", "min", "0", "time", "1")
	command.Stdin = input
	if err := command.Run(); err != nil {
		restore()
		return false, func() {}, fmt.Errorf("настроить ввод терминала: %w", err)
	}
	return true, restore, nil
}

type lineEditor struct {
	text         []rune
	cursor       int
	escape       string
	history      []string
	position     int
	draft        []rune
	suggestion   string
	options      []completion
	selected     int
	dismissed    bool
	chatScroll   int
	toggleSelect bool
	interrupted  bool
}

func mouseWheelDirection(sequence string) (int, bool) {
	if !strings.HasPrefix(sequence, "\x1b[<") || !strings.HasSuffix(sequence, "M") {
		return 0, false
	}
	fields := strings.Split(strings.TrimSuffix(strings.TrimPrefix(sequence, "\x1b[<"), "M"), ";")
	if len(fields) != 3 {
		return 0, false
	}
	button, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, false
	}
	base := button &^ (4 | 8 | 16)
	switch base {
	case 64:
		return 1, true
	case 65:
		return -1, true
	default:
		return 0, false
	}
}

func (e *lineEditor) normalizeCursor() {
	if e.cursor < 0 {
		e.cursor = 0
	}
	if e.cursor > len(e.text) {
		e.cursor = len(e.text)
	}
}

func (e *lineEditor) refreshCompletions() {
	if e.dismissed {
		e.options = nil
		return
	}
	e.options = commandCompletions(string(e.text))
	if e.selected >= len(e.options) {
		e.selected = 0
	}
}

func (e *lineEditor) dismissCompletions() {
	e.escape = ""
	e.dismissed = true
	e.options = nil
	e.selected = 0
}

func (e *lineEditor) acceptCompletion(submit bool) bool {
	if len(e.options) == 0 {
		return submit
	}
	choice := e.options[e.selected]
	e.text = []rune(choice.Value)
	e.cursor = len(e.text)
	e.selected = 0
	e.dismissed = false
	e.refreshCompletions()
	return submit && choice.Submit
}

func (e *lineEditor) insert(r rune) {
	e.normalizeCursor()
	e.text = append(e.text, 0)
	copy(e.text[e.cursor+1:], e.text[e.cursor:])
	e.text[e.cursor] = r
	e.cursor++
}

func (e *lineEditor) deletePreviousRune() {
	e.normalizeCursor()
	if e.cursor == 0 {
		return
	}
	copy(e.text[e.cursor-1:], e.text[e.cursor:])
	e.text = e.text[:len(e.text)-1]
	e.cursor--
}

func (e *lineEditor) deleteNextRune() {
	e.normalizeCursor()
	if e.cursor >= len(e.text) {
		return
	}
	copy(e.text[e.cursor:], e.text[e.cursor+1:])
	e.text = e.text[:len(e.text)-1]
}

func (e *lineEditor) previousWordStart() int {
	e.normalizeCursor()
	position := e.cursor
	for position > 0 && unicode.IsSpace(e.text[position-1]) {
		position--
	}
	for position > 0 && !unicode.IsSpace(e.text[position-1]) {
		position--
	}
	return position
}

func (e *lineEditor) nextWordEnd() int {
	e.normalizeCursor()
	position := e.cursor
	for position < len(e.text) && unicode.IsSpace(e.text[position]) {
		position++
	}
	for position < len(e.text) && !unicode.IsSpace(e.text[position]) {
		position++
	}
	return position
}

func (e *lineEditor) deletePreviousWord() {
	start := e.previousWordStart()
	copy(e.text[start:], e.text[e.cursor:])
	e.text = e.text[:len(e.text)-(e.cursor-start)]
	e.cursor = start
}

func (e *lineEditor) deleteNextWord() {
	end := e.nextWordEnd()
	copy(e.text[e.cursor:], e.text[end:])
	e.text = e.text[:len(e.text)-(end-e.cursor)]
}

func (e *lineEditor) lineBounds() (start, end int) {
	e.normalizeCursor()
	start = e.cursor
	for start > 0 && e.text[start-1] != '\n' {
		start--
	}
	end = e.cursor
	for end < len(e.text) && e.text[end] != '\n' {
		end++
	}
	return start, end
}

func (e *lineEditor) moveVertical(direction int) bool {
	start, end := e.lineBounds()
	column := e.cursor - start
	if direction < 0 {
		if start == 0 {
			return false
		}
		previousEnd := start - 1
		previousStart := previousEnd
		for previousStart > 0 && e.text[previousStart-1] != '\n' {
			previousStart--
		}
		e.cursor = previousStart + minInt(column, previousEnd-previousStart)
		return true
	}
	if end == len(e.text) {
		return false
	}
	nextStart := end + 1
	nextEnd := nextStart
	for nextEnd < len(e.text) && e.text[nextEnd] != '\n' {
		nextEnd++
	}
	e.cursor = nextStart + minInt(column, nextEnd-nextStart)
	return true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (e *lineEditor) setHistoryText(value []rune) {
	e.text = append([]rune(nil), value...)
	e.cursor = len(e.text)
}

func (e *lineEditor) feed(r rune) (ready, eof bool) {
	before := string(e.text)
	defer func() {
		e.normalizeCursor()
		if before != string(e.text) {
			e.selected = 0
			e.dismissed = false
			e.refreshCompletions()
		}
	}()
	if e.escape == "\x1b" && r != '[' && r != 'O' {
		switch r {
		case 8, 127:
			e.deletePreviousWord()
		case '\n', '\r':
			e.insert('\n')
		case 'b':
			e.cursor = e.previousWordStart()
		case 'f':
			e.cursor = e.nextWordEnd()
		case 'd':
			e.deleteNextWord()
		case 's', 'S':
			e.toggleSelect = true
		default:
			e.dismissCompletions()
		}
		e.escape = ""
		return false, false
	}
	if e.escape != "" {
		e.escape += string(r)
		if len(e.escape) == 2 && (r == '[' || r == 'O') {
			return false, false
		}
		if len(e.escape) > 2 && (r < '@' || r > '~') && len(e.escape) < 32 {
			return false, false
		}
		if direction, ok := mouseWheelDirection(e.escape); ok {
			e.chatScroll += direction
			e.escape = ""
			return false, false
		}
		if e.escape == "\x1bOM" || e.escape == "\x1b[57414u" || e.escape == "\x1b[57414;1u" {
			e.escape = ""
			return e.acceptCompletion(true), false
		}
		switch e.escape {
		case "\x1b[A", "\x1bOA", "\x1b[1;1A":
			if e.position < len(e.history) {
				if e.position > 0 {
					e.position--
					e.setHistoryText([]rune(e.history[e.position]))
				}
			} else if len(e.options) > 0 {
				e.selected = (e.selected + len(e.options) - 1) % len(e.options)
			} else if !e.moveVertical(-1) && e.position > 0 {
				if e.position == len(e.history) {
					e.draft = append([]rune(nil), e.text...)
				}
				e.position--
				e.setHistoryText([]rune(e.history[e.position]))
			}
		case "\x1b[B", "\x1bOB", "\x1b[1;1B":
			if e.position < len(e.history) {
				e.position++
				if e.position == len(e.history) {
					e.setHistoryText(e.draft)
				} else {
					e.setHistoryText([]rune(e.history[e.position]))
				}
			} else if len(e.options) > 0 {
				e.selected = (e.selected + 1) % len(e.options)
			} else {
				e.moveVertical(1)
			}
		case "\x1b[D", "\x1bOD":
			if e.cursor > 0 {
				e.cursor--
			}
		case "\x1b[C", "\x1bOC":
			if e.cursor < len(e.text) {
				e.cursor++
			}
		case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
			e.cursor, _ = e.lineBounds()
		case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
			_, e.cursor = e.lineBounds()
		case "\x1b[1;3D", "\x1b[1;5D", "\x1b[5D":
			e.cursor = e.previousWordStart()
		case "\x1b[1;3C", "\x1b[1;5C", "\x1b[5C":
			e.cursor = e.nextWordEnd()
		case "\x1b[3~":
			e.deleteNextRune()
		case "\x1b[3;3~", "\x1b[127;3u", "\x1b[8;3u", "\x1b[27;3;127~", "\x1b[27;3;8~":
			e.deletePreviousWord()
		case "\x1b[13;2u", "\x1b[13;2~", "\x1b[27;2;13~":
			e.insert('\n')
		}
		e.escape = ""
		return false, false
	}
	switch r {
	case 27:
		e.escape = "\x1b"
	case '\r':
		return e.acceptCompletion(true), false
	case '\n':
		e.insert('\n')
	case '\t':
		if len(e.text) == 0 && len(e.options) == 0 && e.suggestion != "" {
			e.text = []rune(e.suggestion)
			e.cursor = len(e.text)
		} else {
			e.acceptCompletion(false)
		}
	case 1:
		e.cursor, _ = e.lineBounds()
	case 3:
		e.interrupted = true
		return false, true
	case 4:
		if len(e.text) == 0 {
			return false, true
		}
		e.deleteNextRune()
	case 5:
		_, e.cursor = e.lineBounds()
	case 8, 127:
		e.deletePreviousRune()
	case 21:
		e.text = nil
		e.cursor = 0
	case 23:
		e.deletePreviousWord()
	default:
		if !unicode.IsControl(r) && len(e.text) < 256*1024 {
			e.insert(r)
		}
	}
	return false, false
}

func readEditedLine(ctx context.Context, reader *bufio.Reader, terminal *bottomTerminal, history []string) (string, error) {
	editor := lineEditor{history: history, position: len(history)}
	if err := terminal.drawEditor(&editor); err != nil {
		return "", err
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		r, _, err := reader.ReadRune()
		if err == io.EOF {
			if editor.escape == "\x1b" {
				editor.dismissCompletions()
				if err := terminal.drawEditor(&editor); err != nil {
					return "", err
				}
			}
			continue
		}
		if err != nil {
			return "", err
		}
		editor.suggestion = terminal.suggestedReply()
		ready, eof := editor.feed(r)
		if editor.toggleSelect {
			if err := terminal.toggleSelectionMode(); err != nil {
				return "", err
			}
			editor.toggleSelect = false
		}
		if editor.chatScroll != 0 {
			if err := terminal.scrollChat(editor.chatScroll); err != nil {
				return "", err
			}
			editor.chatScroll = 0
		}
		if eof {
			if editor.interrupted {
				return "", errInputInterrupted
			}
			return "", io.EOF
		}
		if ready {
			line := string(editor.text)
			if err := terminal.drawEditor(&lineEditor{}); err != nil {
				return "", err
			}
			return line, nil
		}
		if err := terminal.drawEditor(&editor); err != nil {
			return "", err
		}
	}
}
