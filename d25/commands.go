package main

import (
	"aiac9-terminal"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

var commands = []terminal.Command{
	{Value: "/help", Description: "показать команды", Submit: true},
	{Value: "/reset", Description: "новый диалог", Submit: true},
	{Value: "/clear", Description: "новый диалог", Submit: true},
	{Value: "/exit", Description: "выйти", Submit: true},
	{Value: "/quit", Description: "выйти", Submit: true},
	{Value: "/index", Description: "локальный индекс"},
	{Value: "/index build", Description: "построить индекс документов"},
	{Value: "/index status", Description: "состояние индекса", Submit: true},
	{Value: "/task", Description: "память задачи"},
	{Value: "/task status", Description: "показать память", Submit: true},
	{Value: "/task goal", Description: "задать цель"},
	{Value: "/task clarify", Description: "добавить уточнение"},
	{Value: "/task constraint", Description: "добавить ограничение"},
	{Value: "/task term", Description: "задать термин: НАЗВАНИЕ = ЗНАЧЕНИЕ"},
}

func runCommand(ctx context.Context, a *Agent, line string, out io.Writer) (bool, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return false, false
	}
	fields := strings.Fields(line)
	cmd := fields[0]
	arg := strings.TrimSpace(strings.TrimPrefix(line, cmd))
	switch cmd {
	case "/exit", "/quit":
		return true, true
	case "/help":
		fmt.Fprintln(out, "Команды: /help, /reset, /clear, /exit, /quit; /index build [КАТАЛОГ], /index status; /task status, /task goal ТЕКСТ, /task clarify ТЕКСТ, /task constraint ТЕКСТ, /task term НАЗВАНИЕ = ЗНАЧЕНИЕ")
	case "/reset", "/clear":
		if arg != "" {
			fmt.Fprintln(out, "Использование: /reset или /clear")
			return true, false
		}
		a.Reset()
		if screen, ok := out.(interface{ ClearChat() error }); ok {
			if err := screen.ClearChat(); err != nil {
				fmt.Fprintln(out, "Ошибка очистки экрана:", err)
			}
		}
		fmt.Fprintln(out, "Новый диалог. История и память очищены; индекс сохранён.")
	case "/index":
		action, path, _ := strings.Cut(arg, " ")
		switch action {
		case "build":
			path = strings.TrimSpace(path)
			if path == "" {
				path = "knowledge"
			}
			if err := a.Build(ctx, path, func(s string) { fmt.Fprintln(out, s) }); err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
			}
		case "status":
			idx, err := a.load()
			if err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
			} else {
				fmt.Fprintf(out, "Индекс: %d фрагментов\n", len(idx.Chunks))
			}
		default:
			fmt.Fprintln(out, "Использование: /index build [КАТАЛОГ] | /index status")
		}
	case "/task":
		action, value, _ := strings.Cut(arg, " ")
		value = strings.TrimSpace(value)
		switch action {
		case "status":
			data, _ := json.MarshalIndent(a.task, "", "  ")
			fmt.Fprintln(out, string(data))
		case "goal", "clarify", "constraint":
			if value == "" {
				fmt.Fprintln(out, "Укажите текст после команды.")
				break
			}
			value = limitText(value, 300)
			switch action {
			case "goal":
				a.task.Goal = value
			case "clarify":
				a.task.Clarifications = boundList(append(a.task.Clarifications, value))
			case "constraint":
				a.task.Constraints = boundList(append(a.task.Constraints, value))
			}
			fmt.Fprintln(out, "Память обновлена.")
		case "term":
			name, meaning, ok := strings.Cut(value, "=")
			name, meaning = strings.TrimSpace(name), strings.TrimSpace(meaning)
			if !ok || name == "" || meaning == "" {
				fmt.Fprintln(out, "Использование: /task term НАЗВАНИЕ = ЗНАЧЕНИЕ")
				break
			}
			if a.task.Terms == nil {
				a.task.Terms = make(map[string]string)
			}
			a.task.Terms[limitText(name, 80)] = limitText(meaning, 250)
			a.task = boundedState(a.task)
			fmt.Fprintln(out, "Термин сохранён.")
		default:
			fmt.Fprintln(out, "Использование: /task status|goal|clarify|constraint|term")
		}
	default:
		fmt.Fprintln(out, "Неизвестная команда. /help")
	}
	return true, false
}
