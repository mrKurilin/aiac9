package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// Estimates are deliberately independent of any provider tokenizer.
// UTF-8 bytes/3 plus message framing is a heuristic, not a context guarantee.
func estimateText(s string) int {
	if strings.HasPrefix(s, contextFillerPrefix) {
		body := strings.TrimPrefix(s, contextFillerPrefix)
		prefixTokens := (len(contextFillerPrefix) + 2) / 3
		// Recognize the old filler too, so saved sessions get corrected estimates.
		if len(body)%3 == 0 && body == strings.Repeat(" x ", len(body)/3) {
			return prefixTokens + 2*(len(body)/3)
		}
		if len(body)%2 == 0 && body == strings.Repeat(" x", len(body)/2) {
			return prefixTokens + len(body)/2
		}
	}
	return (len(s) + 2) / 3
}
func estimateMessages(messages []Message) int {
	if len(messages) == 0 {
		return 0
	}
	n := 3
	for _, m := range messages {
		n += 4 + estimateText(m.Role) + estimateText(m.Content)
	}
	return n
}

type Usage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	CacheHit   int `json:"prompt_cache_hit_tokens"`
}
type Meter struct {
	Pricing                              *Pricing
	RequestStarted, RequestFinished      func()
	InputCost, OutputCost                float64
	Estimated                            int
	AllowOverflow                        bool
	Report                               io.Writer
	Client                               ChatClient
	Out                                  io.Writer
	ContextLimit, Reserve                int
	InputPrice, CachedPrice, OutputPrice float64
	Turns, Input, Output, Unknown        int
	Cost                                 float64
}

func (m *Meter) Complete(ctx context.Context, messages []Message) (string, error) {
	return m.CompleteStream(ctx, messages, nil)
}
func (m *Meter) CompleteStream(ctx context.Context, messages []Message, emit func(string) error) (string, error) {
	out := m.Out
	if out == nil {
		out = io.Discard
	}
	current, history := 0, []Message{}
	if len(messages) > 0 {
		current = estimateText(messages[len(messages)-1].Content)
		for _, msg := range messages[:len(messages)-1] {
			if msg.Role != "system" {
				history = append(history, msg)
			}
		}
	}
	input := estimateMessages(messages)
	fmt.Fprintf(out, "  Запрос ≈%d · история ≈%d · вход ≈%d токенов\n", current, estimateMessages(history), input)
	if m.ContextLimit > 0 {
		if m.Reserve > 0 {
			fmt.Fprintf(out, "  Окно: ≈%d + резерв ответа %d / %d · %.1f%%\n", input, m.Reserve, m.ContextLimit, 100*float64(input+m.Reserve)/float64(m.ContextLimit))
		} else {
			fmt.Fprintf(out, "  Окно: ≈%d / %d · %.1f%% · ответ без ручного лимита\n", input, m.ContextLimit, 100*float64(input)/float64(m.ContextLimit))
		}
		if input+m.Reserve > m.ContextLimit && !m.AllowOverflow {
			return "", fmt.Errorf("окно переполнено: запрос не отправлен. /overflow on — отправлять в API; /reset — очистить")
		}
	}
	rates := Prices{Input: m.InputPrice, Cached: m.CachedPrice, Output: m.OutputPrice, Label: "ручные тарифы"}
	if m.Pricing != nil {
		var priceErr error
		rates, priceErr = m.Pricing.At(time.Now())
		if priceErr != nil {
			return "", priceErr
		}
	}
	var answer string
	var err error
	if m.RequestStarted != nil {
		m.RequestStarted()
	}
	if m.RequestFinished != nil {
		defer m.RequestFinished()
	}
	if streaming, ok := m.Client.(StreamingChatClient); ok && emit != nil {
		answer, err = streaming.CompleteStream(ctx, messages, emit)
	} else {
		answer, err = m.Client.Complete(ctx, messages)
		if err == nil && emit != nil {
			err = emit(answer)
		}
	}
	if m.Report != nil {
		out = m.Report
	}
	usage := (*Usage)(nil)
	finish := ""
	if client, ok := m.Client.(*DeepSeekClient); ok {
		usage = client.LastUsage
		finish = client.FinishReason
	}
	if err != nil {
		m.Unknown++
		fmt.Fprintln(out, "\nХод не сохранён. Расход не учтён: при сбое API мог уже потратить токены.")
		return "", err
	}
	source := "оценка"
	if usage == nil {
		m.Estimated++
	}
	u := Usage{Prompt: input, Completion: estimateText(answer)}
	if usage != nil {
		u = *usage
		source = "API usage"
	}
	hits := u.CacheHit
	if hits < 0 {
		hits = 0
	}
	if hits > u.Prompt {
		hits = u.Prompt
	}
	inputCost := (float64(u.Prompt-hits)*rates.Input + float64(hits)*rates.Cached) / 1e6
	outputCost := float64(u.Completion) * rates.Output / 1e6
	cost := inputCost + outputCost
	m.InputCost += inputCost
	m.OutputCost += outputCost
	m.Turns++
	m.Input += u.Prompt
	m.Output += u.Completion
	m.Cost += cost
	fmt.Fprintf(out, "  Токены (%s): вход %d ($%.8f) · ответ %d ($%.8f)\n", source, u.Prompt, inputCost, u.Completion, outputCost)
	fmt.Fprintf(out, "  Ход $%.8f · за запуск $%.8f · %s · оценочных ходов %d\n", cost, m.Cost, rates.Label, m.Estimated)
	if finish == "length" {
		if client, ok := m.Client.(*DeepSeekClient); ok {
			return "", fmt.Errorf("ответ обрезан (finish_reason=length). %s", lengthDiagnostic(client, u))
		}
		return "", fmt.Errorf("ответ обрезан (finish_reason=length): API не сообщил конкретный лимит; вход %d, ответ %d токенов; токены учтены, незавершённый ход не сохранён", u.Prompt, u.Completion)
	}
	return answer, nil
}
func (m *Meter) Summary(out io.Writer) {
	fmt.Fprintf(out, "Стоимость: вход $%.8f · ответы $%.8f · оценочных ходов %d\n", m.InputCost, m.OutputCost, m.Estimated)
	fmt.Fprintf(out, "За запуск: ходов %d | вход %d + ответ %d = %d токенов | $%.8f | сбоев с неизвестным расходом %d\n", m.Turns, m.Input, m.Output, m.Input+m.Output, m.Cost, m.Unknown)
}
