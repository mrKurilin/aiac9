package week6

import (
	"aiac9-terminal"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	var cloud ChatGenerator
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		cloud = NewDeepSeek(os.Getenv("DEEPSEEK_BASE_URL"), key)
	}
	return RunWithCloud(context.Background(), day, client, os.Getenv("OLLAMA_MODEL"), cloud, os.Getenv("DEEPSEEK_MODEL"), os.Stdin, os.Stdout)
}

// RunWith keeps the application boundary testable without a running model.
func RunWith(ctx context.Context, day int, client LocalClient, model string, in io.Reader, out io.Writer) error {
	return RunWithCloud(ctx, day, client, model, nil, "", in, out)
}

func RunWithCloud(ctx context.Context, day int, client LocalClient, model string, cloud ChatGenerator, cloudModel string, in io.Reader, out io.Writer) error {
	if cloudModel == "" {
		cloudModel = "deepseek-flash"
	}
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
			terminal.Command{Value: "/compare", Description: "сравнить локальный и облачный RAG по вопросу"},
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
		progress := func(s string) { terminal.PrintDiagnostic(out, s) }
		switch line {
		case "/exit", "/quit":
			return true
		case "/help":
			message := "Команды: /help, /model status, /check, /reset, /clear, /exit, /quit."
			if rag != nil {
				message += " /index build [КАТАЛОГ], /index status, /compare ВОПРОС. Обычный текст — локальный вопрос по документам."
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
					terminal.PrintDiagnostic(out, "Вопрос: "+result.Question)
					terminal.PrintDiagnostic(out, FormatHits(result.Hits))
					for _, sample := range result.Samples {
						memory := "н/д"
						if sample.Memory > 0 {
							memory = fmt.Sprintf("%.1f ГБ", float64(sample.Memory)/1e9)
						}
						terminal.PrintDiagnostic(out, fmt.Sprintf("%s: %s; вход %d / выход %d токенов; память %s (по Ollama)", sample.Profile.Name, sample.Result.Duration, sample.Result.PromptTokens, sample.Result.OutputTokens, memory))
					}
					terminal.PrintDiagnostic(out, "Качество: сравните полноту и точность ответов по указанным источникам.")
				}
				break
			}
			if rag != nil && (line == "/compare" || strings.HasPrefix(line, "/compare ")) {
				question := strings.TrimSpace(strings.TrimPrefix(line, "/compare"))
				switch {
				case question == "":
					fmt.Fprintln(out, "Укажите вопрос: /compare ВОПРОС")
				case cloud == nil:
					fmt.Fprintln(out, "Для /compare задайте DEEPSEEK_API_KEY")
				default:
					local, remote, hits, err := rag.Compare(ctx, agent, cloud, cloudModel, question, progress)
					if local.Text != "" && remote.Text == "" {
						terminal.PrintMessage(out, "ЛОКАЛЬНАЯ · "+agent.Model, local.Text)
						terminal.PrintDiagnostic(out, FormatHits(hits))
					}
					if err != nil {
						fmt.Fprintln(out, "Ошибка сравнения:", err)
					} else {
						metrics := fmt.Sprintf("Локальная %s: %s, токены %d/%d\nОблачная %s: %s, токены %d/%d\n%s", agent.Model, local.Duration.Round(time.Millisecond), local.PromptTokens, local.OutputTokens, cloudModel, remote.Duration.Round(time.Millisecond), remote.PromptTokens, remote.OutputTokens, FormatHits(hits))
						terminal.PrintComparison(out, "ЛОКАЛЬНАЯ", "ОБЛАЧНАЯ", local.Text, remote.Text, question, metrics)
					}
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
