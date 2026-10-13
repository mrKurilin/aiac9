package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatClient interface {
	Complete(context.Context, []message) (message, error)
}

type deepSeekClient struct {
	key, url, model string
	http            *http.Client
}

func (c deepSeekClient) Complete(ctx context.Context, messages []message) (message, error) {
	if c.key == "" {
		return message{}, fmt.Errorf("задайте DEEPSEEK_API_KEY")
	}
	payload := map[string]any{"model": c.model, "messages": messages, "stream": false}
	data, err := json.Marshal(payload)
	if err != nil {
		return message{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(data))
	if err != nil {
		return message{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return message{}, ctx.Err()
		}
		return message{}, fmt.Errorf("ошибка соединения с моделью")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return message{}, fmt.Errorf("модель вернула HTTP %d", resp.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message message `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil {
		return message{}, fmt.Errorf("неверный ответ модели: %w", err)
	}
	if len(result.Choices) == 0 {
		return message{}, fmt.Errorf("модель не вернула ответ")
	}
	return result.Choices[0].Message, nil
}

type agent struct {
	client  chatClient
	search  searchIndex
	history []message
}

func (a *agent) reset() { a.history = nil }

func (a *agent) answer(ctx context.Context, question string, progress func(string)) (string, error) {
	messages := []message{{Role: "system", Content: "Ты помощник разработчика. Отвечай на русском языке."}}
	messages = append(messages, a.history...)
	messages = append(messages, message{Role: "user", Content: question})
	progress("Модель: отправляю сообщение")
	started := time.Now()
	reply, err := a.client.Complete(ctx, messages)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reply.Content) == "" {
		return "", fmt.Errorf("модель вернула пустой ответ")
	}
	progress(fmt.Sprintf("Модель: ответ получен за %s", time.Since(started).Round(time.Millisecond)))
	a.history = append(a.history, message{Role: "user", Content: question}, message{Role: "assistant", Content: reply.Content})
	return reply.Content, nil
}
