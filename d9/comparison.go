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

type comparisonPane struct {
	agent    *Agent
	mu       sync.Mutex
	status   string // Updated only between requests; rendering never reads a running Meter.
	requests string
	blocks   []Message
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

func (p *comparisonPane) message(role, content string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocks = append(p.blocks, Message{Role: role, Content: content})
	return len(p.blocks) - 1
}

func (p *comparisonPane) lines(width int) []string {
	var lines []string
	for _, b := range p.blocks {
		prefix, title := "", ""
		switch b.Role {
		case "user":
			prefix, title = "┃ ", "┃ ВЫ"
		case "assistant":
			title = "МОДЕЛЬ"
		}
		if title != "" {
			lines = append(lines, paneLines(title, width)...)
		}
		for _, line := range paneLines(b.Content, maxInt(2, width-len([]rune(prefix)))) {
			lines = append(lines, prefix+line)
		}
		lines = append(lines, "")
	}
	return lines
}

func (p *comparisonPane) refresh() {
	a := p.agent
	s := a.CompressionSnapshot()
	var status strings.Builder
	if s.Config.Enabled {
		fmt.Fprintf(&status, "Сжатий %d · пакет %d/%d · хвост %d\n", s.Stats.Runs, s.PendingToBatch, s.Config.SummaryEvery, s.Config.KeepLast)
		fmt.Fprintf(&status, "Сжатие ≈%d → %d (−%d)\n", s.Stats.LastBefore, s.Stats.LastAfter, s.Stats.LastBefore-s.Stats.LastAfter)
	} else {
		fmt.Fprintln(&status, "Сжатие off · полная история\nСжатий 0")
	}
	if m, ok := a.client.(*Meter); ok {
		a.mu.Lock()
		used := estimateMessages(a.contextMessages())
		system := estimateMessages([]Message{{Role: "system", Content: a.system}})
		dialog := estimateMessages(a.history)
		a.mu.Unlock()
		window := "окно неизвестно"
		if m.ContextLimit > 0 {
			window = fmt.Sprintf("/ %d (%.2f%%)", m.ContextLimit, 100*float64(used)/float64(m.ContextLimit))
		}
		fmt.Fprintf(&status, "Контекст ≈%d %s\n", used, window)
		fmt.Fprintf(&status, "Последний: ↑%d ↓%d [%s]\n", m.LastUsage.Prompt, m.LastUsage.Completion, m.LastSource)
		fmt.Fprintf(&status, "Всего: ↑%d ↓%d · API %d\n", m.Input, m.Output, m.Turns)
		fmt.Fprintf(&status, "Стоимость $%.6f · кеш %d\n", m.Cost, m.CacheHits)
		fmt.Fprintf(&status, "System ≈%d · summary ≈%d\n", system, estimateText(s.Summary))
		fmt.Fprintf(&status, "История ≈%d · сообщений %d\n", dialog, s.RawMessages)
		fmt.Fprintf(&status, "Резерв %d · оценки %d · сбои %d\n", m.Reserve, m.Estimated, m.Unknown)
	}
	p.mu.Lock()
	p.status = status.String()
	p.mu.Unlock()
}

type comparisonView struct {
	panes       [2]*comparisonPane
	out         io.Writer
	interactive bool
	scroll      int
	previous    []string
	lastColumns int
}

func newComparisonView(left, right *Agent, out io.Writer) *comparisonView {
	v := &comparisonView{out: out, interactive: terminalOutput(out)}
	for i, a := range []*Agent{left, right} {
		p := &comparisonPane{agent: a}
		v.panes[i] = p
		if s := a.CompressionSnapshot(); s.Summary != "" {
			fmt.Fprintln(p, "Сохранённое summary:", s.Summary)
		}
		for _, m := range a.History() {
			if strings.HasPrefix(m.Content, contextFillerPrefix) {
				fmt.Fprintf(p, "[%s: наполнитель ≈%d токенов]\n", m.Role, estimateText(m.Content))
			} else {
				p.message(m.Role, m.Content)
			}
		}
		p.refresh()
	}
	return v
}

func (v *comparisonView) ask(ctx context.Context, prompt string) error {
	v.scroll = 0
	done := make(chan error, 2)
	for _, p := range v.panes {
		p.message("user", prompt)
		answerIndex := p.message("assistant", "Ожидание модели…")
		go func(p *comparisonPane) {
			var report bytes.Buffer
			if m, ok := p.agent.client.(*Meter); ok {
				m.Out, m.Report = &report, &report
			}
			started := false
			_, err := p.agent.AskStream(ctx, prompt, func(chunk string) error {
				p.mu.Lock()
				defer p.mu.Unlock()
				if !started {
					p.blocks[answerIndex].Content = ""
					started = true
				}
				p.blocks[answerIndex].Content += chunk
				return nil
			})
			if err != nil {
				fmt.Fprintln(p, "Ошибка:", err)
			}
			if warning := p.agent.DrainCompressionWarning(); warning != "" {
				fmt.Fprintln(p, "Сжатие не выполнено:", warning)
			}
			if m, ok := p.agent.client.(*Meter); ok {
				m.Out, m.Report = nil, nil
			}
			p.mu.Lock()
			p.requests = report.String()
			p.mu.Unlock()
			p.refresh()
			done <- err
		}(p)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var result error
	for pending := 2; pending > 0; {
		select {
		case err := <-done:
			result = errors.Join(result, err)
			pending--
		case <-ticker.C:
			if v.interactive {
				v.render()
			}
		}
	}
	v.render()
	return result
}

func (v *comparisonView) command(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	name := fields[0]
	switch name {
	case "/scroll":
		amount := 10
		if len(fields) == 2 {
			var err error
			amount, err = strconv.Atoi(fields[1])
			if err != nil || amount < -1000000 || amount > 1000000 {
				amount = 0
			}
		}
		v.scroll += amount
		if v.scroll < 0 {
			v.scroll = 0
		}
		v.render()
		return true
	case "/bottom":
		v.scroll = 0
		v.render()
		return true
	case "/exit", "/quit":
		return false
	}
	if !strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "+") {
		return false
	}
	for _, p := range v.panes {
		switch name {
		case "/reset":
			p.agent.Reset()
			p.mu.Lock()
			p.blocks = nil
			p.mu.Unlock()
			fmt.Fprintln(p, "Память очищена; расход за запуск сохранён.")
		case "/stats":
			if m, ok := p.agent.client.(*Meter); ok {
				m.Summary(p)
			}
		case "/compress":
			fmt.Fprintln(p, "В сравнении режимы фиксированы: слева off, справа on. Одиночный режим: -compare=false.")
		case "/summary":
			fmt.Fprintln(p, "Summary:", p.agent.CompressionSnapshot().Summary)
		case "/requests":
			fmt.Fprintln(p, p.requests)
		case "/info":
			fmt.Fprintln(p, "/context /memory /summary /stats /requests /fill N /overflow on|off /reset /exit\n/requests — детали последнего хода, включая вызов сжатия.\n/scroll N — выше на N строк; /scroll -N — ниже; /bottom — конец.\nОбщий ввод отправляется обеим моделям; Esc отменяет запросы.\nСчётчики включают создание summary. ≈ — оценка, API usage — фактические токены.")
		default:
			if !contextCommand(p.agent, line, p) && !memoryCommand(p.agent, line, p) {
				fmt.Fprintln(p, "Неизвестная команда. /info — справка.")
			}
		}
		p.refresh()
	}
	v.scroll = 0
	v.render()
	return true
}

// Strip terminal controls before laying out untrusted model/user text.
func paneLines(text string, width int) []string {
	text = terminalControls.ReplaceAllString(text, "")
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\t", "    "), "\n") {
		var b strings.Builder
		cells := 0
		for _, r := range line {
			n := runeCells(r)
			if n == 0 {
				continue
			}
			if cells+n > width {
				lines = append(lines, b.String())
				b.Reset()
				cells = 0
			}
			b.WriteRune(r)
			cells += n
		}
		lines = append(lines, b.String())
	}
	return lines
}

func padPane(s string, width int) string {
	cells := 0
	for _, r := range s {
		cells += runeCells(r)
	}
	return s + strings.Repeat(" ", maxInt(0, width-cells))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (v *comparisonView) render() {
	columns, rows := terminalSize(v.out)
	width := maxInt(2, (columns-4)/2)
	var body, status [2][]string
	for i, p := range v.panes {
		p.mu.Lock()
		body[i] = p.lines(width)
		status[i] = paneLines(strings.TrimSpace(p.status), width)
		p.mu.Unlock()
	}
	statusRows := maxInt(len(status[0]), len(status[1]))
	bodyRows := maxInt(len(body[0]), len(body[1]))
	if v.interactive {
		// Leave the final row for the shared input editor. On a short terminal,
		// detailed metrics remain accessible through /context, /memory, /stats.
		// Use only the actual wrapped statistics height, preserving at least
		// one dialogue row on very short terminals.
		if available := maxInt(1, rows-5); statusRows > available {
			statusRows = available
		}
		bodyRows = maxInt(1, rows-statusRows-4)
	}
	var frame strings.Builder
	row := func(left, right string) {
		style := func(s string) string {
			padded := padPane(s, width)
			_, noColor := os.LookupEnv("NO_COLOR")
			if v.interactive && !noColor && strings.HasPrefix(s, "┃") {
				return "\x1b[1;36m" + padded + "\x1b[0m"
			}
			return padded
		}
		fmt.Fprintf(&frame, "%s │ %s\n", style(left), style(right))
	}
	row(paneLines("БЕЗ СЖАТИЯ", width)[0], paneLines("СО СЖАТИЕМ", width)[0])
	for i := 0; i < bodyRows; i++ {
		var pair [2]string
		for side := range body {
			start := 0
			if v.interactive {
				start = maxInt(0, len(body[side])-bodyRows-v.scroll)
			}
			if start+i < len(body[side]) {
				pair[side] = body[side][start+i]
			}
		}
		row(pair[0], pair[1])
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
	help := "/info · /scroll N · /bottom · /exit | общий ввод → обеим моделям"
	frame.WriteString(paneLines(help, maxInt(2, columns-1))[0] + "\n")
	if !v.interactive {
		io.WriteString(v.out, frame.String())
		return
	}
	v.paint(strings.Split(strings.TrimSuffix(frame.String(), "\n"), "\n"), columns, rows)
}

// Absolute cursor positions avoid linefeeds/scrolling; unchanged rows produce no output.
func (v *comparisonView) paint(lines []string, columns, rows int) {
	var update strings.Builder
	for i, line := range lines {
		if v.lastColumns != columns || len(v.previous) != len(lines) || i >= len(v.previous) || v.previous[i] != line {
			fmt.Fprintf(&update, "\x1b[%d;1H%s\x1b[K", i+1, line)
		}
	}
	if update.Len() > 0 {
		fmt.Fprintf(v.out, "\x1b[?25l%s\x1b[%d;1H\x1b[?25h", update.String(), rows)
	}
	v.previous, v.lastColumns = lines, columns
}
