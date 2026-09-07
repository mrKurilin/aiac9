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

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestAgentKeepsHistory(t *testing.T) {
	var calls [][]Message
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("unexpected Authorization: %q", got)
		}
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, request.Messages)
		return jsonResponse(http.StatusOK, `{"choices":[{"message":{"role":"assistant","content":"answer"}}]}`), nil
	})}

	client := &DeepSeekClient{APIKey: "test-token", BaseURL: "https://example.test/chat", Model: "deepseek-chat", HTTP: httpClient}
	agent := NewAgent(client, "system")
	if _, err := agent.Ask(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Ask(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 || len(calls[1]) != 4 {
		t.Fatalf("history was not sent: %#v", calls)
	}
	if calls[1][1].Content != "first" || calls[1][2].Content != "answer" || calls[1][3].Content != "second" {
		t.Fatalf("unexpected messages: %#v", calls[1])
	}
}

func TestAgentDoesNotRememberFailedRequest(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"error":{"message":"bad token"}}`), nil
	})}

	agent := NewAgent(&DeepSeekClient{APIKey: "bad", BaseURL: "https://example.test/chat", Model: "deepseek-chat", HTTP: httpClient}, "")
	if _, err := agent.Ask(context.Background(), "hello"); err == nil {
		t.Fatal("expected API error")
	}
	if len(agent.history) != 0 {
		t.Fatalf("failed request was saved: %#v", agent.history)
	}
}
