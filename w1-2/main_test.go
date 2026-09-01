package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T, backend http.Handler) *httptest.Server {
	t.Helper()

	backendServer := httptest.NewServer(backend)

	a := newApp("test-key")
	a.baseURL = backendServer.URL
	a.client = backendServer.Client()

	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)
	t.Cleanup(backendServer.Close)

	return srv
}

func TestIndexServesPage(t *testing.T) {
	srv := testServer(t, http.NotFoundHandler())

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "LLM Playground") {
		t.Fatalf("page does not contain title, got %d bytes", len(body))
	}
}

func TestCompleteReturnsAnswer(t *testing.T) {
	var got map[string]any

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)

		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("unexpected Authorization header: %q", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello from deepseek"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	})

	srv := testServer(t, backend)

	resp, err := http.Post(srv.URL+"/api/complete", "application/json",
		strings.NewReader(`{"prompt":"hi","config":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	if result["content"] != "hello from deepseek" {
		t.Fatalf("unexpected content: %q", result["content"])
	}
	if result["finish_reason"] != "stop" {
		t.Fatalf("unexpected finish_reason: %v", result["finish_reason"])
	}
	if result["completion_tokens"] != float64(2) {
		t.Fatalf("unexpected completion_tokens: %v", result["completion_tokens"])
	}

	messages := got["messages"].([]any)
	first := messages[0].(map[string]any)
	if first["content"] != "hi" {
		t.Errorf("unexpected prompt sent to backend: %v", first["content"])
	}
	if _, ok := got["max_tokens"]; ok {
		t.Error("max_tokens must not be sent when not set")
	}
	if _, ok := got["stop"]; ok {
		t.Error("stop must not be sent when not set")
	}
	if _, ok := got["response_format"]; ok {
		t.Error("response_format must not be sent when not set")
	}
}

func TestCompleteAppliesFormatToPrompt(t *testing.T) {
	var got map[string]any

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	})

	srv := testServer(t, backend)

	resp, err := http.Post(srv.URL+"/api/complete", "application/json",
		strings.NewReader(`{"prompt":"hi","config":{"format":"Формат: строго JSON"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	messages := got["messages"].([]any)
	first := messages[0].(map[string]any)
	content, _ := first["content"].(string)

	if !strings.Contains(content, "hi") || !strings.Contains(content, "Формат: строго JSON") {
		t.Errorf("format description must be appended to the prompt, got: %q", content)
	}
}

func TestCompleteSetsMaxTokens(t *testing.T) {
	var got map[string]any

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	})

	srv := testServer(t, backend)

	resp, err := http.Post(srv.URL+"/api/complete", "application/json",
		strings.NewReader(`{"prompt":"hi","config":{"max_tokens":60}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if got["max_tokens"] != float64(60) {
		t.Errorf("expected max_tokens 60 in request, got %v", got["max_tokens"])
	}
}

func TestCompleteSetsStop(t *testing.T) {
	var got map[string]any

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	})

	srv := testServer(t, backend)

	resp, err := http.Post(srv.URL+"/api/complete", "application/json",
		strings.NewReader(`{"prompt":"hi","config":{"stop":"."}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	stop, ok := got["stop"].([]any)
	if !ok || len(stop) != 1 || stop[0] != "." {
		t.Errorf("expected stop [\".\"] in request, got %v", got["stop"])
	}
}

func TestBuildCompletionRequestSkipsInvalidConfig(t *testing.T) {
	zero := 0

	req := buildCompletionRequest("hi", requestConfig{MaxTokens: &zero}, nil)

	if _, ok := req["max_tokens"]; ok {
		t.Error("max_tokens 0 must be omitted")
	}

	req = buildCompletionRequest("hi", requestConfig{Stop: " "}, nil)

	if _, ok := req["stop"]; ok {
		t.Error("blank stop must be omitted")
	}

	req = buildCompletionRequest("hi", requestConfig{Format: "Формат: JSON"}, nil)

	messages := req["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected a single message, got %v", messages)
	}
	content, _ := messages[0].(map[string]any)["content"].(string)
	if !strings.Contains(content, "Формат: JSON") {
		t.Errorf("format must be appended to the prompt, got %q", content)
	}
}

func TestBuildCompletionRequestMergesManualParams(t *testing.T) {
	maxTokens := 60

	req := buildCompletionRequest("hi", requestConfig{MaxTokens: &maxTokens}, map[string]any{"max_tokens": 30, "temperature": 0.2})

	if req["max_tokens"] != 30 {
		t.Errorf("manual max_tokens must override config, got %v", req["max_tokens"])
	}
	if req["temperature"] != 0.2 {
		t.Errorf("manual temperature must be present in the request, got %v", req["temperature"])
	}
	if _, ok := req["model"]; !ok {
		t.Error("model must remain in the request")
	}

	req = buildCompletionRequest("hi", requestConfig{}, map[string]any{"response_format": map[string]any{"type": "json_object"}})

	rf, ok := req["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_object" {
		t.Error("manual params must be able to set response_format")
	}
}

func TestCompleteForwardsManualParams(t *testing.T) {
	var got map[string]any

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	})

	srv := testServer(t, backend)

	resp, err := http.Post(srv.URL+"/api/complete", "application/json",
		strings.NewReader(`{"prompt":"hi","config":{"max_tokens":60},"params":{"max_tokens":30,"temperature":0.2}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if got["max_tokens"] != float64(30) {
		t.Errorf("manual max_tokens must override config, got %v", got["max_tokens"])
	}
	if got["temperature"] != 0.2 {
		t.Errorf("manual temperature must be in the request, got %v", got["temperature"])
	}
}

func TestCompleteSurfacesAPIError(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Incorrect API key"}}`)
	})

	srv := testServer(t, backend)

	resp, err := http.Post(srv.URL+"/api/complete", "application/json",
		strings.NewReader(`{"prompt":"hi","config":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)

	if !strings.Contains(result["error"], "Incorrect API key") {
		t.Fatalf("unexpected error message: %q", result["error"])
	}
}
