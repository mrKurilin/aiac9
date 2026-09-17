package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatClient interface {
	Complete(context.Context, []Message) (string, error)
}

type StreamingChatClient interface {
	ChatClient
	CompleteStream(context.Context, []Message, func(string) error) (string, error)
}

type DeepSeekClient struct {
	Key     string
	BaseURL string
	Model   string
	HTTP    *http.Client
}

func NewDeepSeekClient(apiKey, baseURL, model string, httpClient *http.Client) *DeepSeekClient {
	return &DeepSeekClient{Key: apiKey, BaseURL: baseURL, Model: model, HTTP: httpClient}
}

func (c *DeepSeekClient) Complete(ctx context.Context, messages []Message) (string, error) {
	body, err := json.Marshal(map[string]any{"model": c.Model, "messages": messages})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("вызвать DeepSeek API: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	var result struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
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
	answer := strings.TrimSpace(result.Choices[0].Message.Content)
	if answer == "" {
		return "", fmt.Errorf("DeepSeek вернул пустой ответ")
	}
	return answer, nil
}

func (c *DeepSeekClient) CompleteStream(ctx context.Context, messages []Message, emit func(string) error) (string, error) {
	body, err := json.Marshal(map[string]any{"model": c.Model, "messages": messages, "stream": true})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("вызвать DeepSeek API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return "", fmt.Errorf("DeepSeek API, HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var result strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			if result.Len() == 0 {
				return "", fmt.Errorf("в потоке DeepSeek не было текста")
			}
			return result.String(), nil
		}
		var event struct {
			Choices []struct {
				Delta Message `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return "", fmt.Errorf("разобрать фрагмент DeepSeek: %w", err)
		}
		if event.Error != nil {
			return "", fmt.Errorf("DeepSeek API: %s", event.Error.Message)
		}
		for _, choice := range event.Choices {
			chunk := choice.Delta.Content
			if chunk == "" {
				continue
			}
			result.WriteString(chunk)
			if emit != nil {
				if err := emit(chunk); err != nil {
					return "", err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("читать поток DeepSeek: %w", err)
	}
	return "", fmt.Errorf("поток DeepSeek оборвался до [DONE]")
}
