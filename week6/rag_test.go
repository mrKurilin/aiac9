package week6

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type keywordEmbedder struct{}

func (keywordEmbedder) Embed(_ context.Context, _ string, texts []string) ([][]float64, error) {
	vectors := make([][]float64, len(texts))
	for i, text := range texts {
		lower := strings.ToLower(text)
		vector := []float64{0, 0}
		if strings.Contains(lower, "отпуск") {
			vector[0] = 1
		}
		if strings.Contains(lower, "расход") {
			vector[1] = 1
		}
		vectors[i] = vector
	}
	return vectors, nil
}

func TestLocalRAGBuildSearchAndAnswer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.md"), []byte("Правила отпуска: 20 рабочих дней."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "expenses.txt"), []byte("Расходы возмещаются после согласования."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.txt"), []byte("Расходы по секретному правилу."), 0600); err != nil {
		t.Fatal(err)
	}
	rag := NewRAG(keywordEmbedder{}, "embeddinggemma", filepath.Join(t.TempDir(), "index.json"))
	count, err := rag.Build(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("ожидалось 2 безопасных фрагмента, получено %d", count)
	}
	hits, err := rag.Search(context.Background(), "Какой отпуск?", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Source != "policy.md" {
		t.Fatalf("неверный поиск: %+v", hits)
	}
	a := NewAgent(&fakeLocal{}, "qwen3:4b-instruct")
	result, sources, err := rag.Answer(context.Background(), a, "Какой отпуск?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "ответ" || len(sources) != 2 || !strings.Contains(FormatHits(sources), "policy.md") {
		t.Fatalf("неверный ответ или источники: %+v %+v", result, sources)
	}
	a.Reset()
	if _, err := rag.Load(); err != nil {
		t.Fatalf("/reset не должен удалять индекс: %v", err)
	}
}
