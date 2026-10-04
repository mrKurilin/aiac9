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
	profileName := ""
	if day >= 29 {
		agent.UseProfile(Economical)
		profileName = Economical.Name
		commands = append(commands,
			terminal.Command{Value: "/profile", Description: "профиль генерации"},
			terminal.Command{Value: "/profile status", Description: "текущий профиль", Submit: true},
			terminal.Command{Value: "/profile baseline", Description: "базовые параметры", Submit: true},
			terminal.Command{Value: "/profile optimized", Description: "экономный профиль", Submit: true},
			terminal.Command{Value: "/benchmark", Description: "сравнить профили"},
		)
	}
	handle := func(ctx context.Context, line string, out io.Writer) bool {
		line = strings.TrimSpace(line)
		progress := func(s string) { fmt.Fprintln(out, s) }
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
			if day >= 29 {
				message += " /profile status|baseline|optimized, /benchmark ВОПРОС."
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
		case "/profile status":
			if day < 29 {
				fmt.Fprintln(out, "Неизвестная команда. /help")
				break
			}
			fmt.Fprintf(out, "Профиль: %s; модель: %s; temperature %.1f; max tokens %d; context %d\n", profileName, agent.Model, agent.Options.Temperature, agent.Options.NumPredict, agent.Options.NumCtx)
		case "/profile baseline", "/profile optimized":
			if day < 29 {
				fmt.Fprintln(out, "Неизвестная команда. /help")
				break
			}
			if line == "/profile baseline" {
				agent.UseProfile(Baseline)
				profileName = Baseline.Name
			} else {
				agent.UseProfile(Economical)
				profileName = Economical.Name
			}
			fmt.Fprintln(out, "Выбран профиль:", profileName)
		default:
			if day >= 29 && (line == "/benchmark" || strings.HasPrefix(line, "/benchmark ")) {
				question := strings.TrimSpace(strings.TrimPrefix(line, "/benchmark"))
				if question == "" {
					fmt.Fprintln(out, "Использование: /benchmark ВОПРОС")
					break
				}
				result, err := rag.Benchmark(ctx, agent, question, progress)
				if err != nil {
					fmt.Fprintln(out, "Ошибка:", err)
					break
				}
				if len(result.Samples) == 2 {
					left, right := result.Samples[0], result.Samples[1]
					terminal.PrintComparison(out, "БАЗОВЫЙ", "ЭКОНОМНЫЙ", left.Result.Text, right.Result.Text)
					fmt.Fprintln(out, "Вопрос:", result.Question)
					fmt.Fprintln(out, FormatHits(result.Hits))
					for _, sample := range result.Samples {
						memory := "н/д"
						if sample.Memory > 0 { memory = fmt.Sprintf("%.1f ГБ", float64(sample.Memory)/1e9) }
						fmt.Fprintf(out, "%s: %s; вход %d / выход %d токенов; память %s (по Ollama)\n", sample.Profile.Name, sample.Result.Duration, sample.Result.PromptTokens, sample.Result.OutputTokens, memory)
					}
					fmt.Fprintln(out, "Качество: сравните полноту и точность ответов по указанным источникам.")
				}
				break
			}
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
					fmt.Fprintln(out, result.Text+"\n\n"+FormatHits(hits))
				}
			} else {
				result, err := agent.Answer(ctx, line, progress)
				if err != nil {
					fmt.Fprintln(out, "Ошибка:", err)
				} else {
					fmt.Fprintln(out, result.Text)
				}
			}
		}
		return false
	}
	return terminal.Run(ctx, in, out, fmt.Sprintf("mrkai · день %d · локальная LLM · /help", day), commands, handle)
}
