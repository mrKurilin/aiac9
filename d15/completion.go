package main

import "strings"

type completion struct {
	Label  string
	Value  string
	Submit bool
}

var slashCommands = []completion{
	{Label: "/state   показать состояние задачи", Value: "/state", Submit: true},
	{Label: "/invariants   показать инварианты", Value: "/invariants", Submit: true},
	{Label: "/invariant add   добавить инвариант", Value: "/invariant add ", Submit: false},
	{Label: "/invariant remove   удалить инвариант", Value: "/invariant remove ", Submit: false},
	{Label: "/invariant clear   удалить все инварианты", Value: "/invariant clear", Submit: true},
	{Label: "/pause   поставить задачу на паузу", Value: "/pause ", Submit: false},
	{Label: "/resume  продолжить сохранённую задачу", Value: "/resume", Submit: true},
	{Label: "/reset   удалить задачу и контекст", Value: "/reset", Submit: true},
	{Label: "/info    показать команды", Value: "/info", Submit: true},
	{Label: "/exit    выйти", Value: "/exit", Submit: true},
}

func commandCompletions(text string) []completion {
	if !strings.HasPrefix(text, "/") || strings.ContainsAny(text, " \t\n") {
		return nil
	}
	text = strings.ToLower(text)
	result := []completion{}
	for _, command := range slashCommands {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(command.Value)), text) {
			result = append(result, command)
		}
	}
	return result
}
