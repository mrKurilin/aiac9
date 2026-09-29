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
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
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
	message, err := c.completeMessage(ctx, map[string]any{"model": c.Model, "messages": messages})
	if err != nil {
		return "", err
	}
	answer := strings.TrimSpace(message.Content)
	if answer == "" {
		return "", fmt.Errorf("DeepSeek вернул пустой ответ")
	}
	return answer, nil
}

func (c *DeepSeekClient) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDefinition) (Message, error) {
	request := map[string]any{"model": c.Model, "messages": messages}
	if len(tools) > 0 {
		request["tools"] = tools
	}
	message, err := c.completeMessage(ctx, request)
	if err != nil {
		return Message{}, err
	}
	message.Content = strings.TrimSpace(message.Content)
	if message.Content == "" && len(message.ToolCalls) == 0 {
		return Message{}, fmt.Errorf("DeepSeek вернул пустой ответ")
	}
	return message, nil
}

func (c *DeepSeekClient) completeMessage(ctx context.Context, request map[string]any) (Message, error) {
	if strings.TrimSpace(c.Key) == "" {
		return Message{}, fmt.Errorf("для запроса на естественном языке задайте DEEPSEEK_API_KEY")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Message{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Message{}, ctx.Err()
		}
		return Message{}, fmt.Errorf("сетевая ошибка DeepSeek API")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Message{}, err
	}
	var result struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return Message{}, fmt.Errorf("разобрать ответ DeepSeek (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Message{}, fmt.Errorf("DeepSeek API, HTTP %d", resp.StatusCode)
	}
	if len(result.Choices) == 0 {
		return Message{}, fmt.Errorf("в ответе DeepSeek нет choices")
	}
	return result.Choices[0].Message, nil
}

func (c *DeepSeekClient) CompleteStream(ctx context.Context, messages []Message, emit func(string) error) (string, error) {
	if strings.TrimSpace(c.Key) == "" {
		return "", fmt.Errorf("для запроса на естественном языке задайте DEEPSEEK_API_KEY")
	}
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
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("сетевая ошибка DeepSeek API")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("DeepSeek API, HTTP %d", resp.StatusCode)
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
