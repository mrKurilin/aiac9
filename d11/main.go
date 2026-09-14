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
	if len(args) == 0 || (len(args) == 1 && args[0] == "web") {
		return nil
	}
	return fmt.Errorf("запускайте mrkai без параметров или как mrkai web")
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
	store, err := NewJSONLayerStore(filepath.Join(envOr("DATA_DIR", "./data"), "memory-layers"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
	client := NewDeepSeekClient(
		apiKey,
		envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com/chat/completions"),
		envOr("DEEPSEEK_MODEL", "deepseek-chat"),
		&http.Client{Timeout: 90 * time.Second},
	)
	agent := NewAgent(client, envOr("AGENT_SYSTEM_PROMPT", "Ты полезный ассистент. Отвечай на языке пользователя."), store, "default")
	agent.extractor = LLMMemoryExtractor{Client: client}
	if envOr("WEATHER_ENABLED", "1") != "0" {
		weatherHTTP := &http.Client{
			Timeout:       12 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		agent.weather = WttrWeather{HTTP: weatherHTTP}
		agent.weatherPlace = LLMWeatherLocationResolver{Client: client}
	}
	if envOr("ARTICLES_ENABLED", "1") != "0" {
		articlesHTTP := &http.Client{
			Timeout:       12 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		agent.articles = DEVArticles{HTTP: articlesHTTP}
		agent.articleTopic = LLMArticleTopicResolver{Client: client}
	}
	if agent.loadErr != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", agent.loadErr)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if len(os.Args) == 2 {
		addr := envOr("WEB_ADDR", ":8080")
		fmt.Printf("Веб-чат: http://localhost%s\n", addr)
		if err := runWebServer(ctx, addr, agent); err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
			os.Exit(1)
		}
		return
	}
	if err := runTerminal(ctx, agent, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
