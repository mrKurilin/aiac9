package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type captureClient struct {
	calls [][]Message
	reply string
	err   error
}

type scriptedExtractor struct {
	updates []MemoryUpdate
	err     error
	calls   int
}

func (e *scriptedExtractor) Extract(context.Context, MemoryLayers, []Message, string, string) (MemoryUpdate, error) {
	e.calls++
	if e.err != nil {
		return MemoryUpdate{}, e.err
	}
	if len(e.updates) == 0 {
		return MemoryUpdate{}, nil
	}
	update := e.updates[0]
	e.updates = e.updates[1:]
	return update, nil
}

func (c *captureClient) Complete(_ context.Context, messages []Message) (string, error) {
	c.calls = append(c.calls, append([]Message(nil), messages...))
	if c.reply == "" {
		return "Ответ", c.err
	}
	return c.reply, c.err
}

func TestMemoryLayersAreSeparateAndInfluenceContext(t *testing.T) {
	store, err := NewJSONLayerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := &captureClient{}
	agent := NewAgent(client, "system", store, "test")
	for _, entry := range []struct{ layer, key, value string }{
		{"short", "temporary", "черновик"},
		{"working", "deadline", "пятница"},
		{"long", "profile.language", "русский"},
	} {
		if err := agent.Remember(entry.layer, entry.key, entry.value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := agent.Ask(context.Background(), "Каков план?", nil); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("calls=%d", len(client.calls))
	}
	joined := ""
	for _, message := range client.calls[0] {
		joined += "\n" + message.Role + ":" + message.Content
	}
	for _, want := range []string{"черновик", "пятница", "русский", "Каков план?"} {
		if !strings.Contains(joined, want) {
			t.Errorf("context misses %q:\n%s", want, joined)
		}
	}
	if client.calls[0][1].Role != "system" || !strings.Contains(client.calls[0][1].Content, "Долговременная") {
		t.Fatalf("layers not labeled explicitly: %+v", client.calls[0])
	}
	if err := agent.Clear("short"); err != nil {
		t.Fatal(err)
	}
	snapshot := agent.Snapshot()
	if len(snapshot.ShortTerm) != 0 || len(agent.Transcript()) != 0 {
		t.Fatal("short memory was not cleared")
	}
	if snapshot.Working["deadline"] != "пятница" || snapshot.LongTerm["profile.language"] != "русский" {
		t.Fatal("clearing short memory changed another layer")
	}
}

func TestEachLayerHasItsOwnPersistentFile(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLayerStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(&captureClient{}, "", store, "separate")
	if err := agent.Remember("short", "topic", "memory"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Remember("working", "task", "memory UI"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Remember("long", "decision", "Go"); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(dir, "separate", "short-term.json"),
		filepath.Join(dir, "separate", "working.json"),
		filepath.Join(dir, "separate", "long-term.json"),
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode=%o", path, info.Mode().Perm())
		}
	}
	restored := NewAgent(&captureClient{}, "", store, "separate").Snapshot()
	if restored.ShortTerm["topic"] != "memory" || restored.Working["task"] != "memory UI" || restored.LongTerm["decision"] != "Go" {
		t.Fatalf("restored=%+v", restored)
	}
	var working map[string]string
	data, err := os.ReadFile(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &working); err != nil {
		t.Fatal(err)
	}
	if _, leaked := working["decision"]; leaked {
		t.Fatal("long-term entry leaked into working file")
	}
}

func TestLegacyShortTermDropsMessagesAndKeepsFacts(t *testing.T) {
	dir := t.TempDir()
	legacyDir := filepath.Join(dir, "legacy")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"messages":[{"role":"user","content":"whole message"}],"notes":{"topic":"memory"}}`)
	path := filepath.Join(legacyDir, "short-term.json")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewJSONLayerStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	layers, err := store.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if layers.ShortTerm["topic"] != "memory" {
		t.Fatalf("layers=%+v", layers)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "whole message") || !strings.Contains(string(data), `"topic": "memory"`) {
		t.Fatalf("legacy file was not minimized: %s", data)
	}
}

func TestTranscriptIsVolatileAndKeepsLastTwelveMessages(t *testing.T) {
	agent := NewAgent(&captureClient{}, "", nil, "window")
	for i := 0; i < 8; i++ {
		if _, err := agent.Ask(context.Background(), string(rune('a'+i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	messages := agent.Transcript()
	if len(messages) != dialogMessageLimit {
		t.Fatalf("messages=%d", len(messages))
	}
	if messages[0].Content != "c" || messages[len(messages)-1].Role != "assistant" {
		t.Fatalf("unexpected window: %+v", messages)
	}
	if len(agent.Snapshot().ShortTerm) != 0 {
		t.Fatal("whole messages leaked into persisted memory")
	}
}

func TestFailedTurnDoesNotEnterShortTermMemory(t *testing.T) {
	agent := NewAgent(&captureClient{err: context.Canceled}, "", nil, "rollback")
	if _, err := agent.Ask(context.Background(), "not saved", nil); err == nil {
		t.Fatal("expected error")
	}
	if len(agent.Transcript()) != 0 {
		t.Fatal("failed turn was saved")
	}
}

func TestAutomaticExtractorClassifiesAndPersistsFacts(t *testing.T) {
	store, err := NewJSONLayerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	extractor := &scriptedExtractor{updates: []MemoryUpdate{{
		Short:   LayerUpdate{Set: map[string]string{"current.file": "agent.go"}},
		Working: LayerUpdate{Set: map[string]string{"task.goal": "добавить память"}},
		Long:    LayerUpdate{Set: map[string]string{"profile.language": "русский"}},
	}, {}}}
	client := &captureClient{}
	agent := NewAgent(client, "", store, "automatic")
	agent.extractor = extractor
	if _, err := agent.Ask(context.Background(), "Работаем над agent.go, отвечай по-русски", nil); err != nil {
		t.Fatal(err)
	}
	layers := agent.Snapshot()
	if layers.ShortTerm["current.file"] != "agent.go" || layers.Working["task.goal"] != "добавить память" || layers.LongTerm["profile.language"] != "русский" {
		t.Fatalf("layers=%+v", layers)
	}
	if extractor.calls != 1 {
		t.Fatalf("extractor calls=%d", extractor.calls)
	}
	if _, err := agent.Ask(context.Background(), "Что ты помнишь?", nil); err != nil {
		t.Fatal(err)
	}
	contextText := ""
	for _, message := range client.calls[1] {
		contextText += message.Content
	}
	for _, want := range []string{"agent.go", "добавить память", "русский"} {
		if !strings.Contains(contextText, want) {
			t.Errorf("next answer context misses %q", want)
		}
	}
	if extractor.calls != 2 {
		t.Fatalf("extractor calls=%d", extractor.calls)
	}
	restored := NewAgent(&captureClient{}, "", store, "automatic").Snapshot()
	if restored.Working["task.goal"] == "" || len(NewAgent(&captureClient{}, "", store, "automatic").Transcript()) != 0 {
		t.Fatal("facts were not persisted separately from transcript")
	}
}

func TestAutomaticExtractorCanDeleteStaleFact(t *testing.T) {
	extractor := &scriptedExtractor{updates: []MemoryUpdate{{Short: LayerUpdate{Delete: []string{"draft"}}}}}
	agent := NewAgent(&captureClient{}, "", nil, "delete")
	if err := agent.Remember("short", "draft", "старый"); err != nil {
		t.Fatal(err)
	}
	agent.extractor = extractor
	if _, err := agent.Ask(context.Background(), "Черновик больше не нужен", nil); err != nil {
		t.Fatal(err)
	}
	if _, exists := agent.Snapshot().ShortTerm["draft"]; exists {
		t.Fatal("stale fact remains")
	}
}

func TestExtractorFailureKeepsAnswerAndReportsWarning(t *testing.T) {
	agent := NewAgent(&captureClient{reply: "Готово"}, "", nil, "warning")
	agent.extractor = &scriptedExtractor{err: context.DeadlineExceeded}
	answer, err := agent.Ask(context.Background(), "hello", nil)
	if err != nil || answer != "Готово" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if !strings.Contains(agent.DrainMemoryWarning(), "deadline") {
		t.Fatal("missing warning")
	}
}

func TestSecretsAreRejectedFromMemory(t *testing.T) {
	agent := NewAgent(&captureClient{}, "", nil, "secrets")
	if err := agent.Remember("long", "api_token", "not-a-real-value"); err == nil {
		t.Fatal("secret-like entry accepted")
	}
}
