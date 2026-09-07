package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Message is one item in the conversation with the model.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatClient describes the only capability the agent needs from an LLM client.
// Keeping this interface separate makes the agent easy to test or move to another UI.
type ChatClient interface {
	Complete(ctx context.Context, messages []Message) (string, error)
}

// Agent owns the conversation and coordinates requests to an LLM.
// This is intentionally a separate entity rather than a raw API call from main.
type Agent struct {
	client  ChatClient
	system  string
	mu      sync.Mutex
	history []Message
}

func NewAgent(client ChatClient, systemPrompt string) *Agent {
	return &Agent{client: client, system: strings.TrimSpace(systemPrompt)}
}

// Ask adds a user message, calls the model and remembers a successful answer.
func (a *Agent) Ask(ctx context.Context, prompt string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("пустой запрос")
	}

	// Serialize calls so concurrent interfaces cannot interleave one conversation.
	a.mu.Lock()
	defer a.mu.Unlock()

	messages := make([]Message, 0, len(a.history)+2)
	if a.system != "" {
		messages = append(messages, Message{Role: "system", Content: a.system})
	}
	messages = append(messages, a.history...)
	messages = append(messages, Message{Role: "user", Content: prompt})

	answer, err := a.client.Complete(ctx, messages)
	if err != nil {
		return "", err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", fmt.Errorf("модель вернула пустой ответ")
	}

	a.history = append(a.history,
		Message{Role: "user", Content: prompt},
		Message{Role: "assistant", Content: answer},
	)
	return answer, nil
}

func (a *Agent) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = nil
}

type DeepSeekClient struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *DeepSeekClient) Complete(ctx context.Context, messages []Message) (string, error) {
	body, err := json.Marshal(chatRequest{Model: c.Model, Messages: messages})
	if err != nil {
		return "", fmt.Errorf("подготовить запрос: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("создать запрос: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("вызвать DeepSeek API: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("прочитать ответ: %w", err)
	}
	var result chatResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("разобрать ответ DeepSeek (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(data))
		if result.Error != nil && result.Error.Message != "" {
			message = result.Error.Message
		}
		return "", fmt.Errorf("DeepSeek API, HTTP %d: %s", resp.StatusCode, message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("в ответе DeepSeek нет choices")
	}
	return result.Choices[0].Message.Content, nil
}
