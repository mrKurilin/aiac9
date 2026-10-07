package week6

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type ChatGenerator interface {
	Chat(context.Context, string, []Message, Options) (ChatResult, error)
}

type DeepSeek struct {
	BaseURL string
	key     string
	HTTP    *http.Client
}

var cloudSensitiveText = regexp.MustCompile(`(?i)(?:\b(?:password|passwd|token|api[_-]?key|secret|credential)\s*[:=]\s*\S+|\b(?:sk-[A-Za-z0-9]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|glpat-[A-Za-z0-9_-]{18,})\b|://[^\s/:@]+:[^\s/:@]+@|\b[\w-]+(?:\.[\w-]+)*\.(?:local|internal|intranet|corp|lan|svc|priv|vpn)\b)`)
var cloudIPv4 = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

func safeCloudMessages(messages []Message) bool {
	for _, message := range messages {
		if cloudSensitiveText.MatchString(message.Content) {
			return false
		}
		for _, value := range cloudIPv4.FindAllString(message.Content, -1) {
			if ip := net.ParseIP(value); ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
				return false
			}
		}
	}
	return true
}

func NewDeepSeek(baseURL, apiKey string) *DeepSeek {
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	return &DeepSeek{BaseURL: strings.TrimRight(baseURL, "/"), key: apiKey, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

func (c *DeepSeek) Chat(ctx context.Context, model string, messages []Message, options Options) (ChatResult, error) {
	if c.key == "" {
		return ChatResult{}, errors.New("для /compare задайте DEEPSEEK_API_KEY")
	}
	if !safeCloudMessages(messages) {
		return ChatResult{}, errors.New("контекст содержит чувствительные данные; облачный запрос остановлен")
	}
	endpoint, err := url.Parse(c.BaseURL)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && net.ParseIP(endpoint.Hostname()) != nil && net.ParseIP(endpoint.Hostname()).IsLoopback())) {
		return ChatResult{}, errors.New("облачный API требует HTTPS")
	}
	payload, err := json.Marshal(map[string]any{
		"model": model, "messages": messages, "stream": false,
		"temperature": options.Temperature, "max_tokens": options.NumPredict,
		"thinking": map[string]string{"type": "disabled"},
	})
	if err != nil {
		return ChatResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return ChatResult{}, errors.New("неверный адрес облачного API")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	started := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ChatResult{}, ctx.Err()
		}
		return ChatResult{}, errors.New("облачный API недоступен")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatResult{}, fmt.Errorf("облачный API вернул HTTP %d", resp.StatusCode)
	}
	var data struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&data); err != nil {
		return ChatResult{}, errors.New("не удалось прочитать ответ облачного API")
	}
	if len(data.Choices) == 0 || strings.TrimSpace(data.Choices[0].Message.Content) == "" {
		return ChatResult{}, errors.New("облачная модель вернула пустой ответ")
	}
	return ChatResult{
		Text:         strings.TrimSpace(data.Choices[0].Message.Content),
		PromptTokens: data.Usage.PromptTokens,
		OutputTokens: data.Usage.CompletionTokens,
		Duration:     time.Since(started),
	}, nil
}
