package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Message is one item in the conversation with the model.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

var contextLengthError = regexp.MustCompile(`maximum context length is ([0-9]+) tokens.*requested ([0-9]+) tokens \(([0-9]+) in the messages, ([0-9]+) in the completion\)`)

func deepSeekHTTPError(status int, message string) error {
	match := contextLengthError.FindStringSubmatch(message)
	if len(match) == 5 {
		limit, _ := strconv.Atoi(match[1])
		requested, _ := strconv.Atoi(match[2])
		messages, _ := strconv.Atoi(match[3])
		completion, _ := strconv.Atoi(match[4])
		return fmt.Errorf("DeepSeek API, HTTP %d: контекст переполнен — сообщения %d + ответ %d = %d токенов, лимит модели %d. Это размер одного текущего запроса (system + вся история + новое сообщение), а не сумма прошлых вызовов. Число API точнее локальной оценки",
			status, messages, completion, requested, limit)
	}
	return fmt.Errorf("DeepSeek API, HTTP %d: %s", status, message)
}

// ChatClient describes the only capability the agent needs from an LLM client.
// Keeping this interface separate makes the agent easy to test or move to another UI.
type ChatClient interface {
	Complete(ctx context.Context, messages []Message) (string, error)
}

// StreamingChatClient is the optional capability of a ChatClient to deliver the
// answer piece by piece through emit. The agent uses it when available and falls
// back to a single Complete call otherwise, so a non-streaming backend still works.
type StreamingChatClient interface {
	ChatClient
	CompleteStream(ctx context.Context, messages []Message, emit func(string) error) (string, error)
}

type DeepSeekClient struct {
	APIKey       string
	BaseURL      string
	Model        string
	HTTP         *http.Client
	MaxTokens    int
	LastUsage    *Usage
	FinishReason string
}

func NewDeepSeekClient(apiKey, baseURL, model string, httpClient *http.Client, maxTokens int) *DeepSeekClient {
	// The credential remains an in-memory runtime value and is never stored in
	// conversation history or serialized into the request body.
	return &DeepSeekClient{apiKey, baseURL, model, httpClient, maxTokens, nil, ""}
}

type chatRequest struct {
	Model         string          `json:"model"`
	Messages      []Message       `json:"messages"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	StreamOptions map[string]bool `json:"stream_options,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
}

type chatResponse struct {
	Usage   *Usage `json:"usage"`
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *DeepSeekClient) Complete(ctx context.Context, messages []Message) (string, error) {
	c.LastUsage, c.FinishReason = nil, ""
	body, err := json.Marshal(chatRequest{Model: c.Model, Messages: messages, MaxTokens: c.MaxTokens})
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
		return "", deepSeekHTTPError(resp.StatusCode, message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("в ответе DeepSeek нет choices")
	}
	c.LastUsage = result.Usage
	c.FinishReason = result.Choices[0].FinishReason
	return result.Choices[0].Message.Content, nil
}

// CompleteStream requests a streaming answer from DeepSeek and forwards each
// piece of text to emit as it arrives. It returns the fully assembled answer so
// the caller can persist it once, exactly like a non-streaming Complete.
func (c *DeepSeekClient) CompleteStream(ctx context.Context, messages []Message, emit func(string) error) (string, error) {
	c.LastUsage, c.FinishReason = nil, ""
	body, err := json.Marshal(chatRequest{Model: c.Model, Messages: messages, Stream: true, MaxTokens: c.MaxTokens, StreamOptions: map[string]bool{"include_usage": true}})
	if err != nil {
		return "", fmt.Errorf("подготовить запрос: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("создать запрос: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("вызвать DeepSeek API: %w", err)
	}
	defer resp.Body.Close()

	// Errors come back as ordinary JSON, not as a stream.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		message := strings.TrimSpace(string(data))
		var errBody struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &errBody) == nil && errBody.Error != nil && errBody.Error.Message != "" {
			message = errBody.Error.Message
		}
		return "", deepSeekHTTPError(resp.StatusCode, message)
	}

	var sb strings.Builder
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSpace(line)
		}
		if err != nil {
			if err == io.EOF {
				return "", fmt.Errorf("поток DeepSeek оборвался до [DONE]")
			}
			return "", fmt.Errorf("читать поток DeepSeek: %w", err)
		}
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if string(payload) == "[DONE]" {
			break
		}
		var chunk struct {
			Usage   *Usage `json:"usage"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return "", fmt.Errorf("разобрать фрагмент DeepSeek: %w", err)
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return "", fmt.Errorf("DeepSeek API: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			c.LastUsage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				c.FinishReason = choice.FinishReason
			}
			delta := choice.Delta.Content
			if delta == "" {
				continue
			}
			sb.WriteString(delta)
			if emit != nil {
				if err := emit(delta); err != nil {
					return "", err
				}
			}
		}
	}
	if sb.Len() == 0 {
		return "", fmt.Errorf("в потоке DeepSeek не было текста")
	}
	return sb.String(), nil
}
