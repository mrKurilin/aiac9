package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"README.md":            "# Mercury\n\nПроект Mercury — терминальный помощник разработчика.",
		"docs/architecture.md": "# Архитектура\n\nМодуль Agent хранит историю диалога. Модуль RAG ищет документацию.",
		"docs/token.md":        "Это не должно попасть в индекс.",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRAGIndexesREADMEAndDocs(t *testing.T) {
	root := testProject(t)
	s := searchIndex{root: root, path: filepath.Join(t.TempDir(), "index.json")}
	count, err := s.build(context.Background(), nil)
	if err != nil || count < 2 {
		t.Fatalf("build: %d, %v", count, err)
	}
	idx, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range idx.Chunks {
		seen[item.Source] = true
	}
	if !seen["README.md"] || !seen["docs/architecture.md"] || seen["docs/token.md"] {
		t.Fatalf("Wrong sources: %v", seen)
	}
	hits, err := s.search("Как Agent хранит историю?", 3)
	if err != nil || len(hits) == 0 || hits[0].Source != "docs/architecture.md" {
		t.Fatalf("Search: %v, %v", hits, err)
	}
}

type fakeChat struct {
	seen     [][]message
	response string
}

func (f *fakeChat) Complete(_ context.Context, messages []message) (message, error) {
	f.seen = append(f.seen, append([]message(nil), messages...))
	content := f.response
	if content == "" {
		content = `{"decision":"comment","body":"Ответ по проекту"}`
	}
	return message{Role: "assistant", Content: content}, nil
}

func TestEntrypointResetAliasesKeepIndex(t *testing.T) {
	root := testProject(t)
	if output, err := exec.Command("git", "init", "-b", "samplebranch", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	var requests [][]message
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, body.Messages)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Ответ"}}]}`)
	}))
	defer server.Close()
	binary := filepath.Join(t.TempDir(), "mrkai")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	cmd := exec.Command(binary)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "MRKAI_PROJECT_ROOT="+root, "DEEPSEEK_API_KEY=placeholder", "DEEPSEEK_BASE_URL="+server.URL)
	cmd.Stdin = strings.NewReader("/index build\nпервый\nвторой\n/reset\nтретий\n/index status\n/clear\nчетвёртый\n/index status\n/exit\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v: %s", err, output)
	}
	if strings.Count(string(output), "Индекс: ") != 2 || len(requests) != 4 {
		t.Fatalf("index or requests missing: %s; calls %d", output, len(requests))
	}
	if len(requests[1]) != 4 || len(requests[2]) != 2 || len(requests[3]) != 2 {
		t.Fatalf("reset did not clear model context: lengths %d, %d, %d", len(requests[1]), len(requests[2]), len(requests[3]))
	}
}

func TestResolveProjectRoot(t *testing.T) {
	root := t.TempDir()
	got, err := resolveProjectRoot(root)
	if err != nil || got != root {
		t.Fatalf("valid directory: %q, %v", got, err)
	}
	got, err = resolveProjectRoot("")
	cwd, _ := os.Getwd()
	if err != nil || got != cwd {
		t.Fatalf("default directory: %q, %v", got, err)
	}
	file := filepath.Join(root, "regular.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(root, "missing")} {
		if _, err := resolveProjectRoot(path); err == nil || !strings.Contains(err.Error(), "MRKAI_PROJECT_ROOT") {
			t.Fatalf("invalid root should report configuration error: %v", err)
		}
	}
}
