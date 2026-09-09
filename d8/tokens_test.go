package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

func TestMaxTokensOmittedWhenNotConfigured(t *testing.T) {
	body, err := json.Marshal(chatRequest{Model: "deepseek-v4-flash", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "max_tokens") {
		t.Fatalf("unexpected response limit: %s", body)
	}
	body, err = json.Marshal(chatRequest{Model: "deepseek-v4-flash", MaxTokens: 32})
	if err != nil || !strings.Contains(string(body), `"max_tokens":32`) {
		t.Fatalf("manual response limit missing: %s, %v", body, err)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testClient(t *testing.T, stream bool, finish string, usage bool) *DeepSeekClient {
	t.Helper()
	return &DeepSeekClient{BaseURL: "https://example.com/chat", MaxTokens: 32, HTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.MaxTokens != 32 || request.Stream != stream {
			t.Fatalf("request: %+v", request)
		}
		body := fmt.Sprintf(`{"choices":[{"message":{"content":"Ответ"},"finish_reason":%q}]`, finish)
		u := `"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":40}`
		if usage {
			body += "," + u
		}
		body += "}"
		if stream {
			if !request.StreamOptions["include_usage"] {
				t.Fatal("missing include_usage")
			}
			body = fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":\"Ответ\"},\"finish_reason\":%q}]}\n\n", finish)
			if usage {
				body += "data: {\"choices\":[]," + u + "}\n\n"
			}
			body += "data: [DONE]\n\n"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
}
func TestAPIUsageAndCost(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			client := testClient(t, stream, "stop", true)
			var out bytes.Buffer
			meter := &Meter{Client: client, Out: &out, InputPrice: 1, CachedPrice: .1, OutputPrice: 2}
			a := NewAgent(meter, "system", nil, "test")
			var emit func(string) error
			if stream {
				emit = func(s string) error { return nil }
			}
			if _, err := a.AskStream(context.Background(), "Hello", emit); err != nil {
				t.Fatal(err)
			}
			if meter.Input != 100 || meter.Output != 20 || math.Abs(meter.Cost-.000104) > 1e-12 {
				t.Fatalf("meter: %+v", meter)
			}
			if len(a.History()) != 2 {
				t.Fatal("history not saved")
			}
		})
	}
}
func TestOverflowPreservesHistoryAndSkipsAPI(t *testing.T) {
	f := &terminalFake{}
	m := &Meter{Client: f}
	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := NewAgent(m, "system", store, "test")
	if _, err := a.Ask(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	m.ContextLimit = 10
	m.Reserve = 5
	if _, err := a.Ask(context.Background(), strings.Repeat("Ж", 100)); err == nil {
		t.Fatal("expected overflow")
	}
	restored := NewAgent(f, "system", store, "test")
	if len(f.calls) != 1 || len(restored.History()) != 2 || m.Turns != 1 {
		t.Fatal("overflow mutated history or called API")
	}
}
func TestLengthCountsButDoesNotSave(t *testing.T) {
	for _, stream := range []bool{false, true} {
		c := testClient(t, stream, "length", true)
		m := &Meter{Client: c}
		a := NewAgent(m, "", nil, "test")
		var emit func(string) error
		if stream {
			emit = func(string) error { return nil }
		}
		if _, err := a.AskStream(context.Background(), "hi", emit); err == nil {
			t.Fatal("expected truncation")
		}
		if m.Output != 20 || len(a.History()) != 0 {
			t.Fatal("truncated answer handling")
		}
	}
}
func TestMissingUsageIsEstimate(t *testing.T) {
	c := testClient(t, true, "stop", false)
	c.LastUsage = &Usage{Prompt: 9999}
	var out bytes.Buffer
	m := &Meter{Client: c, Out: &out}
	if _, err := m.CompleteStream(context.Background(), []Message{{Role: "user", Content: "Привет"}}, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if m.Input == 9999 || !strings.Contains(out.String(), "Токены (оценка)") {
		t.Fatal(out.String())
	}
}
func TestBrokenStream(t *testing.T) {
	c := &DeepSeekClient{BaseURL: "https://example.com", HTTP: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))}, nil
	})}}
	m := &Meter{Client: c}
	a := NewAgent(m, "", nil, "test")
	if _, err := a.AskStream(context.Background(), "hi", func(string) error { return nil }); err == nil {
		t.Fatal("expected interrupted stream")
	}
	if len(a.History()) != 0 || m.Unknown != 1 {
		t.Fatal("failure handling")
	}
}
func TestDemo(t *testing.T) {
	var out bytes.Buffer
	if err := runDemo(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Короткий диалог", "Длинный диалог", "Переполнение", "запрос не отправлен"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("missing", want)
		}
	}
}
