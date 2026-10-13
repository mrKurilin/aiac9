package main

import (
	"aiac9-terminal"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var commands = []terminal.Command{
	{Value: "/review", Description: "ревью изменений: /review BASE HEAD"},
	{Value: "/help", Description: "подсказка по командам", Submit: true},
	{Value: "/index", Description: "индекс документации и кода"},
	{Value: "/index build", Description: "индексировать Markdown и Go", Submit: true},
	{Value: "/index status", Description: "показать число фрагментов", Submit: true},
	{Value: "/reset", Description: "новый диалог", Submit: true},
	{Value: "/clear", Description: "новый диалог", Submit: true},
	{Value: "/exit", Description: "выйти", Submit: true},
	{Value: "/quit", Description: "выйти", Submit: true},
}

func main() {
	if len(os.Args) != 1 && !(len(os.Args) == 4 && (os.Args[1] == "--review" || os.Args[1] == "--review-json")) {
		fmt.Fprintln(os.Stderr, "Использование: mrkai [--review|--review-json BASE HEAD]")
		os.Exit(2)
	}
	root, err := resolveProjectRoot(os.Getenv("MRKAI_PROJECT_ROOT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
	model := os.Getenv("DEEPSEEK_MODEL")
	if model == "" {
		model = "deepseek-chat"
	}
	url := os.Getenv("DEEPSEEK_BASE_URL")
	if url == "" {
		url = "https://api.deepseek.com/chat/completions"
	}
	client := deepSeekClient{key: os.Getenv("DEEPSEEK_API_KEY"), url: url, model: model, http: &http.Client{Timeout: 3 * time.Minute}}
	if len(os.Args) == 4 {
		answer, err := (&agent{client: client}).review(context.Background(), root, os.Args[2], os.Args[3], func(s string) { fmt.Fprintln(os.Stderr, s) })
		if err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
			os.Exit(1)
		}
		if os.Args[1] == "--review-json" {
			if err := json.NewEncoder(os.Stdout).Encode(answer); err != nil {
				fmt.Fprintln(os.Stderr, "Ошибка вывода ревью")
				os.Exit(1)
			}
		} else {
			fmt.Printf("Решение: %s\n\n%s\n", answer.Decision, answer.Body)
		}
		return
	}
	if err := run(context.Background(), root, client, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}

func resolveProjectRoot(root string) (string, error) {
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("MRKAI_PROJECT_ROOT: не удалось определить путь проекта")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("MRKAI_PROJECT_ROOT: каталог не существует или недоступен")
	}
	if !info.IsDir() {
		return "", fmt.Errorf("MRKAI_PROJECT_ROOT должен указывать на каталог")
	}
	return abs, nil
}

func run(ctx context.Context, root string, client chatClient, in io.Reader, out io.Writer) error {
	base, err := os.Getwd()
	if err != nil {
		return err
	}
	a := &agent{client: client, search: searchIndex{root: root, path: filepath.Join(base, "data", "index.json")}}
	handler := func(ctx context.Context, line string, out io.Writer) bool {
		line = strings.TrimSpace(line)
		progress := func(s string) { terminal.PrintDiagnostic(out, s) }
		switch line {
		case "/review":
			fmt.Fprintln(out, "Укажите /review BASE HEAD")
		case "/exit", "/quit":
			return true
		case "/help":
			fmt.Fprintln(out, "Команды: /review BASE HEAD, /help, /index build, /index status, /reset, /clear, /exit, /quit. Обычный текст — многоходовой чат.")
		case "/reset", "/clear":
			a.reset()
			if screen, ok := out.(interface{ ClearChat() error }); ok {
				_ = screen.ClearChat()
			}
			fmt.Fprintln(out, "Новый диалог. История очищена.")
		case "/index build":
			progress("RAG: читаю документацию и код")
			count, err := a.search.build(ctx, progress)
			if err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
			} else {
				fmt.Fprintf(out, "Индекс готов: %d фрагментов\n", count)
			}
		case "/index status":
			idx, err := a.search.load()
			if err != nil {
				fmt.Fprintln(out, "Индекс не готов. Выполните /index build.")
			} else {
				fmt.Fprintf(out, "Индекс: %d фрагментов\n", len(idx.Chunks))
			}
		default:
			if strings.HasPrefix(line, "/review ") {
				parts := strings.Fields(line)
				if len(parts) != 3 {
					fmt.Fprintln(out, "Укажите /review BASE HEAD")
					break
				}
				answer, err := a.review(ctx, root, parts[1], parts[2], progress)
				if err != nil {
					fmt.Fprintln(out, "Ошибка:", err)
				} else {
					terminal.PrintMessage(out, "РЕВЬЮ · "+answer.Decision, answer.Body)
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
			answer, err := a.answer(ctx, line, progress)
			if err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
				break
			}
			terminal.PrintMessage(out, "АГЕНТ", answer)
		}
		return false
	}
	return terminal.Run(ctx, in, out, "mrkai · ревью кода", commands, handler)
}
