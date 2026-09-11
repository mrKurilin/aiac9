package main

import (
	"fmt"
	"io"
	"strings"
)

type completion struct {
	Label, Value string
	Submit       bool
}
type completer func(string) []completion

var slashCommands = []completion{
	{"/contextManagementStrategy — управление контекстом", "/contextManagementStrategy ", false},
	{"/addBranch <name> — создать и открыть ветку", "/addBranch ", false},
	{"/branch — выбрать ветку", "/branch ", false},
	{"/checkpoint <name> — сохранить точку ветвления", "/checkpoint ", false},
	{"/memory — состояние памяти", "/memory", true},
	{"/facts — актуальные факты", "/facts", true},
	{"/context — заполнение окна", "/context", true},
	{"/stats — токены и стоимость", "/stats", true},
	{"/sendNext — следующее сообщение сценария", "/sendNext", true},
	{"/scroll N — выше на N строк, -N — ниже", "/scroll ", false},
	{"/bottom — вернуться к концу диалога", "/bottom", true},
	{"/fill N — учебный наполнитель", "/fill ", false},
	{"/overflow on|off — контроль переполнения", "/overflow ", false},
	{"/reset — очистить всю сессию", "/reset", true},
	{"/info — справка", "/info", true},
	{"/exit — выйти", "/exit", true},
}

func agentCompleter(a *Agent) completer {
	return func(text string) []completion {
		if !strings.HasPrefix(text, "/") {
			return nil
		}
		fields := strings.Fields(text)
		if len(fields) == 0 {
			return nil
		}
		command := fields[0]
		choices := []completion{}
		switch command {
		case "/contextManagementStrategy":
			choices = []completion{
				{"Sliding Window", command + " window", true},
				{"Sticky Facts / Key-Value Memory", command + " facts", true},
			}
		case "/branch":
			a.mu.Lock()
			active := a.active
			a.mu.Unlock()
			for _, name := range a.branchNames() {
				label := name
				if name == active {
					label += " (активная)"
				}
				choices = append(choices, completion{label, command + " " + name, true})
			}
		case "/overflow":
			choices = []completion{{"on — отправлять сверх локальной оценки", command + " on", true}, {"off — блокировать переполнение", command + " off", true}}
		default:
			if strings.ContainsAny(text, " \t") {
				return nil
			}
			choices = slashCommands
		}
		matches := []completion{}
		for _, choice := range choices {
			if strings.HasPrefix(strings.ToLower(choice.Value), strings.ToLower(text)) {
				matches = append(matches, choice)
			}
		}
		return matches
	}
}

func (e *lineEditor) refreshCompletions() {
	if e.complete == nil || e.dismissed {
		e.options = nil
		return
	}
	e.options = e.complete(string(e.text))
	if e.selected >= len(e.options) {
		e.selected = 0
	}
}
func (e *lineEditor) acceptCompletion(submit bool) bool {
	if len(e.options) == 0 {
		return submit
	}
	choice := e.options[e.selected]
	e.text = []rune(choice.Value)
	e.selected = 0
	e.dismissed = false
	e.refreshCompletions()
	return submit && choice.Submit
}
func (e *lineEditor) dismissMenu() {
	e.escape = ""
	e.dismissed = true
	e.options = nil
	e.selected = 0
}

func menuPrefix(text string, cells int) string {
	if cells <= 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range text {
		width := runeCells(r)
		if width > cells {
			break
		}
		cells -= width
		b.WriteRune(r)
	}
	return b.String()
}

// Draw below the input, then return to its cursor. Limit the visible slice so
// long command/branch lists scroll with the selection even in small terminals.
func renderInputMenu(out io.Writer, e *lineEditor, oldRows int) (int, error) {
	columns, rows := terminalSize(out)
	limit := rows - 4
	if limit > 6 {
		limit = 6
	}
	if limit < 1 {
		limit = 1
	}
	shown := len(e.options)
	if shown > limit {
		shown = limit
	}
	start := 0
	if e.selected >= shown && shown > 0 {
		start = e.selected - shown + 1
	}
	newRows := 0
	if shown > 0 {
		newRows = shown + 1
	}
	eraseRows := oldRows
	if newRows > eraseRows {
		eraseRows = newRows
	}
	var frame strings.Builder
	frame.WriteString("\r\x1b[2K│ ")
	visible := inputViewport(e.text, columns-3)
	frame.WriteString(terminalControls.ReplaceAllString(visible, ""))
	for row := 0; row < eraseRows; row++ {
		frame.WriteString("\n\r\x1b[2K")
		if row < shown {
			marker := "  "
			if start+row == e.selected {
				marker = "› "
			}
			frame.WriteString(menuPrefix(marker+e.options[start+row].Label, columns-1))
		} else if row == shown && shown > 0 {
			frame.WriteString(menuPrefix(fmt.Sprintf("↑/↓ выбор · Tab дополнить · Enter выбрать · Esc закрыть (%d/%d)", e.selected+1, len(e.options)), columns-1))
		}
	}
	if eraseRows > 0 {
		fmt.Fprintf(&frame, "\x1b[%dA", eraseRows)
	}
	frame.WriteString("\r")
	cursor := 2
	for _, r := range visible {
		cursor += runeCells(r)
	}
	if cursor > 0 {
		fmt.Fprintf(&frame, "\x1b[%dC", cursor)
	}
	_, err := io.WriteString(out, frame.String())
	return newRows, err
}
