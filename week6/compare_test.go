package week6

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeCloud struct{ last []Message }

type fakeRAGLocal struct{ fakeLocal }

func (f *fakeRAGLocal) Embed(ctx context.Context, model string, texts []string) ([][]float64, error) {
	return keywordEmbedder{}.Embed(ctx, model, texts)
}

func (f *fakeCloud) Chat(_ context.Context, _ string, messages []Message, _ Options) (ChatResult, error) {
	f.last = append([]Message(nil), messages...)
	return ChatResult{Text: "облачный ответ", PromptTokens: 17, OutputTokens: 3}, nil
}

func TestRAGCompareUsesIdenticalEvidenceAndKeepsLocalHistory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.txt"), []byte("Расходы возмещаются после согласования."), 0600); err != nil {
		t.Fatal(err)
	}
	rag := NewRAG(keywordEmbedder{}, "embeddinggemma", filepath.Join(t.TempDir(), "index.json"))
	if _, err := rag.Build(context.Background(), dir, nil); err != nil {
		t.Fatal(err)
	}
	local := &fakeLocal{}
	cloud := &fakeCloud{}
	agent := NewAgent(local, "local")
	a, b, hits, err := rag.Compare(context.Background(), agent, cloud, "cloud", "Какие расходы?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Text != "ответ" || b.Text != "облачный ответ" || a.Duration <= 0 || b.Duration <= 0 || len(hits) != 1 {
		t.Fatalf("неверное сравнение: %+v %+v %+v", a, b, hits)
	}
	if !reflect.DeepEqual(local.last, cloud.last) || !strings.Contains(local.last[0].Content, "Расходы возмещаются") {
		t.Fatalf("контекст различается: local=%+v cloud=%+v", local.last, cloud.last)
	}
	if len(agent.History) != 2 || agent.History[1].Content != "ответ" {
		t.Fatalf("неверная история: %+v", agent.History)
	}
	agent.Reset()
	if len(agent.History) != 0 {
		t.Fatal("история не очищена")
	}
	if _, err := rag.Load(); err != nil {
		t.Fatal("индекс удалён после сброса", err)
	}
}

func TestDeepSeekRequestAndSanitizedFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer placeholder" {
			t.Errorf("неверный запрос: %s", r.URL.Path)
		}
		var req struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
			Stream   bool      `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Model != "deepseek-flash" || req.Stream || len(req.Messages) != 1 {
			t.Errorf("неверные данные: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": Message{Role: "assistant", Content: "ответ"}}}, "usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 2}})
	}))
	defer server.Close()
	client := NewDeepSeek(server.URL, "placeholder")
	result, err := client.Chat(context.Background(), "deepseek-flash", []Message{{Role: "user", Content: "вопрос"}}, Options{NumPredict: 256})
	if err != nil || result.Text != "ответ" || result.PromptTokens != 5 || result.OutputTokens != 2 || result.Duration <= 0 {
		t.Fatalf("неверный ответ: %+v %v", result, err)
	}
	client.BaseURL = "bad-url"
	_, err = client.Chat(context.Background(), "deepseek-flash", nil, Options{})
	if err == nil || strings.Contains(err.Error(), "placeholder") || strings.Contains(err.Error(), "bad-url") {
		t.Fatalf("небезопасная ошибка: %v", err)
	}
}

func TestCloudRequestRejectsSensitiveContext(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewDeepSeek(server.URL, "placeholder")
	_, err := client.Chat(context.Background(), "deepseek-flash", []Message{{Role: "system", Content: "token=placeholder"}}, Options{})
	if err == nil || called || strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("чувствительный контекст отправлен или раскрыт: %v", err)
	}
}

func TestCompareCommandRequiresKey(t *testing.T) {
	var out bytes.Buffer
	if err := RunWith(context.Background(), 28, &fakeRAGLocal{}, "local", strings.NewReader("/compare вопрос\n/exit\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "DEEPSEEK_API_KEY") {
		t.Fatalf("неверный вывод: %s", out.String())
	}
}

func TestCompareCommandAndResetAliasesPreserveIndex(t *testing.T) {
	docs := t.TempDir()
	if err := os.WriteFile(filepath.Join(docs, "policy.txt"), []byte("Расходы возмещаются после согласования."), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_DIR", t.TempDir())
	input := "/index build " + docs + "\n/compare Какие расходы?\n/reset\n/clear\n/index status\n/compare Какие расходы?\n/exit\n"
	var out bytes.Buffer
	cloud := &fakeCloud{}
	if err := RunWithCloud(context.Background(), 28, &fakeRAGLocal{}, "local", cloud, "cloud", strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ЛОКАЛЬНАЯ") || !strings.Contains(out.String(), "ОБЛАЧНАЯ") || !strings.Contains(out.String(), "Индекс: 1 фрагментов") {
		t.Fatalf("неверный вывод: %s", out.String())
	}
	if len(cloud.last) != 2 {
		t.Fatalf("история после /reset и /clear не очищена: %+v", cloud.last)
	}
}
