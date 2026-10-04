package week6

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Options struct {
	Temperature float64 `json:"temperature"`
	NumPredict  int     `json:"num_predict"`
	NumCtx      int     `json:"num_ctx"`
}

type ChatResult struct {
	Text         string
	PromptTokens int
	OutputTokens int
	Duration     time.Duration
}

type Model struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Details struct {
		Quantization string `json:"quantization_level"`
	} `json:"details"`
}

type LocalClient interface {
	Models(context.Context) ([]Model, error)
	Chat(context.Context, string, []Message, Options) (ChatResult, error)
}

type Ollama struct {
	BaseURL string
	HTTP    *http.Client
}

func NewOllama(baseURL string) *Ollama {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434"
	}
	return &Ollama{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

func (c *Ollama) request(ctx context.Context, method, path string, payload any, result any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("Ollama недоступна: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Ollama вернула HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(result); err != nil {
		return fmt.Errorf("прочитать ответ Ollama: %w", err)
	}
	return nil
}

func (c *Ollama) Models(ctx context.Context) ([]Model, error) {
	var data struct {
		Models []Model `json:"models"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/tags", nil, &data); err != nil {
		return nil, err
	}
	return data.Models, nil
}

func (c *Ollama) Chat(ctx context.Context, model string, messages []Message, options Options) (ChatResult, error) {
	var data struct {
		Message            Message `json:"message"`
		PromptEvalCount    int     `json:"prompt_eval_count"`
		EvalCount          int     `json:"eval_count"`
		TotalDurationNanos int64   `json:"total_duration"`
	}
	err := c.request(ctx, http.MethodPost, "/api/chat", map[string]any{
		"model": model, "messages": messages, "stream": false, "think": false, "options": options,
	}, &data)
	if err != nil {
		return ChatResult{}, err
	}
	text := strings.TrimSpace(data.Message.Content)
	if text == "" {
		return ChatResult{}, errors.New("модель вернула пустой ответ")
	}
	return ChatResult{text, data.PromptEvalCount, data.EvalCount, time.Duration(data.TotalDurationNanos)}, nil
}

func (c *Ollama) Embed(ctx context.Context, model string, texts []string) ([][]float64, error) {
	var data struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := c.request(ctx, http.MethodPost, "/api/embed", map[string]any{
		"model": model, "input": texts, "truncate": true,
	}, &data); err != nil {
		return nil, err
	}
	if len(data.Embeddings) != len(texts) {
		return nil, errors.New("Ollama вернула неверное число эмбеддингов")
	}
	return data.Embeddings, nil
}
