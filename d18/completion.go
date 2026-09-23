package main

import "strings"

type completion struct {
	Label  string
	Value  string
	Submit bool
}

// Commands form a tree by their space-separated path. Autocomplete exposes
// only one level at a time.
var slashCommands = []completion{
	{Label: "/info    показать команды", Value: "/info", Submit: true},
	{Label: "/mcp     показать серверы MCP", Value: "/mcp", Submit: true},
	{Label: "/state   показать состояние задачи", Value: "/state", Submit: true},
	{Label: "/plan-mode   режим планирования", Value: "/plan-mode", Submit: false},
	{Label: "/invariants   показать инварианты", Value: "/invariants", Submit: true},
	{Label: "/invariant   изменить инварианты", Value: "/invariant", Submit: false},
	{Label: "/pause   поставить задачу на паузу", Value: "/pause", Submit: true},
	{Label: "/resume  продолжить сохранённую задачу", Value: "/resume", Submit: true},
	{Label: "/reset   удалить задачу и контекст", Value: "/reset", Submit: true},
	{Label: "/help    показать команды", Value: "/help", Submit: true},
	{Label: "/exit    выйти", Value: "/exit", Submit: true},
	{Label: "/quit    выйти", Value: "/quit", Submit: true},

	{Label: "/mcp list     показать серверы MCP", Value: "/mcp list", Submit: true},
	{Label: "/mcp mrkgitlab   mrkGitlab", Value: "/mcp mrkgitlab", Submit: true},
	{Label: "/mcp mrkscheduler   планировщик", Value: "/mcp mrkscheduler", Submit: true},

	{Label: "/mcp mrkscheduler tools", Value: "/mcp mrkscheduler tools", Submit: true},
	{Label: "/mcp mrkscheduler add", Value: "/mcp mrkscheduler add", Submit: false},
	{Label: "/mcp mrkscheduler mrs", Value: "/mcp mrkscheduler mrs", Submit: false},
	{Label: "/mcp mrkscheduler list", Value: "/mcp mrkscheduler list", Submit: true},
	{Label: "/mcp mrkscheduler summary", Value: "/mcp mrkscheduler summary", Submit: true},
	{Label: "/mcp mrkscheduler cancel", Value: "/mcp mrkscheduler cancel", Submit: false},
	{Label: "/mcp mrkgitlab tools   инструменты mrkGitlab", Value: "/mcp mrkgitlab tools", Submit: true},
	{Label: "/mcp mrkgitlab mrs     сводка по моим MR", Value: "/mcp mrkgitlab mrs", Submit: true},
	{Label: "/mcp mrkgitlab call", Value: "/mcp mrkgitlab call", Submit: false},

	{Label: "/invariant add      добавить инвариант", Value: "/invariant add", Submit: false},
	{Label: "/invariant remove   удалить инвариант", Value: "/invariant remove", Submit: false},
	{Label: "/invariant clear    удалить все инварианты", Value: "/invariant clear", Submit: true},
	{Label: "/plan-mode enable   включить планирование", Value: "/plan-mode enable", Submit: true},
	{Label: "/plan-mode disable  обычный чат", Value: "/plan-mode disable", Submit: true},
}

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
		return []completion{slashCommands[0], slashCommands[1]}
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
