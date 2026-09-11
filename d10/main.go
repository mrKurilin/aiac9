package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	if err := validateLaunchArgs(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	webMode := len(os.Args) == 2 && os.Args[1] == "web"
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	apiKey := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Ошибка: переменная DEEPSEEK_API_KEY не задана.")
		os.Exit(1)
	}
	baseURL := envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com/chat/completions")
	model := envOr("DEEPSEEK_MODEL", "deepseek-v4-flash")
	dataDir := envOr("DATA_DIR", "./data")
	messagesPath = envOr("MESSAGES_FILE", messagesPath)
	fmt.Println("Токены до запроса — приблизительная оценка; после ответа — usage API, если доступен.")

	system := envOr("AGENT_SYSTEM_PROMPT", "Ты полезный ассистент. Отвечай на языке пользователя.")
	left, err := newComparisonAgent(apiKey, baseURL, model, system, filepath.Join(dataDir, "compare-window"), "window")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	right, err := newComparisonAgent(apiKey, baseURL, model, system, filepath.Join(dataDir, "compare-facts"), "facts")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if webMode {
		addr := envOr("WEB_ADDR", ":8080")
		fmt.Printf("Веб-интерфейс: http://localhost%s · слева Sliding Window · справа Sticky Facts / Key-Value Memory · N=%d\n", addr, defaultKeepLast)
		if err := runWebServer(ctx, addr, left, right); err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("Панели: слева Sliding Window · справа Sticky Facts / Key-Value Memory · N=%d\n", defaultKeepLast)
	if err := runComparisonTerminal(ctx, left, right, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}

func newComparisonAgent(apiKey, baseURL, model, system, dir, mode string) (*Agent, error) {
	client := NewDeepSeekClient(apiKey, baseURL, model, &http.Client{Timeout: 90 * time.Second}, 0)
	contextLimit := 0
	if limits, ok := limitsForModel(model); ok {
		contextLimit = limits.Context
	}
	meter := &Meter{Client: client, AllowOverflow: true, ContextLimit: contextLimit}
	pricing := &Pricing{Model: model, Endpoint: baseURL}
	for _, entry := range []struct {
		name   string
		target **float64
	}{
		{"DEEPSEEK_INPUT_PRICE", &pricing.Input},
		{"DEEPSEEK_CACHED_PRICE", &pricing.Cached},
		{"DEEPSEEK_OUTPUT_PRICE", &pricing.Output},
	} {
		value, err := optionalPrice(entry.name)
		if err != nil {
			return nil, err
		}
		*entry.target = value
	}
	if _, err := pricing.At(time.Now()); err != nil {
		return nil, err
	}
	meter.Pricing = pricing
	store, err := NewJSONFileStore(dir)
	if err != nil {
		return nil, err
	}
	agent := NewAgent(meter, system, store, "terminal")
	agent.extractor = LLMFacts{Client: meter}
	if agent.loadErr != nil {
		return nil, agent.loadErr
	}
	if err := agent.Configure(mode, defaultKeepLast); err != nil {
		return nil, err
	}
	return agent, nil
}

func validateLaunchArgs(args []string) error {
	if len(args) == 1 && args[0] == "web" {
		return nil
	}
	if len(args) > 0 {
		return fmt.Errorf("запускайте mrkai без параметров или с единственным параметром web; команды и настройки доступны внутри чата через /")
	}
	return nil
}
func optionalPrice(name string) (*float64, error) {
	raw, exists := os.LookupEnv(name)
	if !exists {
		return nil, nil
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, fmt.Errorf("%s: тариф должен быть конечным неотрицательным числом", name)
	}
	return &value, nil
}
