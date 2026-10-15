package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
		t.Fatalf("wrong sources: %v", seen)
	}
	hits, err := s.search("Как Agent хранит историю?", 3)
	if err != nil || len(hits) == 0 || hits[0].Source != "docs/architecture.md" {
		t.Fatalf("search: %v, %v", hits, err)
	}
}

func TestMCPServerAdvertisesAndCallsGitBranch(t *testing.T) {
	root := testProject(t)
	cmd := exec.Command("git", "init", "-b", "samplebranch", root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"git_branch","arguments":{}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := serveMCP(root, strings.NewReader(requests), &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	for i := 1; i <= 3; i++ {
		var response rpcMessage
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if string(response.ID) != string(rune('0'+i)) || response.Error != nil {
			t.Fatalf("response %d: %+v", i, response)
		}
		if i == 2 && !bytes.Contains(response.Result, []byte("git_branch")) {
			t.Fatalf("no tool in %s", response.Result)
		}
		if i == 3 && !bytes.Contains(response.Result, []byte("samplebranch")) {
			t.Fatalf("wrong branch in %s", response.Result)
		}
	}
}

type fakeChat struct {
	seen [][]message
}

func (f *fakeChat) Complete(_ context.Context, messages []message, _ []toolDefinition) (message, error) {
	f.seen = append(f.seen, append([]message(nil), messages...))
	return message{Role: "assistant", Content: "Ответ по проекту"}, nil
}

func TestHelpContextAndResetPreserveIndex(t *testing.T) {
	root := testProject(t)
	client := &fakeChat{}
	a := &agent{client: client, search: searchIndex{root: root, path: filepath.Join(t.TempDir(), "index.json")}}
	progress := func(string) {}
	_, hits, err := a.answer(context.Background(), "Как Agent хранит историю?", true, progress)
	if err != nil || len(hits) == 0 {
		t.Fatalf("answer: %v, hits %v", err, hits)
	}
	if !strings.Contains(client.seen[0][0].Content, "docs/architecture.md") || len(a.history) != 2 {
		t.Fatal("project evidence or history missing")
	}
	a.reset()
	if len(a.history) != 0 {
		t.Fatal("history not reset")
	}
	if _, err := a.search.load(); err != nil {
		t.Fatalf("index removed by reset: %v", err)
	}
}

type toolChat struct {
	calls int
}

func (f *toolChat) Complete(_ context.Context, messages []message, tools []toolDefinition) (message, error) {
	f.calls++
	if f.calls == 1 {
		if len(tools) != 5 || tools[0].Function.Name != "mcp__project__git_branch" {
			return message{}, fmt.Errorf("tool discovery failed: %v", tools)
		}
		call := toolCall{ID: "call-1", Type: "function"}
		call.Function.Name = tools[0].Function.Name
		call.Function.Arguments = `{}`
		return message{Role: "assistant", ToolCalls: []toolCall{call}}, nil
	}
	for _, msg := range messages {
		if msg.Role == "tool" && msg.ToolCallID == "call-1" && msg.Content == "samplebranch" {
			return message{Role: "assistant", Content: "Текущая ветка samplebranch"}, nil
		}
	}
	return message{}, fmt.Errorf("tool result was not returned to model")
}

func TestAgentUsesDiscoveredMCPTool(t *testing.T) {
	root := testProject(t)
	if output, err := exec.Command("git", "init", "-b", "samplebranch", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveMCP(root, serverIn, serverOut) }()
	mcp := &mcpClient{stdin: clientOut, stdout: bufio.NewReader(clientIn)}
	defer func() {
		_ = clientOut.Close()
		_ = serverOut.Close()
		<-done
	}()
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := mcp.call(context.Background(), "initialize", map[string]any{}, &initialized); err != nil {
		t.Fatal(err)
	}
	client := &toolChat{}
	a := &agent{client: client, mcp: mcp, search: searchIndex{root: root, path: filepath.Join(t.TempDir(), "index.json")}}
	answer, _, err := a.answer(context.Background(), "Какая сейчас ветка?", false, func(string) {})
	if err != nil || answer != "Текущая ветка samplebranch" || client.calls != 2 {
		t.Fatalf("answer %q, calls %d, err %v", answer, client.calls, err)
	}
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
