package week6

import (
	"aiac9-terminal"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	return RunWith(context.Background(), day, client, os.Getenv("OLLAMA_MODEL"), os.Stdin, os.Stdout)
}

// RunWith keeps the application boundary testable without a running model.
func RunWith(ctx context.Context, day int, client LocalClient, model string, in io.Reader, out io.Writer) error {
	agent := NewAgent(client, model)
	commands := append([]terminal.Command(nil), baseCommands...)
	var rag *RAG
	if day >= 28 {
		embedder, ok := client.(Embedder)
		if !ok {
			return fmt.Errorf("клиент не поддерживает локальные эмбеддинги")
		}
		dataDir := os.Getenv("DATA_DIR")
		if dataDir == "" {
			dataDir = "data"
		}
		rag = NewRAG(embedder, os.Getenv("OLLAMA_EMBED_MODEL"), filepath.Join(dataDir, "index.json"))
		commands = append(commands,
			terminal.Command{Value: "/index", Description: "индекс документов"},
			terminal.Command{Value: "/index build", Description: "построить локальный индекс"},
			terminal.Command{Value: "/index status", Description: "число фрагментов", Submit: true},
		)
	}
	handle := func(ctx context.Context, line string, out io.Writer) bool {
		line = strings.TrimSpace(line)
		progress := func(s string) { terminal.PrintDiagnostic(out, s) }
		switch line {
		case "/exit", "/quit":
			return true
		case "/help":
			message := "Команды: /help, /model status, /check, /reset, /clear, /exit, /quit."
			if rag != nil {
				message += " /index build [КАТАЛОГ], /index status. Обычный текст — вопрос по документам."
			} else {
				message += " Обычный текст — диалог с локальной моделью."
			}
			fmt.Fprintln(out, message)
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
		case "/index status":
			if rag == nil {
				fmt.Fprintln(out, "Неизвестная команда. /help")
				break
			}
			index, err := rag.Load()
			if err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
			} else {
				fmt.Fprintf(out, "Индекс: %d фрагментов, модель %s\n", len(index.Chunks), index.Model)
			}
		default:
			if rag != nil && (line == "/index build" || strings.HasPrefix(line, "/index build ")) {
				path := strings.TrimSpace(strings.TrimPrefix(line, "/index build"))
				if path == "" {
					path = "knowledge"
				}
				progress("RAG: читаю документы из " + path)
				count, err := rag.Build(ctx, path, progress)
				if err != nil {
					fmt.Fprintln(out, "Ошибка:", err)
				} else {
					fmt.Fprintf(out, "Индекс готов: %d фрагментов\n", count)
				}
				break
			}
			if strings.HasPrefix(line, "/") {
				fmt.Fprintln(out, "Неизвестная команда. /help")
				break
			}
			if line == "" {
				break
			}
			if rag != nil {
				result, hits, err := rag.Answer(ctx, agent, line, progress)
				if err != nil {
					fmt.Fprintln(out, "Ошибка:", err)
				} else {
					terminal.PrintMessage(out, "МОДЕЛЬ · "+agent.Model, result.Text)
					terminal.PrintDiagnostic(out, FormatHits(hits))
				}
			} else {
				result, err := agent.Answer(ctx, line, progress)
				if err != nil {
					fmt.Fprintln(out, "Ошибка:", err)
				} else {
					terminal.PrintMessage(out, "МОДЕЛЬ · "+agent.Model, result.Text)
				}
			}
		}
		return false
	}
	return terminal.Run(ctx, in, out, fmt.Sprintf("mrkai · день %d · локальная LLM · /help", day), commands, handle)
}
