package main

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// A small VT screen interprets cursor addressing, erasure and scrolling regions.
// Tests assert visible rows after long output, not just emitted escape strings.
type testScreen struct {
	mu                                                 sync.Mutex
	cells                                              [][]rune
	columns, row, col, top, bottom, savedRow, savedCol int
}

func newTestScreen(columns, rows int) *testScreen {
	s := &testScreen{}
	s.resize(columns, rows)
	return s
}
func (s *testScreen) resize(columns, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make([][]rune, rows)
	for i := range next {
		next[i] = make([]rune, columns)
		for j := range next[i] {
			next[i][j] = ' '
		}
		if i < len(s.cells) {
			copy(next[i], s.cells[i])
		}
	}
	s.cells = next
	s.columns = columns
	s.top = 0
	s.bottom = rows - 1
	if s.row >= rows {
		s.row = rows - 1
	}
	if s.col >= columns {
		s.col = columns - 1
	}
}
func (s *testScreen) size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.columns, len(s.cells)
}
func (s *testScreen) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, row := range s.cells {
		out = append(out, strings.TrimRight(string(row), " "))
	}
	return out
}
func (s *testScreen) newline() {
	s.col = 0
	if s.row == s.bottom {
		for row := s.top; row < s.bottom; row++ {
			copy(s.cells[row], s.cells[row+1])
		}
		for col := range s.cells[s.bottom] {
			s.cells[s.bottom][col] = ' '
		}
	} else if s.row < len(s.cells)-1 {
		s.row++
	}
}
func (s *testScreen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runes := []rune(string(p))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == 27 && i+1 < len(runes) {
			i++
			switch runes[i] {
			case '7':
				s.savedRow, s.savedCol = s.row, s.col
			case '8':
				s.row, s.col = s.savedRow, s.savedCol
			case '[':
				start := i + 1
				i++
				for i < len(runes) && (runes[i] < '@' || runes[i] > '~') {
					i++
				}
				if i == len(runes) {
					break
				}
				args := strings.Split(string(runes[start:i]), ";")
				n := func(index, fallback int) int {
					if index >= len(args) || args[index] == "" {
						return fallback
					}
					v, _ := strconv.Atoi(args[index])
					return v
				}
				switch runes[i] {
				case 'H':
					s.row = n(0, 1) - 1
					s.col = n(1, 1) - 1
				case 'r':
					s.top = n(0, 1) - 1
					s.bottom = n(1, len(s.cells)) - 1
					s.row, s.col = 0, 0
				case 'K':
					// Plain CSI K erases from the cursor rightward; only CSI 2 K
					// clears the whole row.
					from := s.col
					if n(0, 0) == 2 {
						from = 0
					}
					for col := from; col < len(s.cells[s.row]); col++ {
						s.cells[s.row][col] = ' '
					}
				case 'J':
					if n(0, 0) == 2 {
						for row := range s.cells {
							for col := range s.cells[row] {
								s.cells[row][col] = ' '
							}
						}
					}
				}
			}
			continue
		}
		switch r {
		case '\r':
			s.col = 0
		case '\n':
			s.newline()
		default:
			if s.col >= s.columns {
				s.newline()
			}
			if s.row >= len(s.cells) {
				s.row = len(s.cells) - 1
			}
			s.cells[s.row][s.col] = r
			s.col++
		}
	}
	return len(p), nil
}
func TestBottomInputSurvivesStreamingAndMenus(t *testing.T) {
	screen := newTestScreen(80, 24)
	term, err := openBottomTerminal(screen, screen.size)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	a := NewAgent(&terminalFake{}, "", nil, "footer")
	e := lineEditor{complete: agentCompleter(a)}
	for _, r := range "/contextManagementStrategy" {
		e.feed(r)
	}
	if err := term.drawEditor(&e); err != nil {
		t.Fatal(err)
	}
	before := screen.lines()
	if term.top != 22 {
		t.Fatalf("two suggestions reserved too much space: footer starts at row %d", term.top)
	}
	if before[23] != "│ /contextManagementStrategy" || !strings.Contains(before[21], "Sliding Window") || !strings.Contains(before[22], "Sticky Facts") {
		t.Fatalf("menu not above bottom input: %+v", before)
	}
	for i := 0; i < 60; i++ {
		fmt.Fprintf(term, "answer %d\n", i)
	}
	fmt.Fprint(term, "frag")
	fmt.Fprint(term, "mented\n")
	after := screen.lines()
	if strings.Join(before[21:], "\n") != strings.Join(after[21:], "\n") {
		t.Fatal("streaming scrolled or overwrote input/menu")
	}
	if !strings.Contains(strings.Join(after[:21], "\n"), "fragmented") {
		t.Fatal("streaming lost its transcript cursor")
	}
	e.dismissMenu()
	if err := term.drawEditor(&e); err != nil {
		t.Fatal(err)
	}
	after = screen.lines()
	if term.top != 23 {
		t.Fatalf("closed menu should reserve two rows, footer starts at row %d", term.top)
	}
	if after[21] != "" || !strings.Contains(after[22], "/ — команды") || after[23] != before[23] {
		t.Fatal("dismiss left menu remnants or moved input")
	}
}
func TestBottomInputResizeResetAndExit(t *testing.T) {
	screen := newTestScreen(80, 24)
	term, err := openBottomTerminal(screen, screen.size)
	if err != nil {
		t.Fatal(err)
	}
	e := lineEditor{text: []rune("draft")}
	if err := term.drawEditor(&e); err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{100, 32}, {40, 12}, {20, 3}, {80, 24}} {
		screen.resize(size[0], size[1])
		term.mu.Lock()
		err = term.resizeLocked()
		term.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		rows := screen.lines()
		if rows[len(rows)-1] != "│ draft" {
			t.Fatalf("input detached after resize %v: %+v", size, rows)
		}
		for i := 0; i < 20; i++ {
			fmt.Fprintln(term, "output")
		}
		rows = screen.lines()
		if rows[len(rows)-1] != "│ draft" {
			t.Fatal("resized scroll region includes footer")
		}
	}
	if err := clearConversationView(term, true); err != nil {
		t.Fatal(err)
	}
	rows := screen.lines()
	if rows[23] != "│ draft" {
		t.Fatal("reset cleared input")
	}
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	if screen.top != 0 || screen.bottom != 23 {
		t.Fatal("exit did not restore scroll region")
	}
}
func TestBottomEditorSubmitsWithoutMovingInputIntoTranscript(t *testing.T) {
	screen := newTestScreen(80, 24)
	term, err := openBottomTerminal(screen, screen.size)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	a := NewAgent(&terminalFake{}, "", nil, "editor")
	line, err := readEditedLineCompletions(bufio.NewReader(strings.NewReader("/\n\x1b[B\n")), term, false, true, agentCompleter(a))
	if err != nil || line != "/contextManagementStrategy facts" {
		t.Fatalf("%q %v", line, err)
	}
	rows := screen.lines()
	if rows[23] != "│" {
		t.Fatalf("submitted input not retained as bottom field: %+v", rows)
	}
	if strings.Contains(strings.Join(rows[:16], "\n"), "contextManagement") {
		t.Fatal("editor drawing leaked into transcript")
	}
}
