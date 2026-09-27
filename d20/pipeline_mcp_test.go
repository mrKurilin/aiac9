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
	"path/filepath"
	"strings"
	"testing"
)

type chainModel struct{ step int }

func (*chainModel) Complete(context.Context, []Message) (string, error) {
	return "", fmt.Errorf("unexpected fallback")
}

func (m *chainModel) CompleteWithTools(_ context.Context, messages []Message, tools []ToolDefinition) (Message, error) {
	want := map[string]bool{"mcp__mrkpipeline__search": true, "mcp__mrkpipeline__summarize": true, "mcp__mrkpipeline__save_to_file": true}
	for _, tool := range tools {
		delete(want, tool.Function.Name)
	}
	if len(want) != 0 {
		return Message{}, fmt.Errorf("missing advertised tools: %v", want)
	}
	call := func(id, name string, args any) (Message, error) {
		encoded, err := json.Marshal(args)
		return Message{ToolCalls: []ToolCall{{ID: id, Type: "function", Function: ToolCallFunction{Name: "mcp__mrkpipeline__" + name, Arguments: string(encoded)}}}}, err
	}
	switch m.step {
	case 0:
		m.step++
		return call("search-1", "search", map[string]string{"query": "MCP"})
	case 1:
		if messages[len(messages)-1].Role != "tool" || messages[len(messages)-1].ToolCallID != "search-1" {
			return Message{}, fmt.Errorf("search result missing")
		}
		var hits []searchHit
		if err := json.Unmarshal([]byte(messages[len(messages)-1].Content), &hits); err != nil || len(hits) != 1 {
			return Message{}, fmt.Errorf("invalid search result: %v", err)
		}
		m.step++
		return call("summary-1", "summarize", map[string]string{"text": hits[0].Text})
	case 2:
		if messages[len(messages)-1].Role != "tool" || messages[len(messages)-1].ToolCallID != "summary-1" {
			return Message{}, fmt.Errorf("summary result missing")
		}
		var summary string
		if err := json.Unmarshal([]byte(messages[len(messages)-1].Content), &summary); err != nil {
			return Message{}, err
		}
		m.step++
		return call("save-1", "save_to_file", map[string]string{"filename": "chain.md", "text": summary})
	case 3:
		if messages[len(messages)-1].Role != "tool" || messages[len(messages)-1].ToolCallID != "save-1" {
			return Message{}, fmt.Errorf("save result missing")
		}
		m.step++
		return Message{Content: "Цепочка завершена"}, nil
	default:
		return Message{}, fmt.Errorf("extra model round")
	}
}

func connectedPipelineMCP(t *testing.T, knowledge, reports string) *MCPClient {
	t.Helper()
	serverIn, clientIn := io.Pipe()
	clientOut, serverOut := io.Pipe()
	finished := make(chan error, 1)
	go func() {
		finished <- servePipelineMCP(serverIn, serverOut, knowledge, reports)
		_ = serverOut.Close()
	}()
	client := &MCPClient{stdin: clientIn, stdout: bufio.NewReader(clientOut)}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := client.call("initialize", map[string]any{"protocolVersion": "2025-06-18"}, &initialized); err != nil {
		t.Fatal(err)
	}
	if err := client.notify("notifications/initialized"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientIn.Close()
		if err := <-finished; err != nil {
			t.Errorf("pipeline MCP: %v", err)
		}
		_ = clientOut.Close()
	})
	return client
}

func TestModelChainsThreeMCPToolsWithReturnedData(t *testing.T) {
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge")
	if err := os.Mkdir(knowledge, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowledge, "topic.md"), []byte("MCP chain input is preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	model := &chainModel{}
	agent := NewAgent(model, "", nil)
	agent.pipeline = connectedPipelineMCP(t, knowledge, filepath.Join(root, "reports"))
	answer, err := agent.completeTurn(context.Background(), []Message{{Role: "user", Content: "Сохрани выдержку про MCP"}}, nil, nil, agent.complete)
	if err != nil || answer != "Цепочка завершена" || model.step != 4 {
		t.Fatalf("answer=%q step=%d err=%v", answer, model.step, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "reports", "chain.md"))
	if err != nil || strings.TrimSpace(string(data)) != "MCP chain input is preserved" {
		t.Fatalf("saved data=%q err=%v", data, err)
	}
}

func TestPipelineCommandPrintsReadableResult(t *testing.T) {
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge")
	if err := os.Mkdir(knowledge, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowledge, "topic.md"), []byte("MCP tools pass results between steps"), 0600); err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(nil, "", nil)
	agent.pipeline = connectedPipelineMCP(t, knowledge, filepath.Join(root, "reports"))
	var out bytes.Buffer
	if err := runPipelineMCPCommand(context.Background(), agent, "run MCP | readable.md", &out); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, part := range []string{"search: найдено документов: 1", "Отчёт сохранён:", "Найдено документов: 1", "Источники: topic.md", "Выдержка:\nMCP tools pass results between steps"} {
		if !strings.Contains(output, part) {
			t.Fatalf("missing %q in terminal output: %s", part, output)
		}
	}
	if strings.Contains(output, `{"found":`) {
		t.Fatalf("raw JSON leaked into terminal result: %s", output)
	}
}

func TestNaturalLanguageTurnRunsPipelineThroughModel(t *testing.T) {
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge")
	if err := os.Mkdir(knowledge, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowledge, "topic.md"), []byte("MCP tools pass results between steps"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Messages []Message        `json:"messages"`
			Tools    []ToolDefinition `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode model request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var event string
		if calls == 1 {
			if request.Messages[len(request.Messages)-1].Content != "Найди материалы про MCP, составь выдержку и сохрани в mcp-report.md" {
				t.Errorf("natural language prompt was changed: %+v", request.Messages)
			}
			found := false
			for _, tool := range request.Tools {
				found = found || tool.Function.Name == "mcp__mrkpipeline__run_pipeline"
			}
			if !found {
				t.Error("pipeline tool was not offered to model")
			}
			event = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"pipeline-1","type":"function","function":{"name":"mcp__mrkpipeline__run_pipeline","arguments":"{\"query\":\"MCP\",\"filename\":\"mcp-report.md\"}"}}]}}]}`
		} else {
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "tool" || last.ToolCallID != "pipeline-1" || !strings.Contains(last.Content, `"found":1`) {
				t.Errorf("pipeline result was not returned to model: %+v", last)
			}
			event = `{"choices":[{"delta":{"content":"{\"answer\":\"Отчёт сохранён в data/reports/mcp-report.md\"}"}}]}`
		}
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", event)
	}))
	defer server.Close()
	client := NewDeepSeekClient("placeholder", server.URL, "test-model", server.Client())
	agent := NewAgent(client, "", nil)
	agent.pipeline = connectedPipelineMCP(t, knowledge, filepath.Join(root, "reports"))
	var out bytes.Buffer
	_, err := handleTerminalLine(context.Background(), agent, "Найди материалы про MCP, составь выдержку и сохрани в mcp-report.md", &out)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(out.String(), "search: найдено документов: 1") || !strings.Contains(out.String(), "Отчёт сохранён в data/reports/mcp-report.md") {
		t.Fatalf("model calls=%d terminal=%s", calls, out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "reports", "mcp-report.md")); err != nil {
		t.Fatalf("report not saved: %v", err)
	}
}

func TestNaturalLanguageWithoutKeyExplainsConfiguration(t *testing.T) {
	client := NewDeepSeekClient("", "https://example.test/chat", "test-model", &http.Client{})
	_, err := client.CompleteWithToolsStream(context.Background(), []Message{{Role: "user", Content: "Найди MCP"}}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
		t.Fatalf("missing key error: %v", err)
	}
}

func TestPipelineTransfersSearchTextToSummaryAndFile(t *testing.T) {
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge")
	if err := os.Mkdir(knowledge, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowledge, "mcp.md"), []byte("# MCP\nПервая содержательная строка.\nВторая строка."), 0600); err != nil {
		t.Fatal(err)
	}
	s := pipelineService{knowledgeDir: knowledge, reportsDir: filepath.Join(root, "reports")}
	steps := []string{}
	result, err := s.run("MCP", "report.md", func(step string) { steps = append(steps, step) })
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 5 || result["found"] != 1 || !strings.Contains(result["summary"].(string), "Первая содержательная строка") {
		t.Fatalf("unexpected pipeline result: %#v; steps: %#v", result, steps)
	}
	data, err := os.ReadFile(filepath.Join(root, "reports", "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), result["summary"].(string)) || !strings.Contains(string(data), "mcp.md") {
		t.Fatalf("search result was not transferred to report: %s", data)
	}
}

func TestPipelineStopsBeforeSaveOnMissingMatch(t *testing.T) {
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge")
	if err := os.Mkdir(knowledge, 0700); err != nil {
		t.Fatal(err)
	}
	s := pipelineService{knowledgeDir: knowledge, reportsDir: filepath.Join(root, "reports")}
	if _, err := s.run("unmatched", "report.md", func(string) {}); err == nil {
		t.Fatal("expected missing-match error")
	}
	if _, err := os.Stat(s.reportsDir); !os.IsNotExist(err) {
		t.Fatalf("report directory should not exist: %v", err)
	}
}

func TestPipelineRejectsPathTraversal(t *testing.T) {
	s := pipelineService{reportsDir: t.TempDir()}
	for _, name := range []string{"../escape.md", "/tmp/escape.md", "hidden.txt"} {
		if _, err := s.save(name, "content"); err == nil {
			t.Fatalf("accepted filename %q", name)
		}
	}
}

func TestPipelineMCPProtocolAndTerminalCommand(t *testing.T) {
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge")
	if err := os.Mkdir(knowledge, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowledge, "sample.md"), []byte("MCP pipeline passes data"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := []mcpMessage{
		{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"2025-06-18"}`)},
		{JSONRPC: "2.0", Method: "notifications/initialized"},
		{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list", Params: json.RawMessage(`{}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "tools/call", Params: json.RawMessage(`{"name":"run_pipeline","arguments":{"query":"MCP","filename":"result.md"}}`)},
	}
	var in, out bytes.Buffer
	for _, request := range requests {
		if err := json.NewEncoder(&in).Encode(request); err != nil {
			t.Fatal(err)
		}
	}
	if err := servePipelineMCP(&in, &out, knowledge, filepath.Join(root, "reports")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "run_pipeline") || !strings.Contains(out.String(), "search: найдено документов: 1") || !strings.Contains(out.String(), `\"found\":1`) {
		t.Fatalf("missing MCP tools, progress, or result: %s", out.String())
	}
	if children := directCommandChildren("/mcp mrkpipeline"); len(children) != 3 {
		t.Fatalf("pipeline commands missing from completion: %#v", children)
	}
	if got := mcpOperation("/mcp mrkpipeline run MCP | result.md"); got == "" {
		t.Fatal("pipeline command should show terminal activity")
	}
}
