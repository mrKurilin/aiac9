package main

import (
	"aiac9-week6"
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type recordingModel struct {
	mu    sync.Mutex
	calls [][]week6.Message
}

func (m *recordingModel) Models(context.Context) ([]week6.Model, error) { return nil, nil }

func (m *recordingModel) Chat(_ context.Context, _ string, messages []week6.Message, _ week6.Options) (week6.ChatResult, error) {
	m.mu.Lock()
	m.calls = append(m.calls, append([]week6.Message(nil), messages...))
	m.mu.Unlock()
	return week6.ChatResult{Text: "ответ"}, nil
}

func TestRemoteTerminalKeepsContextAndResets(t *testing.T) {
	model := &recordingModel{}
	service, err := week6.NewService(week6.NewAgent(model, ""), "127.0.0.1:0", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	client, err := newRemoteClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	in := strings.NewReader("первый\nвторой\n/reset\nтретий\n/exit\n")
	if err := week6.RunRemote(context.Background(), client, in, &out); err != nil {
		t.Fatal(err)
	}
	other, err := newRemoteClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Chat(context.Background(), "", []week6.Message{{Role: "user", Content: "другая сессия"}}, week6.Options{}); err != nil {
		t.Fatal(err)
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.calls) != 4 {
		t.Fatalf("вызовов модели: %d; вывод: %s", len(model.calls), out.String())
	}
	if len(model.calls[0]) != 2 || len(model.calls[1]) != 4 || len(model.calls[2]) != 2 {
		t.Fatalf("неверный контекст: %v", model.calls)
	}
	if model.calls[1][1].Content != "первый" || model.calls[1][2].Content != "ответ" || model.calls[1][3].Content != "второй" {
		t.Fatalf("второй запрос потерял контекст: %v", model.calls[1])
	}
	if model.calls[2][1].Content != "третий" || !strings.Contains(out.String(), "История очищена") {
		t.Fatalf("reset не очистил диалог: %v; вывод: %s", model.calls[2], out.String())
	}
	if len(model.calls[3]) != 2 || model.calls[3][1].Content != "другая сессия" {
		t.Fatalf("контекст попал в другую сессию: %v", model.calls[3])
	}
}

func TestRemoteClientAcceptsPublicPlainHTTP(t *testing.T) {
	if _, err := newRemoteClient("http://example.com:8780", "password-placeholder"); err != nil {
		t.Fatal(err)
	}
	if _, err := newRemoteClient("http://127.0.0.1:8780", ""); err != nil {
		t.Fatal(err)
	}
}
