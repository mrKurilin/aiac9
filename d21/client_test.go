package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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
	parts := []string{`{"answer":"При`, `вет\nмир"}`}
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

func TestDeepSeekClientStreamsAnswerWithToolsBeforeCompletion(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	release := make(chan struct{})
	go func() {
		defer writer.Close()
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"answer\\\":\\\"При\"}}]}\n\n")
		<-release
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"вет\\\"}\"}}]}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}()
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Stream bool             `json:"stream"`
			Tools  []ToolDefinition `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !body.Stream || len(body.Tools) != 1 || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("request: stream=%v tools=%d accept=%q", body.Stream, len(body.Tools), r.Header.Get("Accept"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: reader}, nil
	})}
	client := NewDeepSeekClient("test-key", "https://example.test/chat", "test-model", httpClient)
	chunks := make(chan string, 2)
	finished := make(chan struct {
		message Message
		err     error
	}, 1)
	go func() {
		message, err := client.CompleteWithToolsStream(context.Background(), []Message{{Role: "user", Content: "вопрос"}}, toolDefinitions([]MCPTool{{Name: "lookup"}}), func(chunk string) error {
			chunks <- chunk
			return nil
		})
		finished <- struct {
			message Message
			err     error
		}{message, err}
	}()
	select {
	case chunk := <-chunks:
		if chunk != `{"answer":"При` {
			t.Fatalf("first chunk=%q", chunk)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first chunk arrived only after completion")
	}
	close(release)
	result := <-finished
	if result.err != nil || result.message.Content != `{"answer":"Привет"}` {
		t.Fatalf("message=%+v err=%v", result.message, result.err)
	}
}

func TestDeepSeekClientAssemblesStreamedToolCall(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"id\\\":\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"42}\"}}]}}]}\n\n" +
		"data: [DONE]\n\n"
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}
	client := NewDeepSeekClient("test-key", "https://example.test/chat", "test-model", httpClient)
	message, err := client.CompleteWithToolsStream(context.Background(), []Message{{Role: "user", Content: "вопрос"}}, toolDefinitions([]MCPTool{{Name: "lookup"}}), nil)
	if err != nil || len(message.ToolCalls) != 1 || message.ToolCalls[0].ID != "call-1" || message.ToolCalls[0].Function.Name != "lookup" || message.ToolCalls[0].Function.Arguments != `{"id":42}` {
		t.Fatalf("message=%+v err=%v", message, err)
	}
}
