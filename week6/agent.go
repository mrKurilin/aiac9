package week6

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Agent struct {
	Client  LocalClient
	Model   string
	Options Options
	History []Message
}

func NewAgent(client LocalClient, model string) *Agent {
	if model == "" {
		model = "qwen3:4b-instruct"
	}
	return &Agent{Client: client, Model: model, Options: Options{Temperature: 0.2, NumPredict: 256, NumCtx: 4096}}
}

func (a *Agent) Reset() { a.History = nil }

func (a *Agent) Models(ctx context.Context) (string, error) {
	models, err := a.Client.Models(ctx)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, model := range models {
		lines = append(lines, fmt.Sprintf("%s — %.1f ГБ, %s", model.Name, float64(model.Size)/1e9, model.Details.Quantization))
	}
	if len(lines) == 0 {
		return "Модели не загружены. Для начала: ollama pull " + a.Model, nil
	}
	return strings.Join(lines, "\n"), nil
}

func (a *Agent) Answer(ctx context.Context, prompt string, progress func(string)) (ChatResult, error) {
	messages := append([]Message(nil), a.History...)
	messages = append(messages, Message{Role: "user", Content: prompt})
	if progress != nil {
		progress("Ollama: отправляю запрос к " + a.Model)
	}
	started := time.Now()
	result, err := a.Client.Chat(ctx, a.Model, messages, a.Options)
	if err != nil {
		return ChatResult{}, err
	}
	if ctx.Err() != nil {
		return ChatResult{}, ctx.Err()
	}
	if result.Duration == 0 {
		result.Duration = time.Since(started)
	}
	a.History = append(a.History, Message{Role: "user", Content: prompt}, Message{Role: "assistant", Content: result.Text})
	if progress != nil {
		progress(fmt.Sprintf("Ollama: готово за %s, вход %d / выход %d токенов", result.Duration.Round(time.Millisecond), result.PromptTokens, result.OutputTokens))
	}
	return result, nil
}

var CheckPrompts = []string{
	"Ответь одним предложением: что такое локальная языковая модель?",
	"Перечисли три преимущества и два ограничения локальной LLM для личного проекта.",
	"Составь короткий план интеграции локальной LLM в CLI-агента с HTTP API, отменой запроса и ограничением памяти. Укажи порядок шагов.",
}

func (a *Agent) Check(ctx context.Context, progress func(string)) (string, error) {
	var out strings.Builder
	for i, prompt := range CheckPrompts {
		if progress != nil {
			progress(fmt.Sprintf("Проверка %d/%d", i+1, len(CheckPrompts)))
		}
		result, err := a.Client.Chat(ctx, a.Model, []Message{{Role: "user", Content: prompt}}, a.Options)
		if err != nil {
			return out.String(), fmt.Errorf("запрос %d: %w", i+1, err)
		}
		fmt.Fprintf(&out, "%d. %s\n%s\nВремя: %s; токены: %d/%d\n\n", i+1, prompt, result.Text, result.Duration.Round(time.Millisecond), result.PromptTokens, result.OutputTokens)
	}
	return strings.TrimSpace(out.String()), nil
}
