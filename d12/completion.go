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
	{"/remember <layer> <key> <value> — сохранить факт", "/remember ", false},
	{"/forget <layer> <key> — удалить факт", "/forget ", false},
	{"/memory [layer] — показать память", "/memory", true},
	{"/clear <layer|all> — очистить память", "/clear ", false},
	{"/reset — очистить диалог и всю память", "/reset", true},
	{"/info — справка", "/info", true},
	{"/exit — выйти", "/exit", true},
}

func memoryCompleter(_ *Agent) completer {
	return func(text string) []completion {
		if !strings.HasPrefix(text, "/") {
			return nil
		}
		fields := strings.Fields(text)
		if len(fields) == 0 {
			return nil
		}
		command := fields[0]
		choices := slashCommands
		if strings.ContainsAny(text, " \t") {
			choices = nil
			if (command == "/remember" || command == "/forget") && len(fields) >= 2 && strings.HasSuffix(text, " ") {
				return nil
			}
			layers := []string{"short", "working", "long"}
			submit := command == "/memory" || command == "/clear"
			if command == "/clear" {
				layers = append(layers, "all")
			}
			if command == "/remember" || command == "/forget" || command == "/memory" || command == "/clear" {
				for _, layer := range layers {
					value := command + " " + layer
					if command == "/remember" || command == "/forget" {
						value += " "
					}
					choices = append(choices, completion{layer + " — слой памяти", value, submit})
				}
			}
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

func menuPrefix(value string, cells int) string {
	if cells <= 0 {
		return ""
	}
	var result strings.Builder
	for _, r := range value {
		width := runeCells(r)
		if width > cells {
			break
		}
		cells -= width
		result.WriteRune(r)
	}
	return result.String()
}

func renderInputMenu(out io.Writer, editor *lineEditor, oldRows int) (int, error) {
	columns, rows := terminalSize(out)
	limit := rows - 4
	if limit > 6 {
		limit = 6
	}
	if limit < 1 {
		limit = 1
	}
	shown := len(editor.options)
	if shown > limit {
		shown = limit
	}
	start := 0
	if editor.selected >= shown && shown > 0 {
		start = editor.selected - shown + 1
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
	visible := inputViewport(editor.text, columns-3)
	frame.WriteString(terminalControls.ReplaceAllString(visible, ""))
	for row := 0; row < eraseRows; row++ {
		frame.WriteString("\n\r\x1b[2K")
		if row < shown {
			marker := "  "
			if start+row == editor.selected {
				marker = "› "
			}
			frame.WriteString(menuPrefix(marker+editor.options[start+row].Label, columns-1))
		} else if row == shown && shown > 0 {
			frame.WriteString(menuPrefix(fmt.Sprintf("↑/↓ выбор · Tab дополнить · Enter выбрать · Esc закрыть (%d/%d)", editor.selected+1, len(editor.options)), columns-1))
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
