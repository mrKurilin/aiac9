package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

// StreamingChatClient is the optional capability of a ChatClient to deliver the
// answer piece by piece through emit. The agent uses it when available and falls
// back to a single Complete call otherwise, so a non-streaming backend still works.
type StreamingChatClient interface {
	ChatClient
	CompleteStream(ctx context.Context, messages []Message, emit func(string) error) (string, error)
}

// Agent owns the conversation and coordinates requests to an LLM.
// This is intentionally a separate entity rather than a raw API call from main.
type Agent struct {
	client  ChatClient
	system  string
	store   Store
	id      string
	mu      sync.Mutex
	history []Message
}

// NewAgent creates an agent and restores the durable history of the
// conversation id from the store, so a restart continues where it stopped.
// The in-memory history is short-term memory; the store is long-term memory.
func NewAgent(client ChatClient, systemPrompt string, store Store, id string) *Agent {
	a := &Agent{
		client: client,
		system: strings.TrimSpace(systemPrompt),
		store:  store,
		id:     id,
	}
	if store == nil {
		return a
	}
	messages, err := store.Load(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Не удалось загрузить историю диалога %s: %v (начинаю с чистого листа)\n", id, err)
		return a
	}
	a.history = messages
	return a
}

// Ask adds a user message, calls the model and remembers a successful answer.
// Every completed exchange is flushed to the store, so memory survives a crash
// or a restart.
func (a *Agent) Ask(ctx context.Context, prompt string) (string, error) {
	return a.AskStream(ctx, prompt, nil)
}

// AskStream is Ask with live delivery of every piece of the answer through emit.
// emit is optional — nil behaves exactly like Ask. Only a fully successful
// exchange is remembered, so an interrupted stream never pollutes the context.
func (a *Agent) AskStream(ctx context.Context, prompt string, emit func(string) error) (string, error) {
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

	var answer string
	var err error
	if streamer, ok := a.client.(StreamingChatClient); ok && emit != nil {
		answer, err = streamer.CompleteStream(ctx, messages, emit)
	} else {
		answer, err = a.client.Complete(ctx, messages)
		if err == nil && emit != nil {
			err = emit(answer)
		}
	}
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
	a.persist()
	return answer, nil
}

func (a *Agent) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = nil
	a.persist()
}

// History returns a snapshot of the remembered conversation so the UI can
// render it without racing with in-flight requests. The system prompt is
// never part of the stored/returned history.
func (a *Agent) History() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Message, len(a.history))
	copy(out, a.history)
	return out
}

// persist must be called with a.mu held. Only a successful exchange reaches
// the store: failed API calls never pollute the remembered context.
func (a *Agent) persist() {
	if a.store == nil {
		return
	}
	if err := a.store.Save(a.id, a.history); err != nil {
		fmt.Fprintf(os.Stderr, "Не удалось сохранить историю диалога %s: %v\n", a.id, err)
	}
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
	Stream   bool      `json:"stream,omitempty"`
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

// CompleteStream requests a streaming answer from DeepSeek and forwards each
// piece of text to emit as it arrives. It returns the fully assembled answer so
// the caller can persist it once, exactly like a non-streaming Complete.
func (c *DeepSeekClient) CompleteStream(ctx context.Context, messages []Message, emit func(string) error) (string, error) {
	body, err := json.Marshal(chatRequest{Model: c.Model, Messages: messages, Stream: true})
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
		return "", fmt.Errorf("DeepSeek API, HTTP %d: %s", resp.StatusCode, message)
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
			Choices []struct {
				Delta struct {
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
		for _, choice := range chunk.Choices {
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
