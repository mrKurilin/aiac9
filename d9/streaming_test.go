package main

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestDeepSeekDeliversChunkBeforeStreamFinishes(t *testing.T) {
	reader, writer := io.Pipe()
	client := &DeepSeekClient{
		BaseURL: "https://example.com/chat",
		Model:   "deepseek-v4-flash",
		HTTP: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: reader, Header: make(http.Header)}, nil
		})},
	}
	emitted := make(chan string, 2)
	finished := make(chan struct {
		answer string
		err    error
	}, 1)
	go func() {
		answer, err := client.CompleteStream(context.Background(), []Message{{Role: "user", Content: "hello"}}, func(chunk string) error {
			emitted <- chunk
			return nil
		})
		finished <- struct {
			answer string
			err    error
		}{answer, err}
	}()

	if _, err := io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"Первая \"}}]}\n\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case chunk := <-emitted:
		if chunk != "Первая " {
			t.Fatalf("first chunk: %q", chunk)
		}
	case <-time.After(time.Second):
		t.Fatal("first SSE chunk was buffered instead of emitted")
	}
	select {
	case result := <-finished:
		t.Fatalf("stream finished before trailing chunk: %+v", result)
	default:
	}

	if _, err := io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"часть\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"); err != nil {
		t.Fatal(err)
	}
	result := <-finished
	if result.err != nil || result.answer != "Первая часть" {
		t.Fatalf("answer=%q err=%v", result.answer, result.err)
	}
}
