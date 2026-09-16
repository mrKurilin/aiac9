package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestDeepSeekClientUsesCompatibleChatAPI(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("request=%s auth=%q", r.Method, r.Header.Get("Authorization"))
		}
		var body struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "test-model" || len(body.Messages) != 1 {
			t.Errorf("body=%+v", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"ответ"}}]}`)),
		}, nil
	})}
	client := NewDeepSeekClient("test-key", "https://example.test/chat", "test-model", httpClient)
	answer, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "вопрос"}})
	if err != nil || answer != "ответ" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
}

func TestDeepSeekClientStreamsContentDeltas(t *testing.T) {
	parts := []string{`{"answer":"При`, `вет\nмир","next_stage":""}`}
	events := make([]string, 0, len(parts)+1)
	for _, part := range parts {
		payload, err := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]string{"content": part}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, "data: "+string(payload)+"\n\n")
	}
	events = append(events, "data: [DONE]\n\n")

	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !body.Stream || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("stream=%v accept=%q", body.Stream, r.Header.Get("Accept"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(strings.Join(events, ""))),
		}, nil
	})}
	client := NewDeepSeekClient("test-key", "https://example.test/chat", "test-model", httpClient)
	var streamed strings.Builder
	answer, err := client.CompleteStream(context.Background(), []Message{{Role: "user", Content: "вопрос"}}, func(chunk string) error {
		streamed.WriteString(chunk)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(parts, "")
	if answer != want || streamed.String() != want {
		t.Fatalf("answer=%q streamed=%q want=%q", answer, streamed.String(), want)
	}
}
