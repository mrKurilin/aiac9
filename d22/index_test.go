package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndexBuildAndCompare(t *testing.T) {
	root := t.TempDir()
	first := "# Оркестрация\n" + strings.Repeat("агент вызывает инструмент и получает результат. ", 80) + "\n"
	second := "# Эмбеддинги\n" + strings.Repeat("индекс ищет похожие документы локально. ", 80)
	extra := ""
	for i := 0; i < 8; i++ {
		extra += "\n# Приложение\n" + strings.Repeat("короткий раздел с описанием программы. ", 4)
	}
	if err := os.WriteFile(filepath.Join(root, "guide.md"), []byte(first+second+extra), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "token.json"), []byte("private"), 0644); err != nil {
		t.Fatal(err)
	}
	a := NewAgent(nil, "", nil)
	a.indexDir = filepath.Join(t.TempDir(), "index")
	var out bytes.Buffer
	handled, exit := runCommandContext(context.Background(), a, "/index build "+root, &out)
	if !handled || exit || !strings.Contains(out.String(), "Готово:") {
		t.Fatalf("build: %s", out.String())
	}
	fixed, err := a.LoadIndex("fixed")
	if err != nil {
		t.Fatal(err)
	}
	structure, err := a.LoadIndex("structure")
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Sources != 1 || fixed.Words < 600 || len(fixed.Chunks) == 0 || len(structure.Chunks) < 2 {
		t.Fatalf("indexes: fixed=%+v structure chunks=%d", fixed, len(structure.Chunks))
	}
	if len(fixed.Chunks) == len(structure.Chunks) {
		t.Fatal("strategies unexpectedly produced equal chunk counts")
	}
	for _, chunk := range structure.Chunks {
		if chunk.Source != "guide.md" || chunk.Title != "guide.md" || chunk.Section == "" || chunk.ChunkID == "" || len(chunk.Vector) != embeddingDimensions {
			t.Fatalf("incomplete chunk metadata: %+v", chunk)
		}
	}
	hits := searchDocumentIndex(structure, "эмбеддинги индекс", 3)
	if len(hits) == 0 || hits[0].Chunk.Section != "Эмбеддинги" {
		t.Fatalf("wrong top section: %q", func() string {
			if len(hits) > 0 {
				return hits[0].Chunk.Section
			}
			return ""
		}())
	}
	out.Reset()
	runCommandContext(context.Background(), a, "/index compare эмбеддинги индекс", &out)
	if !strings.Contains(out.String(), "fixed:") || !strings.Contains(out.String(), "structure:") {
		t.Fatalf("compare: %s", out.String())
	}
}

func TestIndexSkipsHiddenAndSensitiveNames(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".hidden"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"public.md":        "# Public\nhello world",
		"secret-notes.md":  "private content",
		"ignored_test.go":  "package ignored",
		".hidden/notes.md": "private content",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	docs, err := collectDocuments(context.Background(), root, nil)
	if err != nil || len(docs) != 1 || docs[0].path != "public.md" {
		t.Fatalf("docs=%+v err=%v", docs, err)
	}
}

func TestIndexIncludesPublicCorpusInsideDataDirectory(t *testing.T) {
	root := t.TempDir()
	corpus := filepath.Join(root, "example-handbook", "data")
	if err := os.MkdirAll(corpus, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corpus, "Employee_Handbook.txt"), []byte("Vacation policy"), 0644); err != nil {
		t.Fatal(err)
	}
	docs, err := collectDocuments(context.Background(), root, nil)
	if err != nil || len(docs) != 1 || docs[0].path != "example-handbook/data/Employee_Handbook.txt" {
		t.Fatalf("docs=%+v err=%v", docs, err)
	}
}

func TestPlainTextHandbookSectionsBecomeSeparateChunks(t *testing.T) {
	doc := sourceDocument{path: "Employee_Handbook.txt", text: "EMPLOYEE HANDBOOK\n=================\nintro\n\nPAID TIME OFF\n=============\nEmployees receive 20 vacation days per year.\n"}
	index, err := buildDocumentIndex(context.Background(), []sourceDocument{doc}, "structure", nil)
	if err != nil {
		t.Fatal(err)
	}
	hits := searchDocumentIndex(index, "vacation days", 1)
	if len(hits) != 1 || hits[0].Chunk.Section != "PAID TIME OFF" {
		t.Fatalf("top hit=%+v", hits)
	}
}

func TestIndexCommandsComplete(t *testing.T) {
	for _, command := range []string{"/index build", "/index github", "/index status", "/index search fixed", "/index search structure", "/index compare"} {
		if _, ok := exactCommand(command); !ok {
			t.Fatalf("missing completion: %s", command)
		}
	}
}
