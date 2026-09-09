package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOverflowReachesAPIWithoutTruncation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			calls := 0
			large := strings.Repeat("Контекст ", 200)
			client := &DeepSeekClient{BaseURL: "https://example.com/chat", HTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var request chatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if len(request.Messages) != 3 || request.Messages[1].Content != large || request.Messages[2].Content != "повтор" {
					t.Fatal("context was modified")
				}
				return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"maximum context length exceeded"}}`))}, nil
			})}}
			meter := &Meter{Client: client, ContextLimit: 100, Reserve: 32, AllowOverflow: true}
			a := NewAgent(meter, "system", nil, "overflow")
			a.history = []Message{{Role: "user", Content: large}}
			var emit func(string) error
			if stream {
				emit = func(string) error { return nil }
			}
			_, err := a.AskStream(context.Background(), "повтор", emit)
			if err == nil || !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "maximum context length exceeded") {
				t.Fatalf("expected provider error, got %v", err)
			}
			if calls != 1 || len(a.History()) != 1 || a.History()[0].Content != large {
				t.Fatal("failed call changed history")
			}
		})
	}
}
func TestOverflowSwitch(t *testing.T) {
	fake := &terminalFake{}
	meter := &Meter{Client: fake, ContextLimit: 1, Reserve: 32}
	a := NewAgent(meter, "system", nil, "switch")
	var out bytes.Buffer
	for _, command := range []string{"/overflow on", "/overflow off", "/overflow on"} {
		if !contextCommand(a, command, &out) {
			t.Fatal("unhandled")
		}
		_, err := a.Ask(context.Background(), "hello")
		if strings.HasSuffix(command, "off") && err == nil {
			t.Fatal("not blocked")
		}
		if strings.HasSuffix(command, "on") && err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.calls) != 2 {
		t.Fatal("wrong API calls")
	}
}
