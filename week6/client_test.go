package week6

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaLocalAPIAndThreeChecks(t *testing.T) {
	var prompts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "qwen3:4b-instruct", "size": 2500000000}}})
		case "/api/chat":
			var request struct {
				Model    string    `json:"model"`
				Messages []Message `json:"messages"`
				Stream   bool      `json:"stream"`
				Options  Options   `json:"options"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Model != "qwen3:4b-instruct" || request.Stream || request.Options.NumCtx != 4096 {
				t.Errorf("неверный запрос: %+v", request)
			}
			prompts = append(prompts, request.Messages[len(request.Messages)-1].Content)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": Message{Role: "assistant", Content: "Ответ"}, "prompt_eval_count": 10, "eval_count": 2})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a := NewAgent(NewOllama(server.URL), "")
	if _, err := a.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	text, err := a.Check(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 3 || text == "" {
		t.Fatalf("проверки: %d, вывод: %q", len(prompts), text)
	}
}

func TestAgentKeepsHistoryAndResetClearsIt(t *testing.T) {
	client := &fakeLocal{}
	a := NewAgent(client, "qwen3:4b-instruct")
	if _, err := a.Answer(context.Background(), "первый", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Answer(context.Background(), "второй", nil); err != nil {
		t.Fatal(err)
	}
	if len(client.last) != 3 {
		t.Fatalf("история не передана: %d", len(client.last))
	}
	a.Reset()
	if len(a.History) != 0 {
		t.Fatal("история не очищена")
	}
}

type fakeLocal struct{ last []Message }

func (f *fakeLocal) Models(context.Context) ([]Model, error) { return nil, nil }
func (f *fakeLocal) Chat(_ context.Context, _ string, messages []Message, _ Options) (ChatResult, error) {
	f.last = append([]Message(nil), messages...)
	return ChatResult{Text: "ответ"}, nil
}
