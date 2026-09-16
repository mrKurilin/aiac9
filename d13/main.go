package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func validateLaunchArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("запускайте mrkai без параметров")
}

func main() {
	if err := validateLaunchArgs(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(2)
	}
	apiKey := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Ошибка: задайте DEEPSEEK_API_KEY в окружении.")
		os.Exit(1)
	}
	store := NewJSONStateStore(filepath.Join(envOr("DATA_DIR", "./data"), "task-state.json"))
	client := NewDeepSeekClient(
		apiKey,
		envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com/chat/completions"),
		envOr("DEEPSEEK_MODEL", "deepseek-chat"),
		&http.Client{Timeout: 90 * time.Second},
	)
	agent := NewAgent(client, envOr("AGENT_SYSTEM_PROMPT", "Ты практичный ИИ-ассистент. Отвечай на языке пользователя."), store)
	if agent.loadErr != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", agent.loadErr)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := runTerminal(ctx, agent, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
