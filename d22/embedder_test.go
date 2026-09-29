package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaEmbedderUsesAPIKeyAndBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/embed" {
			t.Fatalf("неверный запрос: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			t.Fatal("не задан Content-Type")
		}
		_, _ = w.Write([]byte(`{"embeddings":[[0.6,0.8],[1,0]]}`))
	}))
	defer server.Close()

	embedder := NewOllamaEmbedder(server.URL+"/api/embed", "embeddinggemma", "test-key", server.Client())
	vectors, err := embedder.Embed(context.Background(), []string{"один", "два"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || len(vectors[0]) != 2 || embedder.ID() != "ollama:embeddinggemma" {
		t.Fatalf("неверный результат: %#v, %s", vectors, embedder.ID())
	}
}

func TestOllamaEmbedderDoesNotSendEmptyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("неожиданный Authorization: %q", got)
		}
		_, _ = w.Write([]byte(`{"embeddings":[[1]]}`))
	}))
	defer server.Close()
	_, err := NewOllamaEmbedder(server.URL, "embeddinggemma", "", server.Client()).Embed(context.Background(), []string{"текст"})
	if err != nil {
		t.Fatal(err)
	}
}
