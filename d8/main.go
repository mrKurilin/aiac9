package main

import (
	"context"
	"flag"
	"fmt"
	"math"
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
	flag.Bool("cli", true, "Терминальный режим (включён всегда)")
	prompt := flag.String("prompt", "", "Одиночный запрос")
	session := flag.String("session", "terminal", "Имя сохраняемого диалога")
	demo := flag.Bool("demo", false, "Локальное сравнение короткого, длинного и переполненного диалогов")
	allowOverflow := flag.Bool("allow-overflow", true, "Отправлять запросы сверх локальной оценки окна; фактический лимит проверяет API")
	maxTokens := flag.Int("max-tokens", 0, "Ручной максимум токенов ответа; 0 — не передавать лимит модели")
	inputPrice := flag.Float64("input-price", 0, "Переопределить USD за миллион входных токенов без кеша (по умолчанию тариф модели)")
	cachedPrice := flag.Float64("cached-price", 0, "Переопределить USD за миллион входных токенов из кеша")
	outputPrice := flag.Float64("output-price", 0, "Переопределить USD за миллион токенов ответа")
	flag.Parse()
	if flag.NArg() != 0 || !validID(*session) || *maxTokens < 0 {
		fmt.Fprintln(os.Stderr, "Некорректные аргументы: проверьте -help и имя сессии.")
		os.Exit(1)
	}
	for _, p := range []float64{*inputPrice, *cachedPrice, *outputPrice} {
		if p < 0 || math.IsNaN(p) || math.IsInf(p, 0) {
			fmt.Fprintln(os.Stderr, "Тариф должен быть конечным неотрицательным числом.")
			os.Exit(1)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *demo {
		if err := runDemo(ctx, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	apiKey := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Ошибка: переменная DEEPSEEK_API_KEY не задана.")
		os.Exit(1)
	}
	client := NewDeepSeekClient(
		apiKey,
		envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com/chat/completions"),
		envOr("DEEPSEEK_MODEL", "deepseek-v4-flash"),
		&http.Client{Timeout: 90 * time.Second},
		*maxTokens,
	)
	modelContext := 0
	if limits, ok := limitsForModel(client.Model); ok {
		modelContext = limits.Context
		if client.MaxTokens > 0 {
			fmt.Printf("Лимиты %s: контекст %d · ответ модели до %d · ручной max_tokens %d\n", client.Model, limits.Context, limits.MaxOutput, client.MaxTokens)
		} else {
			fmt.Printf("Лимиты %s: контекст %d · ответ модели до %d · ручной лимит ответа не задан\n", client.Model, limits.Context, limits.MaxOutput)
		}
	} else {
		if client.MaxTokens > 0 {
			fmt.Printf("Лимиты %s: контекст неизвестен · ручной max_tokens %d\n", client.Model, client.MaxTokens)
		} else {
			fmt.Printf("Лимиты %s: контекст неизвестен · ручной лимит ответа не задан\n", client.Model)
		}
	}
	meter := &Meter{Client: client, AllowOverflow: *allowOverflow, ContextLimit: modelContext, Reserve: *maxTokens, InputPrice: *inputPrice, CachedPrice: *cachedPrice, OutputPrice: *outputPrice}
	pricing := &Pricing{Model: client.Model, Endpoint: client.BaseURL}
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "input-price":
			pricing.Input = inputPrice
		case "cached-price":
			pricing.Cached = cachedPrice
		case "output-price":
			pricing.Output = outputPrice
		}
	})
	rates, err := pricing.At(time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	meter.Pricing = pricing
	fmt.Printf("Тариф %s · USD/1M: вход %g · кеш %g · ответ %g\n", rates.Label, rates.Input, rates.Cached, rates.Output)
	store, err := NewJSONFileStore(envOr("DATA_DIR", "./data"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Токены до запроса — приблизительная оценка; после ответа — usage API, если доступен.")

	agent := NewAgent(meter, envOr("AGENT_SYSTEM_PROMPT", "Ты полезный ассистент. Отвечай на языке пользователя."), store, *session)
	if err := runTerminal(ctx, agent, os.Stdin, os.Stdout, *prompt); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
