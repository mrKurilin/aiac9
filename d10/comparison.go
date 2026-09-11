package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func paneLines(text string, width int) []string {
	text = terminalControls.ReplaceAllString(text, "")
	var lines []string
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\t", "    "), "\n") {
		var line strings.Builder
		cells := 0
		for _, r := range raw {
			w := runeCells(r)
			if w == 0 {
				continue
			}
			if cells+w > width {
				lines = append(lines, line.String())
				line.Reset()
				cells = 0
			}
			line.WriteRune(r)
			cells += w
		}
		lines = append(lines, line.String())
	}
	return lines
}

func padPane(line string, width int) string {
	cells := 0
	for _, r := range line {
		cells += runeCells(r)
	}
	return line + strings.Repeat(" ", maxInt(0, width-cells))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type comparisonPane struct {
	agent  *Agent
	mu     sync.Mutex
	blocks []Message
	status string
}

func (p *comparisonPane) message(role, content string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocks = append(p.blocks, Message{Role: role, Content: content})
	return len(p.blocks) - 1
}

func (p *comparisonPane) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.blocks) == 0 || p.blocks[len(p.blocks)-1].Role != "info" {
		p.blocks = append(p.blocks, Message{Role: "info"})
	}
	p.blocks[len(p.blocks)-1].Content += string(data)
	return len(data), nil
}

// paneTurn is one exchange: the message that opened it and everything this
// panel produced in reply.
type paneTurn struct {
	prompt string
	lines  []string
}

// turns groups a panel's blocks by the message that started them, so both
// panels can be laid out under one shared copy of that message.
func (p *comparisonPane) turns(width int) []paneTurn {
	p.mu.Lock()
	defer p.mu.Unlock()
	var turns []paneTurn
	for _, block := range p.blocks {
		if block.Role == "user" {
			turns = append(turns, paneTurn{prompt: block.Content})
			continue
		}
		if len(turns) == 0 {
			turns = append(turns, paneTurn{})
		}
		current := &turns[len(turns)-1]
		if block.Role == "assistant" {
			current.lines = append(current.lines, paneLines("МОДЕЛЬ", width)...)
		}
		current.lines = append(current.lines, paneLines(block.Content, width)...)
		current.lines = append(current.lines, "")
	}
	return turns
}

// Both panels receive the same prompt, so it is drawn once across the full
// width instead of twice in the columns: centred, in blue, as the divider
// between two turns of the comparison.
func promptBand(prompt string, full int, styled bool) []string {
	blue := func(line string) string {
		if !styled || line == "" {
			return line
		}
		return "\x1b[1;34m" + line + "\x1b[0m"
	}
	centre := func(line string) string {
		cells := 0
		for _, r := range line {
			cells += runeCells(r)
		}
		return strings.Repeat(" ", maxInt(0, full-cells)/2) + line
	}
	label := " ВЫ "
	dashes := maxInt(0, full-len([]rune(label)))
	band := []string{blue(strings.Repeat("─", dashes/2) + label + strings.Repeat("─", dashes-dashes/2))}
	for _, line := range paneLines(prompt, maxInt(2, full-4)) {
		band = append(band, blue(centre(line)))
	}
	return append(band, "")
}

func (p *comparisonPane) refresh() {
	a := p.agent
	a.mu.Lock()
	s := a.snapshot()
	used := estimateMessages(a.contextMessages())
	a.mu.Unlock()
	meter, _ := a.client.(*Meter)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", strategyState(s))
	fmt.Fprintf(&b, "Контекст ≈%d · вход %d · выход %d · API %d · $%.6f", used, meter.Input, meter.Output, meter.Turns, meter.Cost)
	p.mu.Lock()
	p.status = b.String()
	p.mu.Unlock()
}

type comparisonView struct {
	panes       [2]*comparisonPane
	out         io.Writer
	interactive bool
	styled      bool
	mu          sync.Mutex // render also runs from the terminal's redraw hook
	previous    []string
	lastColumns int
	lastRows    int
	// Rows the dialogue is lifted by, and the height of one screenful as the
	// last frame measured it.
	scroll       int
	lastBodyRows int
}

// scrollBy lifts the dialogue by whole rows; render clamps it to the transcript
// that actually exists, so overshooting simply stops at the first message.
func (v *comparisonView) scrollBy(rows int) {
	v.mu.Lock()
	v.scroll = maxInt(0, v.scroll+rows)
	v.mu.Unlock()
	v.render()
}

// scrollPages moves by screenfuls: 1 is a page back through the dialogue.
func (v *comparisonView) scrollPages(pages int) {
	v.mu.Lock()
	page := maxInt(1, v.lastBodyRows-1)
	v.scroll = maxInt(0, v.scroll+pages*page)
	v.mu.Unlock()
	v.render()
}

func (v *comparisonView) dim(line string) string {
	if !v.styled || strings.TrimSpace(line) == "" {
		return line
	}
	return "\x1b[2m" + line + "\x1b[0m"
}

func (v *comparisonView) scrollToBottom() {
	v.mu.Lock()
	v.scroll = 0
	v.mu.Unlock()
	v.render()
}

// invalidate drops the painted frame, so the next render rewrites every row.
// The footer erases the rows it grows over; without this the panel would keep
// believing that area still holds the messages it painted there.
func (v *comparisonView) invalidate() {
	v.mu.Lock()
	v.previous = nil
	v.mu.Unlock()
}

func newComparisonView(left, right *Agent, out io.Writer) *comparisonView {
	_, noColor := os.LookupEnv("NO_COLOR")
	v := &comparisonView{out: out, interactive: terminalOutput(out), styled: terminalOutput(out) && !noColor}
	for i, agent := range []*Agent{left, right} {
		pane := &comparisonPane{agent: agent}
		v.panes[i] = pane
		for _, message := range agent.History() {
			pane.message(message.Role, message.Content)
		}
		pane.refresh()
	}
	return v
}

func (v *comparisonView) rawOutput() io.Writer {
	if terminal, ok := v.out.(*bottomTerminal); ok {
		return terminal.out
	}
	return v.out
}

func (v *comparisonView) ask(ctx context.Context, prompt string) error {
	// A new turn belongs at the bottom: never hide the answer being streamed.
	v.mu.Lock()
	v.scroll = 0
	v.mu.Unlock()
	done := make(chan error, 2)
	for _, pane := range v.panes {
		pane.message("user", prompt)
		answerIndex := pane.message("assistant", "Ожидание модели…")
		go func(pane *comparisonPane) {
			var report bytes.Buffer
			if meter, ok := pane.agent.client.(*Meter); ok {
				meter.Out, meter.Report = &report, &report
			}
			started := false
			_, err := pane.agent.AskStream(ctx, prompt, func(chunk string) error {
				pane.mu.Lock()
				defer pane.mu.Unlock()
				if !started {
					pane.blocks[answerIndex].Content = ""
					started = true
				}
				pane.blocks[answerIndex].Content += chunk
				return nil
			})
			if err != nil {
				pane.Write([]byte("Ошибка: " + err.Error() + "\n"))
			}
			if warning := pane.agent.DrainFactsWarning(); warning != "" {
				pane.Write([]byte("Facts не обновлены, ответ по прежним фактам: " + warning + "\n"))
			}
			if meter, ok := pane.agent.client.(*Meter); ok {
				meter.Out, meter.Report = nil, nil
			}
			pane.refresh()
			done <- err
		}(pane)
	}
	var result error
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for pending := 2; pending > 0; {
		select {
		case err := <-done:
			result = errors.Join(result, err)
			pending--
		case <-ticker.C:
			v.render()
		}
	}
	v.render()
	return result
}

func (v *comparisonView) command(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false
	}
	if fields[0] == "/exit" || fields[0] == "/quit" {
		return false
	}
	switch fields[0] {
	case "/scroll":
		rows := 10
		if len(fields) == 2 {
			value, err := strconv.Atoi(fields[1])
			if err != nil {
				for _, pane := range v.panes {
					pane.Write([]byte("Использование: /scroll N — выше на N строк, /scroll -N — ниже.\n"))
				}
				v.render()
				return true
			}
			rows = value
		}
		v.scrollBy(rows)
		return true
	case "/bottom":
		v.scrollToBottom()
		return true
	}
	if fields[0] == "/contextManagementStrategy" {
		for _, pane := range v.panes {
			pane.Write([]byte("Панели фиксированы для сравнения: слева Sliding Window, справа Sticky Facts.\n"))
		}
		v.render()
		return true
	}
	for _, pane := range v.panes {
		switch fields[0] {
		case "/reset":
			err := pane.agent.Reset()
			pane.mu.Lock()
			pane.blocks = nil
			pane.mu.Unlock()
			// Say so: an empty panel is otherwise indistinguishable from a
			// display that lost its contents.
			if err != nil {
				pane.Write([]byte("Ошибка очистки памяти: " + err.Error() + "\n"))
			} else {
				pane.Write([]byte("Память очищена: история, ветки и checkpoints. Расход за запуск сохранён.\n"))
			}
		case "/stats", "/facts", "/memory", "/context", "/info", "/fill", "/overflow", "/checkpoint", "/addBranch", "/branch":
			if !contextCommand(pane.agent, line, pane) && !memoryCommand(pane.agent, line, pane) {
				pane.Write([]byte("Неизвестная команда. /info — справка.\n"))
			}
		default:
			pane.Write([]byte("Неизвестная команда. /info — справка.\n"))
		}
		pane.refresh()
	}
	v.render()
	return true
}

func (v *comparisonView) render() {
	v.mu.Lock()
	defer v.mu.Unlock()
	columns, rows := terminalSize(v.out)
	if terminal, ok := v.out.(*bottomTerminal); ok {
		terminal.mu.Lock()
		rows = terminal.top - 1
		columns = terminal.columns
		terminal.mu.Unlock()
	}
	width := maxInt(2, (columns-4)/2)
	full := width*2 + 3
	var turns [2][]paneTurn
	var status [2][]string
	for i, pane := range v.panes {
		turns[i] = pane.turns(width)
		pane.mu.Lock()
		status[i] = paneLines(strings.TrimSpace(pane.status), width)
		pane.mu.Unlock()
	}
	var body []string
	for i := 0; i < maxInt(len(turns[0]), len(turns[1])); i++ {
		var pair [2]paneTurn
		for side := range turns {
			if i < len(turns[side]) {
				pair[side] = turns[side][i]
			}
		}
		prompt := pair[0].prompt
		if prompt == "" {
			prompt = pair[1].prompt
		}
		if prompt != "" {
			body = append(body, promptBand(prompt, full, v.styled)...)
		}
		for j := 0; j < maxInt(len(pair[0].lines), len(pair[1].lines)); j++ {
			var columns [2]string
			for side := range pair {
				if j < len(pair[side].lines) {
					columns[side] = pair[side].lines[j]
				}
			}
			body = append(body, padPane(columns[0], width)+" │ "+padPane(columns[1], width))
		}
	}
	statusRows := maxInt(len(status[0]), len(status[1]))
	// Cover the whole region above the footer: a row the frame never writes
	// keeps whatever was painted there before.
	bodyRows := maxInt(1, rows-statusRows-2)
	var lines []string
	// Headers, the rule and the statistics are chrome around the dialogue: dim
	// keeps them from reading like the input line or the answers.
	row := func(left, right string) {
		lines = append(lines, v.dim(padPane(left, width)+" │ "+padPane(right, width)))
	}
	row("SLIDING WINDOW", "STICKY FACTS / KEY-VALUE MEMORY")
	// Keep the conversation against the separator: a short dialogue leaves the
	// gap above it, a long one shows its tail — or an earlier screenful when
	// scrolled back.
	v.lastBodyRows = bodyRows
	if limit := maxInt(0, len(body)-bodyRows); v.scroll > limit {
		v.scroll = limit
	}
	start := maxInt(0, len(body)-bodyRows-v.scroll)
	blank := maxInt(0, bodyRows-len(body))
	for i := 0; i < bodyRows; i++ {
		if i < blank {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, body[start+i-blank])
	}
	row(strings.Repeat("─", width), strings.Repeat("─", width))
	for i := 0; i < statusRows; i++ {
		var pair [2]string
		for side := range status {
			if i < len(status[side]) {
				pair[side] = status[side][i]
			}
		}
		row(pair[0], pair[1])
	}
	if !v.interactive {
		fmt.Fprintln(v.out, strings.Join(lines, "\n"))
		return
	}
	v.paint(lines, columns, rows)
}

func (v *comparisonView) paint(lines []string, columns, rows int) {
	// A resized region moves every row, so trusting the previous frame there
	// would leave stale copies of old status lines on screen.
	repaint := v.lastColumns != columns || v.lastRows != rows || len(v.previous) != len(lines)
	var update strings.Builder
	for i, line := range lines {
		if repaint || i >= len(v.previous) || v.previous[i] != line {
			fmt.Fprintf(&update, "\x1b[%d;1H%s\x1b[K", i+1, line)
		}
	}
	for i := len(lines); i < len(v.previous); i++ {
		fmt.Fprintf(&update, "\x1b[%d;1H\x1b[K", i+1)
	}
	if update.Len() == 0 {
		return
	}
	out := v.rawOutput()
	// Park the cursor where the typist expects it. Without a footer the panel
	// owns the screen, so its own last row is the only sensible place.
	tail := fmt.Sprintf("\x1b[%d;1H", rows)
	if terminal, ok := v.out.(*bottomTerminal); ok {
		terminal.mu.Lock()
		defer terminal.mu.Unlock()
		// The footer may have moved while this frame was being built: it just
		// erased rows this frame no longer covers, and painting the stale
		// layout would leave them blank. Its move hook repaints instead.
		if terminal.columns != columns || terminal.top-1 != rows {
			return
		}
		tail = terminal.footerCursor()
	}
	fmt.Fprintf(out, "\x1b[?25l%s%s\x1b[?25h", update.String(), tail)
	v.previous, v.lastColumns, v.lastRows = lines, columns, rows
}
