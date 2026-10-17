package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func privateMessage(id int64, text string) *telegramMessage {
	m := &telegramMessage{Text: text}
	m.Chat.ID = id
	m.Chat.Type = "private"
	m.From.ID = id
	return m
}
func TestBotCancellationIsolationAccessAndLimits(t *testing.T) {
	var mu sync.Mutex
	var replies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		replies = append(replies, body.Text)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer server.Close()
	gate := make(chan struct{})
	p := &scriptedPlanner{block: gate}
	b := &bot{tg: &telegramClient{base: server.URL, http: server.Client()}, template: &agent{client: p, travel: &travelClient{cities: cities()}, now: testNow}, allowed: map[int64]bool{1: true, 2: true}, sessions: map[int64]*chatSession{}, slots: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.handle(ctx, privateMessage(3, "Билет")) // unauthorized must not reach AI
	b.handle(ctx, privateMessage(1, "Из Казани"))
	b.handle(ctx, privateMessage(2, "Из Москвы"))
	deadline := time.Now().Add(2 * time.Second)
	for {
		p.mu.Lock()
		n := len(p.seen)
		p.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("requests did not start")
		}
		time.Sleep(time.Millisecond)
	}
	b.handle(ctx, privateMessage(1, "/reset"))
	close(gate)
	b.workers.Wait()
	b.mu.Lock()
	if len(b.sessions[1].history) != 0 || len(b.sessions[2].history) != 2 {
		t.Fatal("reset or chat isolation failed")
	}
	b.mu.Unlock()
	b.handle(ctx, privateMessage(2, "ещё")) // rate limited
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) != 2 {
		t.Fatal("access/rate limit failed")
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(replies, "\n")
	if !strings.Contains(joined, "Доступ ограничен") || !strings.Contains(joined, "Лимит запросов") {
		t.Fatal("missing access/limit explanation")
	}
}
func TestTelegramUnicodeChunkingAndSafeErrors(t *testing.T) {
	var parts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/bad") {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"SENSITIVE_RESPONSE_MUST_NOT_ESCAPE"}`)
			return
		}
		var body struct {
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ParseMode != "" || len([]rune(body.Text)) > 1800 {
			t.Error("unsafe formatting or length")
		}
		parts = append(parts, body.Text)
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer server.Close()
	tg := &telegramClient{base: server.URL, http: server.Client()}
	text := strings.Repeat("🛫Привет", 1000)
	if err := tg.send(context.Background(), 1, text); err != nil {
		t.Fatal(err)
	}
	if strings.Join(parts, "") != text {
		t.Fatal("unicode damaged")
	}
	err := tg.call(context.Background(), "bad", nil, nil)
	if err == nil || strings.Contains(err.Error(), "SENSITIVE") {
		t.Fatal("error body exposed")
	}
}
func TestTelegram429RetryIsCancellable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":30}}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	tg := &telegramClient{base: server.URL, http: server.Client()}
	started := time.Now()
	if err := tg.call(ctx, "sendMessage", nil, nil); err == nil {
		t.Fatal("expected cancellation")
	}
	if time.Since(started) > time.Second {
		t.Fatal("retry ignored cancellation")
	}
}
func TestPollingStartupOffsetAndShutdownWithRealFakeHTTPFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var offsets []int64
	var sent []string
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getMe":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":7,"username":"sample_bot"}}`)
		case "/getWebhookInfo":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"url":""}}`)
		case "/setMyCommands":
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		case "/aviasales/v3/prices_for_dates":
			_, _ = io.WriteString(w, `{"success":true}`)
		case "/getUpdates":
			var body struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			offsets = append(offsets, body.Offset)
			polls++
			n := polls
			mu.Unlock()
			if n == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []update{{ID: 41, Message: privateMessage(1, "В Сочи")}, {ID: 41, Message: privateMessage(1, "В Сочи")}}})
			} else {
				<-r.Context().Done()
			}
		case "/sendMessage":
			var body struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			sent = append(sent, body.Text)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":7}}`)
			if body.Text == "Укажите даты" {
				cancel()
			}
		default:
			t.Error("unexpected endpoint", r.URL.Path)
		}
	}))
	defer server.Close()
	p := &scriptedPlanner{}
	a := &agent{client: p, travel: &travelClient{key: "placeholder", base: server.URL, http: server.Client(), cities: cities()}, now: testNow}
	dir := t.TempDir()
	err := serveTelegram(ctx, a, &telegramClient{base: server.URL, http: server.Client()}, nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "offset"))
	if err != nil || string(data) != "42" {
		t.Fatalf("offset %q %v", data, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) != 1 {
		t.Fatal("duplicate operation", len(p.seen))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(offsets) == 0 || offsets[0] != 0 {
		t.Fatal("bad offset")
	}
}
func TestWebhookPreventsStartupAndDoesNotDeleteIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/getMe" {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":7}}`)
		} else if r.URL.Path == "/getWebhookInfo" {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"url":"https://example.com/hook"}}`)
		} else {
			t.Error("unexpected mutation")
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	err := serveTelegram(context.Background(), &agent{}, &telegramClient{base: server.URL, http: server.Client()}, nil, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "webhook") || strings.Contains(err.Error(), "example.com") {
		t.Fatal("bad webhook handling")
	}
}

func TestTerminalAndTelegramReuseIdenticalSearchLogic(t *testing.T) {
	var mu sync.Mutex
	var telegramAnswers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/aviasales/v3/prices_for_dates" {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": []offer{ticket()}})
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		telegramAnswers = append(telegramAnswers, body.Text)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer server.Close()
	newAgent := func() *agent {
		return &agent{client: &scriptedPlanner{plans: []searchPlan{plan()}}, travel: &travelClient{key: "placeholder", base: server.URL, http: server.Client(), cities: cities()}, now: testNow}
	}
	input := "Из Казани в Сочи в ноябре 2026, туда-обратно на 5–7 дней, до 20000 рублей, прямой"
	a := newAgent()
	expected, err := a.answer(context.Background(), input, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	var terminalOutput strings.Builder
	if err := run(context.Background(), newAgent(), strings.NewReader(input+"\n/exit\n"), &terminalOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminalOutput.String(), "15000 ₽") || !strings.Contains(terminalOutput.String(), "Казань → Сочи") {
		t.Fatal("terminal search result missing")
	}
	b := &bot{tg: &telegramClient{base: server.URL, http: server.Client()}, template: newAgent(), sessions: map[int64]*chatSession{}, slots: make(chan struct{}, 4)}
	b.handle(context.Background(), privateMessage(1, input))
	b.workers.Wait()
	mu.Lock()
	actual := strings.Join(telegramAnswers[1:], "")
	mu.Unlock()
	if actual != expected {
		t.Fatalf("transports changed shared search output: %q != %q", actual, expected)
	}
}
