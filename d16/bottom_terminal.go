package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
)

// bottomTerminal renders a complete viewport from an internal transcript.
// Repainting absolute rows is more portable than relying on DEC scroll regions:
// status and input therefore remain fixed even in embedded terminal widgets.
type bottomTerminal struct {
	mu            sync.Mutex
	out           io.Writer
	size          func() (int, int)
	columns, rows int
	status        string
	suggestion    string
	loading       bool
	selectionMode bool
	spinnerFrame  int
	editor        lineEditor
	transcript    []rune
	scrollOffset  int
	pendingOutput int
	lastOutput    time.Time
	stop, done    chan struct{}
	closed        bool
}

const transcriptRuneLimit = 256 * 1024
const outputRenderInterval = 25 * time.Millisecond
const outputRenderBatch = 256

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func newBottomTerminal(out io.Writer) (*bottomTerminal, error) {
	return openBottomTerminal(out, func() (int, int) { return terminalSize(out) })
}

func openBottomTerminal(out io.Writer, size func() (int, int)) (*bottomTerminal, error) {
	t := &bottomTerminal{
		out:    out,
		size:   size,
		status: "Состояние: нет задачи  ·  Этап: —",
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	if _, err := io.WriteString(out, "\x1b[?1049h\x1b[?1000h\x1b[?1006h\x1b[>1u\x1b[?25l\x1b[2J\x1b[H"); err != nil {
		return nil, err
	}
	if err := t.resizeLocked(); err != nil {
		return nil, err
	}
	go func() {
		defer close(t.done)
		ticker := time.NewTicker(40 * time.Millisecond)
		defer ticker.Stop()
		spinnerTick := 0
		for {
			select {
			case <-t.stop:
				return
			case <-ticker.C:
				t.mu.Lock()
				_ = t.resizeLocked()
				render := false
				if t.pendingOutput > 0 {
					t.pendingOutput = 0
					t.lastOutput = time.Now()
					render = true
				}
				if t.loading {
					spinnerTick++
					if spinnerTick >= 4 {
						spinnerTick = 0
						t.spinnerFrame = (t.spinnerFrame + 1) % len(spinnerFrames)
						render = true
					}
				}
				if render {
					_ = t.renderLocked()
				}
				t.mu.Unlock()
			}
		}
	}()
	return t, nil
}

func (t *bottomTerminal) terminalSize() (int, int) { return t.size() }

func trimCells(text string, cells int) string {
	if cells <= 0 {
		return ""
	}
	var result strings.Builder
	for _, r := range text {
		width := runeCells(r)
		if width > cells {
			break
		}
		cells -= width
		result.WriteRune(r)
	}
	return result.String()
}

func taskStatus(state *TaskState) string {
	return taskStatusMode(state, false)
}

func suggestedUserMessage(state *TaskState) string {
	if state == nil {
		return ""
	}
	if state.Paused {
		return "/resume"
	}
	switch state.Stage {
	case StagePlanning:
		return "План утверждаю"
	case StageExecution:
		return "Продолжай реализацию по плану"
	case StageValidation:
		return "Покажи результаты проверки"
	case StageDone:
		return "/reset"
	default:
		return ""
	}
}

func taskStatusMode(state *TaskState, thinking bool) string {
	if state == nil {
		if thinking {
			return "Состояние: определяет задачу  ·  Этап: —"
		}
		return "Состояние: нет задачи  ·  Этап: —"
	}
	status := "активна"
	if thinking {
		status = "рассуждает"
	} else if state.Paused {
		status = "пауза"
	} else if state.Stage == StageDone {
		status = "завершена"
	}
	return fmt.Sprintf("Состояние: %s  ·  Этап: %s", status, state.Stage)
}

func cleanOutput(data []byte) []rune {
	result := make([]rune, 0, len(data))
	for _, r := range string(data) {
		switch r {
		case '\n':
			result = append(result, r)
		case '\r':
			// Treat CRLF and progress-style CR as a normal line boundary only
			// when the next LF is absent; ordinary agent output uses LF.
		case '\t':
			result = append(result, ' ', ' ', ' ', ' ')
		default:
			if !unicode.IsControl(r) {
				result = append(result, r)
			}
		}
	}
	return result
}

func wrapLine(line []rune, columns int) []string {
	if columns < 1 {
		columns = 1
	}
	if len(line) == 0 {
		return []string{""}
	}
	result := []string{}
	var part strings.Builder
	cells := 0
	for _, r := range line {
		width := runeCells(r)
		if cells > 0 && cells+width > columns {
			result = append(result, part.String())
			part.Reset()
			cells = 0
		}
		part.WriteRune(r)
		cells += width
	}
	result = append(result, part.String())
	return result
}

func transcriptLines(text []rune, columns int) []string {
	result := []string{}
	start := 0
	for i, r := range text {
		if r != '\n' {
			continue
		}
		result = append(result, wrapLine(text[start:i], columns)...)
		start = i + 1
	}
	result = append(result, wrapLine(text[start:], columns)...)
	return result
}

type editorLayout struct {
	lines       []string
	firstLine   int
	cursorRow   int
	cursorCells int
}

func layoutEditor(text []rune, cursor, width, maxRows int) editorLayout {
	if width < 1 {
		width = 1
	}
	if maxRows < 1 {
		maxRows = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(text) {
		cursor = len(text)
	}
	lines := []string{}
	var part strings.Builder
	cells := 0
	cursorRow, cursorCells := 0, 0
	cursorRecorded := false
	flush := func() {
		lines = append(lines, part.String())
		part.Reset()
		cells = 0
	}
	for index, r := range text {
		if r == '\n' {
			if index == cursor {
				cursorRow, cursorCells, cursorRecorded = len(lines), cells, true
			}
			flush()
			continue
		}
		cellWidth := runeCells(r)
		if cells > 0 && cells+cellWidth > width {
			flush()
		}
		if index == cursor {
			cursorRow, cursorCells, cursorRecorded = len(lines), cells, true
		}
		part.WriteRune(r)
		cells += cellWidth
	}
	if !cursorRecorded {
		cursorRow, cursorCells = len(lines), cells
	}
	flush()
	first := 0
	if len(lines) > maxRows {
		first = cursorRow - maxRows + 1
		if first < 0 {
			first = 0
		}
		if maximum := len(lines) - maxRows; first > maximum {
			first = maximum
		}
		lines = lines[first : first+maxRows]
	}
	return editorLayout{lines: lines, firstLine: first, cursorRow: cursorRow - first, cursorCells: cursorCells}
}

func (t *bottomTerminal) renderLocked() error {
	shown := len(t.editor.options)
	limit := t.rows - 4
	if limit > 6 {
		limit = 6
	}
	if limit < 0 {
		limit = 0
	}
	if shown > limit {
		shown = limit
	}
	optionStart := 0
	if shown > 0 && t.editor.selected >= shown {
		optionStart = t.editor.selected - shown + 1
	}
	maxInputRows := t.rows - 3 - shown
	if maxInputRows > 6 {
		maxInputRows = 6
	}
	layout := layoutEditor(t.editor.text, t.editor.cursor, t.columns-3, maxInputRows)
	inputRows := len(layout.lines)
	statusRow := t.rows - inputRows
	optionFirstRow := statusRow - shown
	available := optionFirstRow - 1
	if available < 1 {
		available = 1
	}
	lines := transcriptLines(t.transcript, t.columns)
	maxOffset := len(lines) - available
	if maxOffset < 0 {
		maxOffset = 0
	}
	if t.scrollOffset > maxOffset {
		t.scrollOffset = maxOffset
	}
	if t.scrollOffset < 0 {
		t.scrollOffset = 0
	}
	end := len(lines) - t.scrollOffset
	start := end - available
	if start < 0 {
		start = 0
	}
	lines = lines[start:end]
	first := available - len(lines) + 1
	var frame strings.Builder
	frame.WriteString("\x1b[?25l")
	for row := 1; row <= available; row++ {
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row)
		index := row - first
		if index >= 0 && index < len(lines) {
			frame.WriteString(lines[index])
		}
	}
	for index := 0; index < shown; index++ {
		row := optionFirstRow + index
		option := t.editor.options[optionStart+index]
		marker := "  "
		if optionStart+index == t.editor.selected {
			marker = "› "
		}
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K%s", row, trimCells(marker+option.Label, t.columns-1))
	}
	status := t.status
	if t.selectionMode {
		status = "Режим выделения мышью  ·  Alt+S — вернуться"
	} else if t.loading {
		status += "  " + spinnerFrames[t.spinnerFrame]
	}
	fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K\x1b[2m%s\x1b[0m", statusRow, trimCells(status, t.columns-1))
	inputStart := statusRow + 1
	for index, line := range layout.lines {
		marker := "│"
		absoluteLine := layout.firstLine + index
		if absoluteLine == 0 {
			marker = "→"
		} else if index == 0 && layout.firstLine > 0 {
			marker = "↑"
		}
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K\x1b[36m%s\x1b[0m ", inputStart+index, marker)
		if len(t.editor.text) == 0 && index == 0 && t.suggestion != "" {
			frame.WriteString("\x1b[2m")
			frame.WriteString(trimCells(t.suggestion+"  [Tab]", t.columns-3))
			frame.WriteString("\x1b[0m")
		} else {
			frame.WriteString(line)
		}
	}
	fmt.Fprintf(&frame, "\x1b[%d;%dH", inputStart+layout.cursorRow, 3+layout.cursorCells)
	frame.WriteString("\x1b[?25h")
	_, err := io.WriteString(t.out, frame.String())
	return err
}

func (t *bottomTerminal) resizeLocked() error {
	columns, rows := t.size()
	if columns < 4 {
		columns = 4
	}
	if rows < 4 {
		rows = 4
	}
	if columns == t.columns && rows == t.rows {
		return nil
	}
	t.columns, t.rows = columns, rows
	return t.renderLocked()
}

func (t *bottomTerminal) drawEditor(editor *lineEditor) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return io.ErrClosedPipe
	}
	t.editor = *editor
	t.editor.text = append([]rune(nil), editor.text...)
	t.editor.options = append([]completion(nil), editor.options...)
	if err := t.resizeLocked(); err != nil {
		return err
	}
	return t.renderLocked()
}

func (t *bottomTerminal) setState(state *TaskState) error {
	return t.setActivity(state, false)
}

func (t *bottomTerminal) setActivity(state *TaskState, thinking bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return io.ErrClosedPipe
	}
	t.status = taskStatusMode(state, thinking)
	if thinking {
		t.suggestion = ""
	} else {
		t.suggestion = suggestedUserMessage(state)
	}
	if thinking && !t.loading {
		t.spinnerFrame = 0
	}
	t.loading = thinking
	if err := t.resizeLocked(); err != nil {
		return err
	}
	return t.renderLocked()
}

func (t *bottomTerminal) suggestedReply() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.suggestion
}

func (t *bottomTerminal) scrollChat(direction int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return io.ErrClosedPipe
	}
	t.scrollOffset += direction * 3
	if t.scrollOffset < 0 {
		t.scrollOffset = 0
	}
	return t.renderLocked()
}

func (t *bottomTerminal) toggleSelectionMode() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return io.ErrClosedPipe
	}
	t.selectionMode = !t.selectionMode
	sequence := "\x1b[?1006l\x1b[?1000l"
	if !t.selectionMode {
		sequence = "\x1b[?1000h\x1b[?1006h"
	}
	if _, err := io.WriteString(t.out, sequence); err != nil {
		return err
	}
	return t.renderLocked()
}

func (t *bottomTerminal) clearChat() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return io.ErrClosedPipe
	}
	t.transcript = nil
	t.scrollOffset = 0
	if err := t.resizeLocked(); err != nil {
		return err
	}
	return t.renderLocked()
}

func (t *bottomTerminal) Write(data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return 0, io.ErrClosedPipe
	}
	cleaned := cleanOutput(data)
	t.transcript = append(t.transcript, cleaned...)
	t.pendingOutput += len(cleaned)
	if len(t.transcript) > transcriptRuneLimit {
		t.transcript = append([]rune(nil), t.transcript[len(t.transcript)-transcriptRuneLimit:]...)
	}
	if err := t.resizeLocked(); err != nil {
		return 0, err
	}
	now := time.Now()
	if t.lastOutput.IsZero() || now.Sub(t.lastOutput) >= outputRenderInterval || t.pendingOutput >= outputRenderBatch {
		if err := t.renderLocked(); err != nil {
			return 0, err
		}
		t.pendingOutput = 0
		t.lastOutput = now
	}
	return len(data), nil
}

func (t *bottomTerminal) Close() error {
	close(t.stop)
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	_, err := io.WriteString(t.out, "\x1b[<u\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[0m\x1b[?1049l")
	return err
}
