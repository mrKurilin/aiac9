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
	{Value: "/task", Description: "цель для работы с файлами: /task ЦЕЛЬ"},
	{Value: "/help", Description: "вопрос о проекте или подсказка по командам"},
	{Value: "/index", Description: "индекс документации"},
	{Value: "/index build", Description: "индексировать README и docs", Submit: true},
	{Value: "/index status", Description: "показать число фрагментов", Submit: true},
	{Value: "/mcp", Description: "инструменты проекта"},
	{Value: "/mcp tools", Description: "список инструментов", Submit: true},
	{Value: "/mcp git", Description: "инструменты git"},
	{Value: "/mcp git branch", Description: "текущая git ветка", Submit: true},
	{Value: "/reset", Description: "новый диалог", Submit: true},
	{Value: "/clear", Description: "новый диалог", Submit: true},
	{Value: "/exit", Description: "выйти", Submit: true},
	{Value: "/quit", Description: "выйти", Submit: true},
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--project-mcp-server" {
		if err := serveMCP(os.Args[2], os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "Использование: mrkai")
		os.Exit(2)
	}
	root := os.Getenv("MRKAI_PROJECT_ROOT")
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
			os.Exit(1)
		}
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
	if err := run(context.Background(), root, client, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
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
		case "/exit", "/quit":
			return true
		case "/help":
			fmt.Fprintln(out, "Команды: /task ЦЕЛЬ, /help ВОПРОС, /index build, /index status, /mcp tools, /mcp git branch, /reset, /clear, /exit, /quit. Обычный текст — многоходовой чат.")
		case "/task":
			fmt.Fprintln(out, "Укажите цель: /task ЦЕЛЬ")
		case "/reset", "/clear":
			a.reset()
			if screen, ok := out.(interface{ ClearChat() error }); ok {
				_ = screen.ClearChat()
			}
			fmt.Fprintln(out, "Новый диалог. История очищена.")
		case "/index build":
			progress("RAG: читаю README и docs")
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
		case "/mcp tools", "/mcp git branch":
			progress("MCP: подключаю сервер проекта")
			mcp, err := startMCP(ctx, root)
			if err != nil {
				fmt.Fprintln(out, "Ошибка MCP:", err)
				break
			}
			defer mcp.close()
			if line == "/mcp tools" {
				list, err := mcp.listTools(ctx)
				if err != nil {
					fmt.Fprintln(out, "Ошибка MCP:", err)
				} else {
					progress(fmt.Sprintf("MCP: обнаружено инструментов %d", len(list)))
					for _, tool := range list {
						fmt.Fprintf(out, "%s — %s\n", tool.Name, tool.Description)
					}
				}
			} else {
				progress("MCP: вызываю git_branch")
				branch, err := mcp.callTool(ctx, "git_branch", json.RawMessage(`{}`))
				if err != nil {
					fmt.Fprintln(out, "Ошибка MCP:", err)
				} else {
					fmt.Fprintln(out, "Текущая ветка:", branch)
					progress("MCP: готово")
				}
			}
		default:
			task := strings.HasPrefix(line, "/task ")
			project := strings.HasPrefix(line, "/help ")
			if strings.HasPrefix(line, "/") && !project && !task {
				fmt.Fprintln(out, "Неизвестная команда. /help")
				break
			}
			question := line
			if project {
				question = strings.TrimSpace(strings.TrimPrefix(line, "/help "))
			}
			if task {
				question = strings.TrimSpace(strings.TrimPrefix(line, "/task "))
			}
			if question == "" {
				break
			}
			progress("MCP: подключаю сервер проекта")
			mcp, err := startMCP(ctx, root)
			if err != nil {
				fmt.Fprintln(out, "Ошибка MCP:", err)
				break
			}
			a.mcp = mcp
			a.taskMode = task
			answer, hits, err := a.answer(ctx, question, project, progress)
			a.taskMode = false
			a.mcp = nil
			mcp.close()
			if err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
				break
			}
			terminal.PrintMessage(out, "АГЕНТ", answer)
			if project && len(hits) > 0 {
				var sources []string
				seen := map[string]bool{}
				for _, hit := range hits {
					if !seen[hit.Source] {
						sources = append(sources, hit.Source)
						seen[hit.Source] = true
					}
				}
				progress("Источники: " + strings.Join(sources, ", "))
			}
		}
		return false
	}
	return terminal.Run(ctx, in, out, "mrkai · работа с файлами", commands, handler)
}
