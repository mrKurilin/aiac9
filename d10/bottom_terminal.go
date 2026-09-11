package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Transcript output scrolls above a reserved footer. Only this writer uses the
// terminal's saved cursor; editor redraws never disturb streaming output.
type bottomTerminal struct {
	mu                 sync.Mutex
	out                io.Writer
	styled             bool
	size               func() (int, int)
	columns, rows, top int
	editor             lineEditor
	stop, done         chan struct{}
	closed             bool
	// A moving footer erases rows that belonged to whatever is painted above
	// it, so every move is counted and reported to the owner of that area.
	geometry, reported int
	onFooterMove       func()
}

func newBottomTerminal(out io.Writer) (*bottomTerminal, error) {
	return openBottomTerminal(out, func() (int, int) { return terminalSize(out) })
}
func openBottomTerminal(out io.Writer, size func() (int, int)) (*bottomTerminal, error) {
	_, noColor := os.LookupEnv("NO_COLOR")
	t := &bottomTerminal{out: out, size: size, styled: terminalOutput(out) && !noColor, stop: make(chan struct{}), done: make(chan struct{})}
	if err := t.resizeLocked(); err != nil {
		return nil, err
	}
	go func() {
		defer close(t.done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-t.stop:
				return
			case <-ticker.C:
				t.mu.Lock()
				_ = t.resizeLocked()
				geometry, reported, hook := t.geometry, t.reported, t.onFooterMove
				t.mu.Unlock()
				if hook == nil || geometry == reported {
					continue
				}
				// Run the hook without the lock: it repaints through this
				// writer. Record the generation captured before the call, so a
				// move that lands while it runs is reported by the next tick.
				hook()
				t.mu.Lock()
				t.reported = geometry
				t.mu.Unlock()
			}
		}
	}()
	return t, nil
}
func (t *bottomTerminal) terminalSize() (int, int) { return t.size() }
func (t *bottomTerminal) isTerminal() bool         { return terminalOutput(t.out) }
func footerTop(rows, options int) int {
	// Closed menu needs only the hint and input rows. Grow the footer just for
	// visible suggestions, so normal conversation output reaches the input.
	reserved := 2 // hint plus input
	if options > 0 {
		visible := options
		if visible > 6 {
			visible = 6
		}
		reserved = visible + 1
	}
	if reserved >= rows {
		reserved = rows - 1
	}
	if reserved < 1 {
		reserved = 1
	}
	top := rows - reserved + 1
	// DECSTBM requires at least two rows in its scrolling region.
	if top < 3 {
		top = 3
	}
	return top
}
func (t *bottomTerminal) resizeLocked() error {
	columns, rows := t.size()
	if rows < 3 {
		rows = 3
	}
	if columns < 4 {
		columns = 4
	}
	top := footerTop(rows, len(t.editor.options))
	if columns == t.columns && rows == t.rows && top == t.top {
		return nil
	}
	var frame strings.Builder
	// Remove the previous footer when a larger terminal exposes its old position.
	if t.rows > 0 {
		for row := t.top; row <= t.rows && row <= rows; row++ {
			fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row)
		}
	}
	t.columns, t.rows, t.top = columns, rows, top
	t.geometry++
	fmt.Fprintf(&frame, "\x1b[1;%dr\x1b[%d;1H\x1b7", t.top-1, t.top-1)
	frame.WriteString(t.footerFrame())
	_, err := io.WriteString(t.out, frame.String())
	return err
}
func (t *bottomTerminal) footerCursor() string {
	visible := inputViewport(t.editor.text, t.columns-3)
	column := 3
	for _, r := range visible {
		column += runeCells(r)
	}
	return fmt.Sprintf("\x1b[0m\x1b[%d;%dH", t.rows, column)
}
func (t *bottomTerminal) footerFrame() string {
	var frame strings.Builder
	frame.WriteString("\x1b[0m")
	for row := t.top; row <= t.rows; row++ {
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row)
	}
	// Keep suggestions tight against the input: the last option occupies the
	// row immediately above it. Controls remain in the persistent hint only
	// while the completion menu is closed.
	capacity := t.rows - t.top
	if capacity < 0 {
		capacity = 0
	}
	shown := len(t.editor.options)
	if shown > capacity {
		shown = capacity
	}
	start := 0
	if t.editor.selected >= shown && shown > 0 {
		start = t.editor.selected - shown + 1
	}
	first := t.rows - shown
	for i := 0; i < shown; i++ {
		marker := "  "
		if start+i == t.editor.selected {
			marker = "› "
		}
		fmt.Fprintf(&frame, "\x1b[%d;1H%s", first+i, menuPrefix(marker+t.editor.options[start+i].Label, t.columns-1))
	}
	if shown == 0 && t.rows-1 >= t.top {
		// Dim: the hint sits directly above the input and must not read as
		// something typed there.
		hint := menuPrefix("/ — команды · Enter — отправить · PgUp/PgDn — прокрутка", t.columns-1)
		if t.styled {
			hint = "\x1b[2m" + hint + "\x1b[0m"
		}
		fmt.Fprintf(&frame, "\x1b[%d;1H%s", t.rows-1, hint)
	}
	fmt.Fprintf(&frame, "\x1b[%d;1H│ %s", t.rows, terminalControls.ReplaceAllString(inputViewport(t.editor.text, t.columns-3), ""))
	frame.WriteString(t.footerCursor())
	return frame.String()
}
func (t *bottomTerminal) drawEditor(e *lineEditor) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.editor = *e
	t.editor.text = append([]rune(nil), e.text...)
	t.editor.options = append([]completion(nil), e.options...)
	if err := t.resizeLocked(); err != nil {
		return err
	}
	_, err := io.WriteString(t.out, t.footerFrame())
	return err
}
func (t *bottomTerminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return 0, io.ErrClosedPipe
	}
	if err := t.resizeLocked(); err != nil {
		return 0, err
	}
	// Preserve partial lines and ANSI attributes between streaming chunks.
	frame := "\x1b8" + string(p) + "\x1b7" + t.footerCursor()
	if _, err := io.WriteString(t.out, frame); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (t *bottomTerminal) clear() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := fmt.Fprintf(t.out, "\x1b[2J\x1b[3J\x1b[%d;1H\x1b7%s", t.top-1, t.footerFrame())
	return err
}
func (t *bottomTerminal) Close() error {
	close(t.stop)
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	_, err := fmt.Fprintf(t.out, "\x1b[r\x1b[0m\x1b[%d;1H\x1b[2K\n", t.rows)
	return err
}
