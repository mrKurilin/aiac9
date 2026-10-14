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
	{Value: "/support", Description: "ответ по тикету: /support ID ВОПРОС"},
	{Value: "/ticket", Description: "данные тикета: /ticket ID"},
	{Value: "/help", Description: "подсказка по командам", Submit: true},
	{Value: "/index", Description: "индекс документации"},
	{Value: "/index build", Description: "индексировать README и docs", Submit: true},
	{Value: "/index status", Description: "показать число фрагментов", Submit: true},
	{Value: "/mcp", Description: "инструменты поддержки"},
	{Value: "/mcp tools", Description: "список инструментов", Submit: true},
	{Value: "/reset", Description: "новый диалог", Submit: true},
	{Value: "/clear", Description: "новый диалог", Submit: true},
	{Value: "/exit", Description: "выйти", Submit: true},
	{Value: "/quit", Description: "выйти", Submit: true},
}

func main() {
	if len(os.Args) == 3 && (os.Args[1] == "--healthcheck" || os.Args[1] == "--deployment-check") {
		var err error
		if os.Args[1] == "--healthcheck" {
			err = healthcheck(os.Args[2])
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			err = checkDeployment(ctx, os.Args[2], &http.Client{Timeout: 10 * time.Second})
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && (os.Args[1] == "web" || os.Args[1] == "support") {
		if err := serveApplication(os.Args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "--support-mcp-server" {
		if err := serveMCP(os.Args[2], os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "Использование: mrkai [web|support]")
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
			fmt.Fprintln(out, "Команды: /support ID ВОПРОС, /ticket ID, /help, /index build, /index status, /mcp tools, /reset, /clear, /exit, /quit. Обычный текст — многоходовой чат.")
		case "/index":
			fmt.Fprintln(out, "Команды: /index build, /index status")
		case "/mcp":
			fmt.Fprintln(out, "Команда: /mcp tools")
		case "/support", "/ticket":
			fmt.Fprintln(out, "Укажите ID тикета; для /support добавьте вопрос.")
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
		case "/mcp tools":
			progress("MCP: подключаю сервер поддержки")
			mcp, err := startMCP(ctx, root)
			if err != nil {
				fmt.Fprintln(out, "Ошибка MCP:", err)
				break
			}
			defer mcp.close()
			list, err := mcp.listTools(ctx)
			if err != nil {
				fmt.Fprintln(out, "Ошибка MCP:", err)
			} else {
				progress(fmt.Sprintf("MCP: обнаружено инструментов %d", len(list)))
				for _, tool := range list {
					fmt.Fprintf(out, "%s — %s\n", tool.Name, tool.Description)
				}
			}
		default:
			if strings.HasPrefix(line, "/ticket ") || strings.HasPrefix(line, "/support ") {
				parts := strings.Fields(line)
				if (parts[0] == "/ticket" && len(parts) != 2) || (parts[0] == "/support" && len(parts) < 3) {
					fmt.Fprintln(out, "Укажите ID тикета и вопрос.")
					break
				}
				progress("MCP: получаю тикет")
				mcp, err := startMCP(ctx, root)
				if err != nil {
					fmt.Fprintln(out, "Ошибка MCP:", err)
					break
				}
				args, _ := json.Marshal(map[string]string{"id": parts[1]})
				ticket, err := mcp.callTool(ctx, "support_ticket", args)
				if err != nil {
					mcp.close()
					fmt.Fprintln(out, "Ошибка MCP:", err)
					break
				}
				if parts[0] == "/ticket" {
					mcp.close()
					fmt.Fprintln(out, ticket)
					break
				}
				if a.supportTicketID != parts[1] {
					a.reset()
				}
				a.supportTicketID = parts[1]
				a.mcp = mcp
				a.supportContext = ticket
				question := strings.Join(parts[2:], " ")
				answer, hits, err := a.answer(ctx, question, true, progress)
				a.mcp = nil
				mcp.close()
				if err != nil {
					fmt.Fprintln(out, "Ошибка:", err)
					break
				}
				terminal.PrintMessage(out, "ПОДДЕРЖКА", answer)
				for _, hit := range hits {
					progress("Источник: " + hit.Source)
				}
				break
			}
			if strings.HasPrefix(line, "/") {
				fmt.Fprintln(out, "Неизвестная команда. /help")
				break
			}
			question := line
			if question == "" {
				break
			}
			progress("MCP: подключаю сервер поддержки")
			mcp, err := startMCP(ctx, root)
			if err != nil {
				fmt.Fprintln(out, "Ошибка MCP:", err)
				break
			}
			if a.supportTicketID != "" {
				progress("MCP: обновляю контекст выбранного тикета")
				args, _ := json.Marshal(map[string]string{"id": a.supportTicketID})
				ticket, err := mcp.callTool(ctx, "support_ticket", args)
				if err != nil {
					mcp.close()
					fmt.Fprintln(out, "Ошибка MCP:", err)
					break
				}
				a.supportContext = ticket
			}
			a.mcp = mcp
			answer, hits, err := a.answer(ctx, question, true, progress)
			a.mcp = nil
			mcp.close()
			if err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
				break
			}
			terminal.PrintMessage(out, "АГЕНТ", answer)
			if len(hits) > 0 {
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
	return terminal.Run(ctx, in, out, "mrkai · поддержка пользователей", commands, handler)
}
