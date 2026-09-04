package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

//go:embed index.html
var staticFiles embed.FS

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionResponse struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type openAIResponse struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

type requestBody struct {
	Provider  string         `json:"provider"`
	Model     string         `json:"model"`
	Prompt    string         `json:"prompt"`
	MaxTokens int            `json:"max_tokens"`
	Params    map[string]any `json:"params"`
}

type app struct {
	apiKeyDeepSeek string
	apiKeyOpenAI   string
	baseDeepSeek   string
	baseOpenAI     string
	client         *http.Client
}

func newApp() *app {
	return &app{
		apiKeyDeepSeek: os.Getenv("DEEPSEEK_API_KEY"),
		apiKeyOpenAI:   os.Getenv("OPENAI_API_KEY"),
		baseDeepSeek:   envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com/chat/completions"),
		baseOpenAI:     envOr("OPENAI_BASE_URL", "https://api.openai.com/v1/responses"),
		client:         http.DefaultClient,
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	a := newApp()
	if a.apiKeyDeepSeek == "" && a.apiKeyOpenAI == "" {
		fmt.Fprintln(os.Stderr, "задайте хотя бы один ключ: DEEPSEEK_API_KEY или OPENAI_API_KEY")
		os.Exit(1)
	}
	if a.apiKeyDeepSeek == "" {
		fmt.Fprintln(os.Stderr, "внимание: DEEPSEEK_API_KEY не задан — колонки DeepSeek будут возвращать ошибку")
	}
	if a.apiKeyOpenAI == "" {
		fmt.Fprintln(os.Stderr, "внимание: OPENAI_API_KEY не задан — колонки OpenAI будут возвращать ошибку")
	}
	fmt.Println("Сервер запущен: http://localhost:8080")
	if err := http.ListenAndServe(":8080", a.handler()); err != nil {
		panic(err)
	}
}

func (a *app) handler() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.index)
	mux.HandleFunc("/api/complete", a.complete)
	return mux
}

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, _ := staticFiles.ReadFile("index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

func (a *app) complete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	var body requestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt is empty"})
		return
	}
	if body.MaxTokens <= 0 {
		body.MaxTokens = 2000
	}

	var result map[string]any
	var err error
	switch body.Provider {
	case "deepseek":
		result, err = a.completeDeepSeek(body)
	case "openai":
		result, err = a.completeOpenAI(body)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown provider: " + body.Provider})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (a *app) completeDeepSeek(body requestBody) (map[string]any, error) {
	if a.apiKeyDeepSeek == "" {
		return nil, fmt.Errorf("DEEPSEEK_API_KEY не задан")
	}

	req := map[string]any{
		"model":      body.Model,
		"messages":   []message{{Role: "user", Content: body.Prompt}},
		"max_tokens": body.MaxTokens,
	}
	for k, v := range body.Params {
		req[k] = v
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	apiReq, err := http.NewRequest(http.MethodPost, a.baseDeepSeek, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("Authorization", "Bearer "+a.apiKeyDeepSeek)

	resp, err := a.client.Do(apiReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, apiErrorMessage(resp.StatusCode, respBody)
	}

	var result completionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("нет choices в ответе API")
	}

	return map[string]any{
		"content":           result.Choices[0].Message.Content,
		"finish_reason":     result.Choices[0].FinishReason,
		"prompt_tokens":     result.Usage.PromptTokens,
		"completion_tokens": result.Usage.CompletionTokens,
		"total_tokens":      result.Usage.TotalTokens,
	}, nil
}

func (a *app) completeOpenAI(body requestBody) (map[string]any, error) {
	if a.apiKeyOpenAI == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY не задан")
	}

	req := map[string]any{
		"model":             body.Model,
		"max_output_tokens": body.MaxTokens,
		"input":             body.Prompt,
	}
	for k, v := range body.Params {
		req[k] = v
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	apiReq, err := http.NewRequest(http.MethodPost, a.baseOpenAI, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("Authorization", "Bearer "+a.apiKeyOpenAI)

	resp, err := a.client.Do(apiReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, apiErrorMessage(resp.StatusCode, respBody)
	}

	var result openAIResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	var text strings.Builder
	for _, item := range result.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" {
				text.WriteString(content.Text)
			}
		}
	}

	input := result.Usage.InputTokens
	output := result.Usage.OutputTokens
	finishReason := result.Status
	if result.IncompleteDetails != nil && result.IncompleteDetails.Reason != "" {
		finishReason = result.IncompleteDetails.Reason
	}
	return map[string]any{
		"content":           text.String(),
		"finish_reason":     finishReason,
		"prompt_tokens":     input,
		"completion_tokens": output,
		"total_tokens":      result.Usage.TotalTokens,
	}, nil
}

func apiErrorMessage(status int, body []byte) error {
	msg := fmt.Sprintf("API вернул статус %d", status)
	var apiErr apiError
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
		msg = apiErr.Error.Message
	}
	return fmt.Errorf("%s", msg)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
