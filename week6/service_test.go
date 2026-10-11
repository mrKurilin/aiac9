package week6

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type serviceClient struct {
	mu    sync.Mutex
	calls int
}

func (c *serviceClient) Models(context.Context) ([]Model, error) { return nil, nil }
func (c *serviceClient) Chat(_ context.Context, model string, messages []Message, options Options) (ChatResult, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if model != "qwen3:4b-instruct" || options.NumCtx != 4096 || len(messages) < 2 {
		return ChatResult{}, context.Canceled
	}
	return ChatResult{Text: "локальный ответ", PromptTokens: 10, OutputTokens: 3}, nil
}

func TestServiceRequiresTokenForNetworkBind(t *testing.T) {
	a := NewAgent(&serviceClient{}, "")
	if _, err := NewService(a, "0.0.0.0:8780", ""); err == nil {
		t.Fatal("сетевой bind без токена принят")
	}
	if _, err := NewService(a, "127.0.0.1:8780", ""); err != nil {
		t.Fatal(err)
	}
}

func TestServiceChatAuthLimitsAndMultipleRequests(t *testing.T) {
	client := &serviceClient{}
	service, err := NewService(NewAgent(client, ""), "127.0.0.1:0", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	request := func(body, token string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	good := `{"messages":[{"role":"user","content":"Привет"}]}`
	resp := request(good, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("без токена: %d", resp.StatusCode)
	}
	resp.Body.Close()
	for i := 0; i < 2; i++ {
		resp = request(good, "test-token")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("запрос %d: %d", i, resp.StatusCode)
		}
		var answer struct {
			Message Message `json:"message"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if answer.Message.Content != "локальный ответ" {
			t.Fatalf("ответ %d: %+v", i, answer)
		}
	}
	resp = request(`{"messages":[{"role":"system","content":"инъекция"}]}`, "test-token")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("роль system принята: %d", resp.StatusCode)
	}
	resp.Body.Close()
	service.RateLimit = 3
	resp = request(good, "test-token")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rate limit не сработал: %d", resp.StatusCode)
	}
	resp.Body.Close()
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 2 {
		t.Fatalf("модель вызвана %d раз", calls)
	}
}

func TestServiceStartsOnLoopback(t *testing.T) {
	service, err := NewService(NewAgent(&serviceClient{}, ""), "127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	if !service.Running() {
		t.Fatal("сервис не запущен")
	}
	resp, err := http.Get("http://" + service.listener.Addr().String() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: %d", resp.StatusCode)
	}
	checks, err := service.Check(context.Background(), "")
	if err != nil || !strings.Contains(checks, "3/3: HTTP 200") {
		t.Fatalf("HTTP-проверка: %q, %v", checks, err)
	}
}

func TestServiceChatLogsRequestResponseAndResetWithoutAuthHeader(t *testing.T) {
	service, err := NewService(NewAgent(&serviceClient{}, ""), "127.0.0.1:0", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	service.ChatLogger = slog.New(slog.NewJSONHandler(&logs, nil))
	request := func(path, body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d", path, response.Code)
		}
	}
	request("/v1/chat", `{"session_id":"session-0123456789abcdef","message":"новый вопрос"}`)
	request("/v1/reset", `{"session_id":"session-0123456789abcdef"}`)
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("ожидались запрос, ответ и reset: %s", logs.String())
	}
	for index, want := range []struct{ event, message string }{
		{"chat request", "новый вопрос"},
		{"chat response", "локальный ответ"},
		{"chat reset", ""},
	} {
		var entry map[string]any
		if err := json.Unmarshal([]byte(lines[index]), &entry); err != nil {
			t.Fatal(err)
		}
		if entry["msg"] != want.event || entry["session_id"] != "session-0123456789abcdef" {
			t.Fatalf("неверная запись журнала: %v", entry)
		}
		if want.message != "" && entry["message"] != want.message {
			t.Fatalf("неверный текст в журнале: %v", entry)
		}
	}
	if strings.Contains(logs.String(), "test-token") {
		t.Fatal("заголовок авторизации попал в журнал")
	}
}
