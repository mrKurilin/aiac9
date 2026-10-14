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

func TestSupportTicketToolAndAnswerContext(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"docs", "support"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"README.md":            "Продукт Mercury поддерживает вход в аккаунт.",
		"docs/faq.md":          "Если вход отклонён, проверьте версию приложения и передайте тикет специалисту.",
		"support/tickets.json": `{"users":[{"id":"u-1","plan":"Команда","status":"активен"}],"tickets":[{"id":"t-1","user_id":"u-1","subject":"Не работает вход","details":"Вход отклонён","status":"открыт"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"support_ticket","arguments":{"id":"t-1"}}}` + "\n"
	var output bytes.Buffer
	if err := serveMCP(root, strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`+"\n"+`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"+request), &output); err != nil {
		t.Fatal(err)
	}
	var response rpcMessage
	decoder := json.NewDecoder(&output)
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(response.Result, []byte("Команда")) {
		t.Fatal("user context missing")
	}
	ctxText, err := supportTicket(root, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeChat{}
	a := &agent{client: client, search: searchIndex{root: root, path: filepath.Join(t.TempDir(), "index.json")}, supportContext: ctxText}
	if _, _, err := a.answer(context.Background(), "Почему вход отклонён?", true, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if len(client.seen) != 1 || !strings.Contains(client.seen[0][0].Content, "Тикет t-1") || !strings.Contains(client.seen[0][0].Content, "docs/faq.md") {
		t.Fatal("support context or FAQ missing")
	}
}

func TestSupportConversationSwitchAndReset(t *testing.T) {
	root := testProject(t)
	if err := os.Mkdir(filepath.Join(root, "support"), 0700); err != nil {
		t.Fatal(err)
	}
	db := []byte(`{"users":[{"id":"u-1","plan":"Команда","status":"активен"}],"tickets":[{"id":"t-1","user_id":"u-1","subject":"Авторизация","details":"Вход отклонён"},{"id":"t-2","subject":"Письмо","details":"Адрес не подтверждён"}]}`)
	path := filepath.Join(root, "support", "tickets.json")
	if err := os.WriteFile(path, db, 0600); err != nil {
		t.Fatal(err)
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
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Ответ"}}]}`)
	}))
	defer server.Close()
	binary := filepath.Join(t.TempDir(), "mrkai")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	cmd := exec.Command(binary)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "MRKAI_PROJECT_ROOT="+root, "DEEPSEEK_API_KEY=placeholder", "DEEPSEEK_BASE_URL="+server.URL)
	cmd.Stdin = strings.NewReader("/support t-1 Почему не работает авторизация?\nЧто попробовать дальше?\n/support t-2 Почему нет письма?\n/reset\nКак войти?\n/support t-1 Вход отклонён\n/clear\nКак войти?\n/index status\n/exit\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v: %s", err, output)
	}
	if len(requests) != 6 {
		t.Fatalf("requests: %d; %s", len(requests), output)
	}
	if len(requests[1]) != 4 || !strings.Contains(requests[1][0].Content, "Тикет t-1") {
		t.Fatal("follow-up lost ticket or chat history")
	}
	if len(requests[2]) != 2 || strings.Contains(requests[2][0].Content, "Тикет t-1") || !strings.Contains(requests[2][0].Content, "Тикет t-2") {
		t.Fatal("ticket switch leaked previous conversation")
	}
	for _, i := range []int{3, 5} {
		if len(requests[i]) != 2 || strings.Contains(requests[i][0].Content, "Контекст выбранного тикета") {
			t.Fatal("reset alias retained ticket context")
		}
	}
	remaining, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(remaining, db) || !strings.Contains(string(output), "Индекс: ") {
		t.Fatal("reset removed durable data")
	}
}

type scriptedChat struct {
	t       *testing.T
	calls   int
	endless bool
}

func (f *scriptedChat) Complete(_ context.Context, messages []message, tools []toolDefinition) (message, error) {
	f.calls++
	if f.calls == 1 || f.endless {
		var calls []toolCall
		for i, id := range []string{"t-1", "missing"} {
			call := toolCall{ID: fmt.Sprintf("call-%d", i), Type: "function"}
			call.Function.Name = tools[0].Function.Name
			call.Function.Arguments = fmt.Sprintf(`{"id":%q}`, id)
			calls = append(calls, call)
		}
		return message{Role: "assistant", ToolCalls: calls}, nil
	}
	var results []string
	for _, msg := range messages {
		if msg.Role == "tool" {
			results = append(results, msg.Content)
		}
	}
	if len(results) < 2 || !strings.Contains(results[0], "Тикет t-1") || !strings.Contains(results[1], "тикет не найден") {
		f.t.Fatal("tool result or error missing")
	}
	if !strings.Contains(messages[0].Content, "docs/architecture.md") {
		f.t.Fatal("ticket did not enrich retrieval")
	}
	return message{Role: "assistant", Content: "Ответ с учётом тикета"}, nil
}

func TestNaturalMCPCallsErrorsRetrievalAndLimit(t *testing.T) {
	root := testProject(t)
	if err := os.Mkdir(filepath.Join(root, "support"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "support", "tickets.json"), []byte(`{"tickets":[{"id":"t-1","subject":"Agent","details":"Agent хранит историю"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveMCP(root, serverIn, serverOut) }()
	mcp := &mcpClient{stdin: clientOut, stdout: bufio.NewReader(clientIn)}
	defer func() { _ = clientOut.Close(); _ = serverOut.Close(); <-done }()
	var initialized map[string]any
	if err := mcp.call(context.Background(), "initialize", map[string]any{"protocolVersion": protocolVersion}, &initialized); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(clientOut).Encode(rpcMessage{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	client := &scriptedChat{t: t}
	a := &agent{client: client, mcp: mcp, search: searchIndex{root: root, path: filepath.Join(t.TempDir(), "index.json")}}
	if _, _, err := a.answer(context.Background(), "Что делать по t-1?", true, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if len(a.history) != 5 {
		t.Fatal("tool results not retained for follow-up")
	}
	a.reset()
	client.calls = 0
	client.endless = true
	if _, _, err := a.answer(context.Background(), "Что делать по t-1?", true, func(string) {}); err == nil || !strings.Contains(err.Error(), "предел") {
		t.Fatalf("missing bounded-loop error: %v", err)
	}
	if len(a.history) != 0 {
		t.Fatal("failed turn saved partial history")
	}
}

func TestProductFAQAnswersAssignmentQuestion(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{client: &fakeChat{}, search: searchIndex{root: root, path: filepath.Join(t.TempDir(), "index.json")}}
	_, hits, err := a.answer(context.Background(), "Почему не работает авторизация?", true, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range hits {
		if hit.Source == "docs/faq.md" && strings.Contains(hit.Text, "blocked_date") {
			found = true
		}
	}
	if !found {
		t.Fatalf("assignment question missed FAQ: %+v", hits)
	}
}
