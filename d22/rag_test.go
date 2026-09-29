package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type controlQuestion struct {
	Question string `json:"question"`
	Expected string `json:"expected"`
	Source   string `json:"source"`
	Section  string `json:"section"`
}

type evidenceClient struct {
	expected string
	calls    int
}

type jsonRAGClient struct{}

func (jsonRAGClient) Complete(_ context.Context, messages []Message) (string, error) {
	if strings.Contains(messages[len(messages)-1].Content, "[Источник") {
		return `{"answer":"25 дней PTO в год."}`, nil
	}
	return `{"answer":"Не знаю без источника."}`, nil
}

func (c *evidenceClient) Complete(_ context.Context, messages []Message) (string, error) {
	c.calls++
	last := messages[len(messages)-1].Content
	if strings.Contains(last, "[Источник") && strings.Contains(last, c.expected) {
		return c.expected, nil
	}
	return "Нет данных", nil
}

func TestRAGControlQuestions(t *testing.T) {
	ctx := context.Background()
	data, err := os.ReadFile(filepath.Join("testdata", "control-questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var questions []controlQuestion
	if err := json.Unmarshal(data, &questions); err != nil {
		t.Fatal(err)
	}
	if len(questions) != 10 {
		t.Fatalf("ожидалось 10 вопросов, получено %d", len(questions))
	}
	docs, err := collectDocuments(ctx, "knowledge", nil)
	if err != nil {
		t.Fatal(err)
	}
	index, err := buildDocumentIndex(ctx, docs, "structure", nil)
	if err != nil {
		t.Fatal(err)
	}
	indexDir := t.TempDir()
	if err := saveDocumentIndex(filepath.Join(indexDir, "structure.json"), index); err != nil {
		t.Fatal(err)
	}
	for _, item := range questions {
		t.Run(item.Section, func(t *testing.T) {
			client := &evidenceClient{expected: item.Expected}
			a := NewAgent(client, "Отвечай кратко", nil)
			a.indexDir = indexDir
			base, grounded, hits, err := a.CompareRAG(ctx, item.Question, nil)
			if err != nil {
				t.Fatal(err)
			}
			if client.calls != 2 || base != "Нет данных" || grounded != item.Expected {
				t.Fatalf("неверное сравнение: calls=%d, base=%q, rag=%q", client.calls, base, grounded)
			}
			found := false
			for _, hit := range hits {
				if hit.Chunk.Source == item.Source && hit.Chunk.Section == item.Section {
					found = true
				}
			}
			if !found {
				t.Fatalf("источник %s [%s] отсутствует среди результатов", item.Source, item.Section)
			}
		})
	}
}

func TestRAGCommandsAndModes(t *testing.T) {
	a := NewAgent(&evidenceClient{}, "", nil)
	var out bytes.Buffer
	for _, line := range []string{"/rag on", "/rag status", "/rag off"} {
		if handled, exit := runCommand(a, line, &out); !handled || exit {
			t.Fatalf("команда %q не обработана", line)
		}
	}
	if a.RAGMode() || !strings.Contains(out.String(), "RAG включён") || !strings.Contains(out.String(), "RAG выключен") {
		t.Fatalf("неверный режим или вывод: %s", out.String())
	}
	for _, value := range []string{"/rag", "/rag on", "/rag off", "/rag status", "/rag compare"} {
		if _, ok := exactCommand(value); !ok {
			t.Errorf("нет автодополнения %s", value)
		}
	}
}

func TestRAGCompareRendersTwoPanes(t *testing.T) {
	a := NewAgent(&evidenceClient{expected: "Факт из индекса"}, "", nil)
	index := DocumentIndex{Version: 1, Strategy: "structure", Chunks: []IndexedChunk{{
		Source: "knowledge.md", Section: "Проверка", ChunkID: "chunk-1", Text: "Факт из индекса",
	}}}
	a.indexDir = t.TempDir()
	if err := saveDocumentIndex(a.indexPath("structure"), index); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runRAGCommand(context.Background(), a, "compare Где факт?", &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"БЕЗ RAG", "С RAG", "│", "Вопрос: Где факт?", "Источники RAG:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("comparison misses %q:\n%s", want, out.String())
		}
	}
}

func TestCompareRAGUnwrapsJSONAnswers(t *testing.T) {
	a := NewAgent(jsonRAGClient{}, "", nil)
	a.indexDir = t.TempDir()
	index, err := buildDocumentIndex(context.Background(), []sourceDocument{{path: "handbook.txt", text: "# PTO\n25 дней PTO в год"}}, "structure", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveDocumentIndex(a.indexPath("structure"), index); err != nil {
		t.Fatal(err)
	}
	base, grounded, _, err := a.CompareRAG(context.Background(), "PTO", nil)
	if err != nil {
		t.Fatal(err)
	}
	if base != "Не знаю без источника." || grounded != "25 дней PTO в год." {
		t.Fatalf("base=%q grounded=%q", base, grounded)
	}
}
