package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fileTaskChat struct{ calls, extraReads int }

func (f *fileTaskChat) Complete(_ context.Context, messages []message, tools []toolDefinition) (message, error) {
	f.calls++
	if len(tools) != 5 && !(f.calls == maxToolRounds+1 && len(tools) == 0) {
		return message{}, fmt.Errorf("file tools missing")
	}
	if f.calls <= 3+f.extraReads {
		name, args := "file_search", `{"query":"Widget"}`
		if f.calls >= 2 && f.calls < 3+f.extraReads {
			name, args = "file_read", `{"path":"two.go"}`
		}
		if f.calls == 3+f.extraReads {
			name, args = "file_write", `{"path":"docs/widget.md","content":"# Widget API"}`
		}
		call := toolCall{ID: fmt.Sprintf("call-%d", f.calls), Type: "function"}
		call.Function.Name, call.Function.Arguments = "mcp__project__"+name, args
		return message{Role: "assistant", ToolCalls: []toolCall{call}}, nil
	}
	for _, msg := range messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "Сохранён docs/widget.md") {
			return message{Role: "assistant", Content: "Документация обновлена"}, nil
		}
	}
	return message{}, fmt.Errorf("write result was not returned to model")
}

func TestProjectToolsSearchSeveralFilesAndPersistDocumentation(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"README.md": "Widget API overview", "one.go": "package main\n// Widget API call", "two.go": "package main\n// Widget API result"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	list, err := projectList(context.Background(), root)
	if err != nil || !strings.Contains(list, "one.go") || !strings.Contains(list, "two.go") {
		t.Fatalf("list: %s, %v", list, err)
	}
	search, err := projectSearch(context.Background(), root, "Widget")
	if err != nil || !strings.Contains(search, "README.md") || !strings.Contains(search, "one.go") || !strings.Contains(search, "two.go") {
		t.Fatalf("search: %s, %v", search, err)
	}
	if _, err := projectWrite(root, "docs/widget.md", "# Widget\nОписание API"); err != nil {
		t.Fatal(err)
	}
	text, err := projectRead(root, "docs/widget.md")
	if err != nil || !strings.Contains(text, "Описание API") {
		t.Fatalf("write/read: %q, %v", text, err)
	}
	for _, path := range []string{"../outside.md", "docs/secret.md", ".env", "one.go"} {
		if _, err := projectWrite(root, path, "bad"); err == nil {
			t.Fatalf("unsafe write accepted: %s", path)
		}
	}
}

func TestAgentInitiatesFileSearchReadAndWrite(t *testing.T) {
	for _, extraReads := range []int{0, 12, maxToolRounds - 3} {
		t.Run(fmt.Sprintf("extra_reads_%d", extraReads), func(t *testing.T) {
			testAgentFileTask(t, extraReads)
		})
	}
}

func testAgentFileTask(t *testing.T, extraReads int) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"README.md": "Widget API overview", "one.go": "package main\n// Widget API call", "two.go": "package main\n// Widget API result", "docs/intro.md": "Widget API"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mcp := testFileMCP(t, root)
	client := &fileTaskChat{extraReads: extraReads}
	a := &agent{client: client, mcp: mcp, taskMode: true}
	answer, _, err := a.answer(context.Background(), "Найди Widget и обнови документацию", false, func(string) {})
	if err != nil || answer != "Документация обновлена" || client.calls != 4+extraReads {
		t.Fatalf("answer=%q calls=%d err=%v", answer, client.calls, err)
	}
	content, err := os.ReadFile(filepath.Join(root, "docs", "widget.md"))
	if err != nil || string(content) != "# Widget API" {
		t.Fatalf("saved file: %q, %v", content, err)
	}
}

func testFileMCP(t *testing.T, root string) *mcpClient {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveMCP(root, serverIn, serverOut) }()
	mcp := &mcpClient{stdin: clientOut, stdout: bufio.NewReader(clientIn)}
	t.Cleanup(func() { clientOut.Close(); serverOut.Close(); <-done })
	var initialized map[string]json.RawMessage
	if err := mcp.call(context.Background(), "initialize", map[string]any{}, &initialized); err != nil {
		t.Fatal(err)
	}
	return mcp
}

type budgetChat struct {
	calls, batch int
	ignoreLimit  bool
}

func (f *budgetChat) Complete(_ context.Context, messages []message, tools []toolDefinition) (message, error) {
	f.calls++
	if len(tools) == 0 && !f.ignoreLimit {
		if !strings.Contains(messages[len(messages)-1].Content, "Бюджет инструментов исчерпан") {
			return message{}, fmt.Errorf("missing budget instruction")
		}
		return message{Role: "assistant", Content: "Анализ не завершён, файлы не изменены"}, nil
	}
	var calls []toolCall
	for i := 0; i < f.batch; i++ {
		call := toolCall{ID: fmt.Sprintf("%d-%d", f.calls, i), Type: "function"}
		call.Function.Name = "mcp__project__file_list"
		call.Function.Arguments = `{}`
		calls = append(calls, call)
	}
	return message{Role: "assistant", ToolCalls: calls}, nil
}

func TestAgentToolBudget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		batch       int
		ignoreLimit bool
		wantCalls   int
		wantError   bool
	}{
		{"round_limit_summary", 1, false, maxToolRounds + 1, false},
		{"call_limit_summary", maxToolCalls, false, 2, false},
		{"oversized_batch", maxToolCalls + 1, false, 1, true},
		{"model_ignores_limit", 1, true, maxToolRounds + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &budgetChat{batch: tc.batch, ignoreLimit: tc.ignoreLimit}
			a := &agent{client: client, mcp: testFileMCP(t, t.TempDir()), taskMode: true}
			answer, _, err := a.answer(context.Background(), "Проверь проект", false, func(string) {})
			if client.calls != tc.wantCalls || (err != nil) != tc.wantError {
				t.Fatalf("calls=%d answer=%q err=%v", client.calls, answer, err)
			}
			if tc.wantError {
				if !strings.Contains(err.Error(), "бюджет MCP") {
					t.Fatal(err)
				}
			} else if !strings.Contains(answer, "не завершён") || len(a.history) != 2 {
				t.Fatalf("missing partial result: %q, history=%v", answer, a.history)
			}
		})
	}
}
