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
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello from deepseek"}}]}`)
	})

	srv := testServer(t, backend)

	resp, err := http.Post(srv.URL+"/api/complete", "application/json",
		strings.NewReader(`{"prompt":"hi","params":{"temperature":0.7,"top_p":1,"top_k":0,"max_tokens":512,"seed":0,"presence_penalty":0,"frequency_penalty":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)

	if result["content"] != "hello from deepseek" {
		t.Fatalf("unexpected content: %q", result["content"])
	}

	messages := got["messages"].([]any)
	first := messages[0].(map[string]any)
	if first["content"] != "hi" {
		t.Errorf("unexpected prompt sent to backend: %v", first["content"])
	}

	if _, ok := got["top_k"]; ok {
		t.Error("top_k=0 must not be sent to the API")
	}
	if _, ok := got["seed"]; ok {
		t.Error("seed=0 must not be sent to the API")
	}
	if got["temperature"] != 0.7 {
		t.Errorf("unexpected temperature: %v", got["temperature"])
	}
}

func TestCompleteSendsTopKAndSeedWhenSet(t *testing.T) {
	var got map[string]any

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	})

	srv := testServer(t, backend)

	resp, err := http.Post(srv.URL+"/api/complete", "application/json",
		strings.NewReader(`{"prompt":"hi","params":{"top_k":50,"seed":42}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if got["top_k"] != float64(50) {
		t.Errorf("expected top_k 50 in request, got %v", got["top_k"])
	}
	if got["seed"] != float64(42) {
		t.Errorf("expected seed 42 in request, got %v", got["seed"])
	}
}

func TestBuildCompletionRequestSkipsInvalidParams(t *testing.T) {
	zero := 0.0
	zeroInt := 0
	positive := 0.01
	two := 2.0

	req := buildCompletionRequest("hi", params{
		Temperature: &zero,
		TopP:        &zero,
		TopK:        &zeroInt,
		MaxTokens:   &zeroInt,
		Seed:        &zeroInt,
	})

	if req.Temperature == nil || *req.Temperature != 0 {
		t.Error("temperature 0 must be kept")
	}
	if req.TopP != nil {
		t.Error("top_p 0 must be omitted")
	}
	if req.TopK != nil {
		t.Error("top_k 0 must be omitted")
	}
	if req.MaxTokens != nil {
		t.Error("max_tokens 0 must be omitted")
	}
	if req.Seed != nil {
		t.Error("seed 0 must be omitted")
	}

	req = buildCompletionRequest("hi", params{Temperature: &two, TopP: &positive})

	if req.Temperature == nil || *req.Temperature != 2 {
		t.Error("temperature 2 must be kept")
	}
	if req.TopP == nil || *req.TopP != 0.01 {
		t.Error("top_p 0.01 must be kept")
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
		strings.NewReader(`{"prompt":"hi","params":{}}`))
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
