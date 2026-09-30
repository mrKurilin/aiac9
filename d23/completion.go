package main

import "strings"

type completion struct {
	Label  string
	Value  string
	Submit bool
}

// Commands form a tree by their space-separated path. Autocomplete exposes
// only one level at a time.
var slashCommands = func() []completion {
	result := make([]completion, 0, len(commands))
	for _, value := range commands {
		submit := false
		switch value {
		case "/help", "/reset", "/clear", "/exit", "/quit", "/index status", "/rag on", "/rag off", "/rag status", "/rag config", "/rag rewrite on", "/rag rewrite off":
			submit = true
		}
		label := commandLabels[value]
		if value == "/reset" || value == "/clear" {
			label = "очистить диалог и экран"
		}
		result = append(result, completion{Label: value + "    " + label, Value: value, Submit: submit})
	}
	return result
}()

func commandDepth(value string) int {
	return len(strings.Fields(value))
}

func directCommandChildren(parent string) []completion {
	parent = strings.TrimSpace(strings.ToLower(parent))
	depth := commandDepth(parent) + 1
	result := []completion{}
	for _, command := range slashCommands {
		value := strings.ToLower(command.Value)
		if commandDepth(value) != depth {
			continue
		}
		if parent == "" || strings.HasPrefix(value, parent+" ") {
			result = append(result, command)
		}
	}
	return result
}

func exactCommand(text string) (completion, bool) {
	text = strings.TrimSpace(strings.ToLower(text))
	for _, command := range slashCommands {
		if strings.ToLower(command.Value) == text {
			return command, true
		}
	}
	return completion{}, false
}

func commandCompletions(text string) []completion {
	if !strings.HasPrefix(text, "/") || strings.ContainsAny(text, "\t\n") {
		return nil
	}
	lower := strings.ToLower(text)
	if lower == "/" {
		return directCommandChildren("")
	}
	trimmed := strings.TrimSpace(lower)
	if _, ok := exactCommand(trimmed); ok {
		if children := directCommandChildren(trimmed); len(children) > 0 {
			return children
		}
	}

	parent := ""
	prefix := trimmed
	if strings.HasSuffix(lower, " ") {
		parent, prefix = trimmed, trimmed+" "
	} else if index := strings.LastIndex(trimmed, " "); index >= 0 {
		parent = trimmed[:index]
	}
	result := []completion{}
	for _, command := range directCommandChildren(parent) {
		if strings.HasPrefix(strings.ToLower(command.Value), prefix) {
			result = append(result, command)
		}
	}
	return result
}
