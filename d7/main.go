package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"
)

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func main() {
	cli := flag.Bool("cli", false, "Запустить чат в терминале")
	prompt := flag.String("prompt", "", "Одиночный запрос в терминале")
	session := flag.String("session", "terminal", "Имя сохраняемого терминального диалога")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Используйте -cli или -prompt для терминального режима.")
		os.Exit(1)
	}
	apiKey := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Ошибка: переменная DEEPSEEK_API_KEY не задана.")
		os.Exit(1)
	}

	client := &DeepSeekClient{
		APIKey:  apiKey,
		BaseURL: envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com/chat/completions"),
		Model:   envOr("DEEPSEEK_MODEL", "deepseek-chat"),
		HTTP:    &http.Client{Timeout: 90 * time.Second},
	}

	store, err := NewJSONFileStore(envOr("DATA_DIR", "./data"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Не удалось открыть хранилище истории: %v\n", err)
		os.Exit(1)
	}
	if *cli || *prompt != "" {
		if !validID(*session) {
			fmt.Fprintln(os.Stderr, "Некорректное имя сессии: используйте буквы, цифры, дефис и подчёркивание.")
			os.Exit(1)
		}
		client.Model = envOr("DEEPSEEK_MODEL", "deepseek-v4-flash")
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		agent := NewAgent(client, envOr("AGENT_SYSTEM_PROMPT", "Ты полезный ассистент. Отвечай на языке пользователя."), store, *session)
		if err := runTerminal(ctx, agent, os.Stdin, os.Stdout, *prompt); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			os.Exit(1)
		}
		return
	}

	app := newWebApp(client, envOr("AGENT_SYSTEM_PROMPT", "Ты полезный ассистент. Отвечай на языке пользователя."), os.Getenv("CHAT_PASSWORD"), store)
	addr := envOr("ADDR", ":8080")
	fmt.Printf("Веб-чат запущен на http://localhost%s, история сохраняется в %s\n", addr, store.dir)
	if app.password == "" {
		fmt.Fprintln(os.Stderr, "Внимание: CHAT_PASSWORD не задан. Не публикуйте сервер в интернете без пароля!")
	}

	server := &http.Server{Addr: addr, Handler: app.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "Ошибка сервера: %v\n", err)
		os.Exit(1)
	}
}
