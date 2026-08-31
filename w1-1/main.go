package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

//go:embed index.html
var staticFiles embed.FS

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionRequest struct {
	Model            string    `json:"model"`
	Messages         []message `json:"messages"`
	Temperature      *float64  `json:"temperature,omitempty"`
	TopP             *float64  `json:"top_p,omitempty"`
	TopK             *int      `json:"top_k,omitempty"`
	MaxTokens        *int      `json:"max_tokens,omitempty"`
	Seed             *int      `json:"seed,omitempty"`
	PresencePenalty  *float64  `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64  `json:"frequency_penalty,omitempty"`
}

type completionResponse struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

type params struct {
	Temperature      *float64 `json:"temperature"`
	TopP             *float64 `json:"top_p"`
	TopK             *int     `json:"top_k"`
	MaxTokens        *int     `json:"max_tokens"`
	Seed             *int     `json:"seed"`
	PresencePenalty  *float64 `json:"presence_penalty"`
	FrequencyPenalty *float64 `json:"frequency_penalty"`
}

type requestBody struct {
	Prompt string `json:"prompt"`
	Params params `json:"params"`
}

type app struct {
	apiKey  string
	client  *http.Client
	baseURL string
}

func newApp(apiKey string) *app {
	return &app{
		apiKey:  apiKey,
		client:  http.DefaultClient,
		baseURL: "https://api.deepseek.com/chat/completions",
	}
}

func main() {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "DEEPSEEK_API_KEY is not set")
		os.Exit(1)
	}

	a := newApp(apiKey)
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
	if body.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt is empty"})
		return
	}

	req := buildCompletionRequest(body.Prompt, body.Params)

	data, err := json.Marshal(req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	apiReq, err := http.NewRequest(http.MethodPost, a.baseURL, bytes.NewReader(data))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.client.Do(apiReq)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		var apiErr apiError
		msg := fmt.Sprintf("API вернул статус %d", resp.StatusCode)
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			msg = apiErr.Error.Message
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": msg})
		return
	}

	var result completionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(result.Choices) == 0 {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "no choices in API response"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"content": result.Choices[0].Message.Content})
}

func buildCompletionRequest(prompt string, p params) completionRequest {
	req := completionRequest{
		Model:            "deepseek-chat",
		Messages:         []message{{Role: "user", Content: prompt}},
		Temperature:      p.Temperature,
		PresencePenalty:  p.PresencePenalty,
		FrequencyPenalty: p.FrequencyPenalty,
	}
	if p.TopP != nil && *p.TopP > 0 {
		req.TopP = p.TopP
	}
	if p.TopK != nil && *p.TopK > 0 {
		req.TopK = p.TopK
	}
	if p.MaxTokens != nil && *p.MaxTokens > 0 {
		req.MaxTokens = p.MaxTokens
	}
	if p.Seed != nil && *p.Seed > 0 {
		req.Seed = p.Seed
	}
	return req
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
