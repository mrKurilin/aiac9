package week6

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type benchmarkClient struct {
	keywordEmbedder
	calls []Options
}

func (c *benchmarkClient) Models(context.Context) ([]Model, error) {
	return []Model{{Name: "qwen3:4b-instruct"}}, nil
}
func (c *benchmarkClient) Chat(_ context.Context, model string, _ []Message, options Options) (ChatResult, error) {
	c.calls = append(c.calls, options)
	if model != "qwen3:4b-instruct" {
		return ChatResult{}, nil
	}
	return ChatResult{Text: "Ответ", OutputTokens: options.NumPredict / 4}, nil
}
func (c *benchmarkClient) Loaded(context.Context) ([]LoadedModel, error) {
	return []LoadedModel{{Name: "qwen3:4b-instruct", SizeVRAM: 2500000000}}, nil
}

func TestBenchmarkSameModelDifferentProfiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rules.md"), []byte("Отпуск: 20 дней."), 0600); err != nil {
		t.Fatal(err)
	}
	client := &benchmarkClient{}
	rag := NewRAG(client, "embeddinggemma", filepath.Join(t.TempDir(), "index.json"))
	if _, err := rag.Build(context.Background(), dir, nil); err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(client, "qwen3:4b-instruct")
	result, err := rag.Benchmark(context.Background(), agent, "Какой отпуск?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Samples) != 2 || len(client.calls) != 2 {
		t.Fatalf("неверное число профилей: %+v", result)
	}
	if client.calls[0].NumCtx != 8192 || client.calls[1].NumCtx != 4096 {
		t.Fatalf("профили не применены: %+v", client.calls)
	}
	if result.Samples[1].Memory != 2500000000 {
		t.Fatal("метрика памяти потеряна")
	}
	if len(agent.History) != 0 {
		t.Fatal("benchmark изменил историю диалога")
	}
}
