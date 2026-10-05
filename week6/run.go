package week6

import (
	"aiac9-terminal"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

var baseCommands = []terminal.Command{
	{Value: "/help", Description: "показать команды", Submit: true},
	{Value: "/reset", Description: "новый диалог", Submit: true},
	{Value: "/clear", Description: "новый диалог", Submit: true},
	{Value: "/exit", Description: "выйти", Submit: true},
	{Value: "/quit", Description: "выйти", Submit: true},
	{Value: "/model", Description: "локальная модель"},
	{Value: "/model status", Description: "список загруженных моделей", Submit: true},
	{Value: "/check", Description: "три запроса разной сложности", Submit: true},
}

func Run(day int) error {
	client := NewOllama(os.Getenv("OLLAMA_BASE_URL"))
	agent := NewAgent(client, os.Getenv("OLLAMA_MODEL"))
	handle := func(ctx context.Context, line string, out io.Writer) bool {
		line = strings.TrimSpace(line)
		progress := func(s string) { terminal.PrintDiagnostic(out, s) }
		switch line {
		case "/exit", "/quit":
			return true
		case "/help":
			fmt.Fprintln(out, "Команды: /help, /model status, /check, /reset, /clear, /exit, /quit. Обычный текст — диалог с локальной моделью.")
		case "/reset", "/clear":
			agent.Reset()
			if screen, ok := out.(interface{ ClearChat() error }); ok {
				_ = screen.ClearChat()
			}
			fmt.Fprintln(out, "Новый диалог. История очищена.")
		case "/model status":
			progress("Ollama: проверяю загруженные модели")
			value, err := agent.Models(ctx)
			if err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
			} else {
				fmt.Fprintln(out, value)
			}
		case "/check":
			value, err := agent.Check(ctx, progress)
			if value != "" {
				fmt.Fprintln(out, value)
			}
			if err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
			}
		default:
			if strings.HasPrefix(line, "/") {
				fmt.Fprintln(out, "Неизвестная команда. /help")
				break
			}
			if line == "" {
				break
			}
			result, err := agent.Answer(ctx, line, progress)
			if err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
			} else {
				terminal.PrintMessage(out, "МОДЕЛЬ · "+agent.Model, result.Text)
			}
		}
		return false
	}
	return terminal.Run(context.Background(), os.Stdin, os.Stdout, fmt.Sprintf("mrkai · день %d · локальная LLM · /help", day), baseCommands, handle)
}
