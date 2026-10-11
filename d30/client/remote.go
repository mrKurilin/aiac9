package main

import (
	"aiac9-week6"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type remoteClient struct {
	baseURL   string
	token     string
	sessionID string
	http      *http.Client
}

func newRemoteClient(rawURL, token string) (*remoteClient, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("MRKAI_API_URL должен быть адресом HTTP(S) без пути и учётных данных")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("создать ID диалога: %w", err)
	}
	return &remoteClient{strings.TrimRight(u.String(), "/"), token, hex.EncodeToString(id[:]), &http.Client{Timeout: 4 * time.Minute}}, nil
}

func (c *remoteClient) request(ctx context.Context, method, path string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP API недоступен: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP API вернул статус %d", resp.StatusCode)
	}
	if result != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(result); err != nil {
			return fmt.Errorf("ответ HTTP API: %w", err)
		}
	}
	return nil
}

func (c *remoteClient) Models(ctx context.Context) ([]week6.Model, error) {
	var health struct {
		Model string `json:"model"`
	}
	if err := c.request(ctx, http.MethodGet, "/health", nil, &health); err != nil {
		return nil, err
	}
	return []week6.Model{{Name: health.Model}}, nil
}

func (c *remoteClient) Chat(ctx context.Context, _ string, messages []week6.Message, _ week6.Options) (week6.ChatResult, error) {
	if len(messages) == 0 || messages[len(messages)-1].Role != "user" {
		return week6.ChatResult{}, errors.New("последнее сообщение должно быть от пользователя")
	}
	var answer struct {
		Message      week6.Message `json:"message"`
		PromptTokens int           `json:"prompt_tokens"`
		OutputTokens int           `json:"output_tokens"`
		DurationMS   int64         `json:"duration_ms"`
	}
	err := c.request(ctx, http.MethodPost, "/v1/chat", map[string]string{
		"session_id": c.sessionID,
		"message":    messages[len(messages)-1].Content,
	}, &answer)
	if err != nil {
		return week6.ChatResult{}, err
	}
	return week6.ChatResult{Text: answer.Message.Content, PromptTokens: answer.PromptTokens, OutputTokens: answer.OutputTokens, Duration: time.Duration(answer.DurationMS) * time.Millisecond}, nil
}

func (c *remoteClient) Reset(ctx context.Context) error {
	return c.request(ctx, http.MethodPost, "/v1/reset", map[string]string{"session_id": c.sessionID}, nil)
}
