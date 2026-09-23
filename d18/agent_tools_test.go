package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingToolStreamModel struct {
	release <-chan struct{}
	calls   int
}

func (*blockingToolStreamModel) Complete(context.Context, []Message) (string, error) {
	return "", fmt.Errorf("unexpected non-streamed completion")
}

func (*blockingToolStreamModel) CompleteWithTools(context.Context, []Message, []ToolDefinition) (Message, error) {
	return Message{}, fmt.Errorf("unexpected non-streamed tool completion")
}

func (m *blockingToolStreamModel) CompleteWithToolsStream(_ context.Context, _ []Message, _ []ToolDefinition, emit func(string) error) (Message, error) {
	m.calls++
	if m.calls == 1 {
		return Message{ToolCalls: []ToolCall{{ID: "lookup-1", Type: "function", Function: ToolCallFunction{Name: "mcp__mrkgitlab__list_open_merge_requests", Arguments: `{}`}}}}, nil
	}
	if err := emit(`{"answer":"При`); err != nil {
		return Message{}, err
	}
	<-m.release
	if err := emit(`вет"}`); err != nil {
		return Message{}, err
	}
	return Message{Content: `{"answer":"Привет"}`}, nil
}

type notifyingWriter struct {
	mu    sync.Mutex
	value strings.Builder
	first chan struct{}
	once  sync.Once
}

func (w *notifyingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.value.Write(p)
	if strings.Contains(w.value.String(), "◆ При") {
		w.once.Do(func() { close(w.first) })
	}
	return n, err
}

func (w *notifyingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.value.String()
}

type scriptedToolModel struct {
	replies []Message
	seen    [][]Message
	tools   [][]ToolDefinition
}

func (s *scriptedToolModel) Complete(context.Context, []Message) (string, error) {
	return "", fmt.Errorf("unexpected fallback")
}

func (s *scriptedToolModel) CompleteWithTools(_ context.Context, messages []Message, tools []ToolDefinition) (Message, error) {
	s.seen = append(s.seen, append([]Message(nil), messages...))
	s.tools = append(s.tools, append([]ToolDefinition(nil), tools...))
	if len(s.replies) == 0 {
		return Message{}, fmt.Errorf("unexpected model call")
	}
	reply := s.replies[0]
	s.replies = s.replies[1:]
	return reply, nil
}

func TestMCPToolsAvailableWithoutKeywordAndModelDecides(t *testing.T) {
	model := &scriptedToolModel{replies: []Message{{Content: `{"answer":"Сказка"}`}}}
	agent := NewAgent(model, "", nil)
	agent.gitlab = connectedGitLabMCP(t, nil)
	raw, err := agent.completeTurn(context.Background(), []Message{{Role: "user", Content: "Расскажи сказку"}}, nil, nil, agent.complete)
	if err != nil || raw != `{"answer":"Сказка"}` {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
	if len(model.tools) != 1 || len(model.tools[0]) != 1 || model.tools[0][0].Function.Name != "mcp__mrkgitlab__list_open_merge_requests" {
		t.Fatalf("tools=%+v", model.tools)
	}
}

func TestMCPToolCallResultReturnsToModel(t *testing.T) {
	name := "mcp__mrkgitlab__list_open_merge_requests"
	api, err := NewGitLabAPI(defaultGitLabAPIURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedToolModel{replies: []Message{
		{ToolCalls: []ToolCall{{ID: "call-1", Type: "function", Function: ToolCallFunction{Name: name, Arguments: `{}`}}}},
		{Content: `{"answer":"Нужен доступ к GitLab"}`},
	}}
	agent := NewAgent(model, "", nil)
	agent.gitlab = connectedGitLabMCP(t, api)
	_, err = agent.completeTurn(context.Background(), []Message{{Role: "user", Content: "Проверь ожидающие изменения"}}, nil, nil, agent.complete)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.seen) != 2 || len(model.seen[1]) != 3 || model.seen[1][1].ToolCalls[0].ID != "call-1" || model.seen[1][2].ToolCallID != "call-1" || !strings.Contains(model.seen[1][2].Content, "GITLAB_API_TOKEN") {
		t.Fatalf("tool exchange=%+v", model.seen)
	}
	if len(model.tools[1]) != 1 {
		t.Fatal("tools disappeared after a call")
	}
}

func TestMCPUnknownToolDoesNotExecute(t *testing.T) {
	model := &scriptedToolModel{replies: []Message{
		{ToolCalls: []ToolCall{{ID: "bad", Type: "function", Function: ToolCallFunction{Name: "mcp__other__unsafe", Arguments: `{}`}}}},
		{Content: `{"answer":"Инструмент недоступен"}`},
	}}
	agent := NewAgent(model, "", nil)
	agent.gitlab = connectedGitLabMCP(t, nil)
	_, err := agent.completeTurn(context.Background(), []Message{{Role: "user", Content: "Проверь"}}, nil, nil, agent.complete)
	if err != nil || !strings.Contains(model.seen[1][2].Content, "неизвестный MCP-инструмент") {
		t.Fatalf("err=%v messages=%+v", err, model.seen)
	}
}

func TestMCPReturnsAllCallsInOneModelReply(t *testing.T) {
	api, err := NewGitLabAPI(defaultGitLabAPIURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	name := "mcp__mrkgitlab__list_open_merge_requests"
	model := &scriptedToolModel{replies: []Message{
		{ToolCalls: []ToolCall{
			{ID: "first", Type: "function", Function: ToolCallFunction{Name: name, Arguments: `{}`}},
			{ID: "second", Type: "function", Function: ToolCallFunction{Name: name, Arguments: `{}`}},
		}},
		{Content: `{"answer":"Оба результата получены"}`},
	}}
	agent := NewAgent(model, "", nil)
	agent.gitlab = connectedGitLabMCP(t, api)
	_, err = agent.completeTurn(context.Background(), []Message{{Role: "user", Content: "Проверь дважды"}}, nil, nil, agent.complete)
	if err != nil || len(model.seen) != 2 || len(model.seen[1]) != 4 || model.seen[1][2].ToolCallID != "first" || model.seen[1][3].ToolCallID != "second" {
		t.Fatalf("err=%v messages=%+v", err, model.seen)
	}
}

func TestMCPRoundLimitReportsError(t *testing.T) {
	model := &scriptedToolModel{}
	for i := 0; i < maxToolRounds; i++ {
		model.replies = append(model.replies, Message{ToolCalls: []ToolCall{{ID: "unknown", Type: "function", Function: ToolCallFunction{Name: "missing", Arguments: `{}`}}}})
	}
	agent := NewAgent(model, "", nil)
	agent.gitlab = connectedGitLabMCP(t, nil)
	_, err := agent.completeTurn(context.Background(), []Message{{Role: "user", Content: "Повторяй"}}, nil, nil, agent.complete)
	if err == nil || !strings.Contains(err.Error(), "предел последовательных вызовов") || len(model.seen) != maxToolRounds {
		t.Fatalf("err=%v rounds=%d", err, len(model.seen))
	}
}

func TestTerminalStreamsAnswerWithMCPConnected(t *testing.T) {
	release := make(chan struct{})
	model := &blockingToolStreamModel{release: release}
	agent := NewAgent(model, "", nil)
	api, err := NewGitLabAPI(defaultGitLabAPIURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	agent.gitlab = connectedGitLabMCP(t, api)
	out := &notifyingWriter{first: make(chan struct{})}
	finished := make(chan error, 1)
	go func() {
		_, err := printAgentTurn(context.Background(), agent, "Привет", out)
		finished <- err
	}()
	select {
	case <-out.first:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("terminal did not print first answer chunk before completion")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Count(got, "Привет") != 1 || strings.Contains(got, "вызываю инструмент") || strings.Contains(got, "↳") || model.calls != 2 {
		t.Fatalf("terminal output=%q", got)
	}
}
