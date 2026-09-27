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
	"time"
)

func connectedSchedulerMCP(t *testing.T, s *Scheduler) *MCPClient {
	t.Helper()
	serverIn, clientIn := io.Pipe()
	clientOut, serverOut := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serveSchedulerMCP(serverIn, serverOut, s, func() time.Time { return time.Unix(1000, 0) })
		_ = serverOut.Close()
	}()
	c := &MCPClient{stdin: clientIn, stdout: bufio.NewReader(clientOut)}
	var init any
	if err := c.call("initialize", map[string]any{"protocolVersion": "2025-06-18"}, &init); err != nil {
		t.Fatal(err)
	}
	if err := c.notify("notifications/initialized"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientIn.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
		_ = clientOut.Close()
	})
	return c
}

type orchestrationModel struct {
	chain chainModel
	step  int
	path  string
	job   ScheduledJob
}

func (*orchestrationModel) Complete(context.Context, []Message) (string, error) {
	return "", fmt.Errorf("unexpected fallback")
}
func (m *orchestrationModel) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDefinition) (Message, error) {
	if m.step < 3 {
		m.step++
		return m.chain.CompleteWithTools(ctx, messages, tools)
	}
	last := messages[len(messages)-1]
	call := func(id, name string, args any) (Message, error) {
		data, err := json.Marshal(args)
		return Message{ToolCalls: []ToolCall{{ID: id, Type: "function", Function: ToolCallFunction{Name: "mcp__mrkscheduler__" + name, Arguments: string(data)}}}}, err
	}
	switch m.step {
	case 3:
		if last.ToolCallID != "save-1" {
			return Message{}, fmt.Errorf("save result missing")
		}
		if err := json.Unmarshal([]byte(last.Content), &m.path); err != nil {
			return Message{}, err
		}
		if data, err := os.ReadFile(m.path); err != nil || !strings.Contains(string(data), "MCP chain input") {
			return Message{}, fmt.Errorf("report not saved before scheduling")
		}
		m.step++
		return call("schedule-1", "schedule_reminder", map[string]any{"text": "Проверить отчёт " + m.path, "delay_seconds": 60})
	case 4:
		if last.ToolCallID != "schedule-1" {
			return Message{}, fmt.Errorf("scheduler result missing")
		}
		if err := json.Unmarshal([]byte(last.Content), &m.job); err != nil || m.job.ID == "" {
			return Message{}, fmt.Errorf("invalid job")
		}
		m.step++
		return call("verify-1", "list_reminders", map[string]any{})
	case 5:
		var result struct {
			Jobs []ScheduledJob `json:"jobs"`
		}
		if last.ToolCallID != "verify-1" {
			return Message{}, fmt.Errorf("verification missing")
		}
		if err := json.Unmarshal([]byte(last.Content), &result); err != nil || len(result.Jobs) != 1 || result.Jobs[0].ID != m.job.ID || result.Jobs[0].Text != "Проверить отчёт "+m.path {
			return Message{}, fmt.Errorf("wrong reminder")
		}
		m.step++
		return Message{Content: "Отчёт сохранён, напоминание проверено"}, nil
	}
	return Message{}, fmt.Errorf("extra round")
}

func TestLongFlowAcrossServersPreservesOrderAndData(t *testing.T) {
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge")
	if err := os.Mkdir(knowledge, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowledge, "topic.md"), []byte("MCP chain input is preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	scheduler, err := NewScheduler(filepath.Join(root, "reminders.json"))
	if err != nil {
		t.Fatal(err)
	}
	model := &orchestrationModel{}
	a := NewAgent(model, "", nil)
	a.pipeline = connectedPipelineMCP(t, knowledge, filepath.Join(root, "reports"))
	a.scheduler = connectedSchedulerMCP(t, scheduler)
	a.gitlab = connectedGitLabMCP(t, nil)
	var progress []string
	answer, err := a.completeTurn(context.Background(), []Message{{Role: "user", Content: "Найди MCP, обработай текст, сохрани chain.md и через минуту напомни проверить отчёт; проверь напоминание"}}, nil, func(s string) { progress = append(progress, s) }, a.complete)
	if err != nil || model.step != 6 || answer != "Отчёт сохранён, напоминание проверено" {
		t.Fatalf("answer=%q step=%d err=%v", answer, model.step, err)
	}
	var calls []string
	for _, p := range progress {
		if strings.Contains(p, "вызываю инструмент") {
			calls = append(calls, p)
		}
	}
	want := []string{"mrkPipeline.search", "mrkPipeline.summarize", "mrkPipeline.save_to_file", "mrkScheduler.schedule_reminder", "mrkScheduler.list_reminders"}
	if len(calls) != len(want) {
		t.Fatalf("calls=%v", calls)
	}
	for i, name := range want {
		if !strings.HasPrefix(calls[i], name+":") {
			t.Fatalf("wrong order: %v", calls)
		}
	}
}

func TestSameToolNameOnDifferentServersRoutesSeparately(t *testing.T) {
	root := t.TempDir()
	a := NewAgent(nil, "", nil)
	a.pipeline = connectedPipelineMCP(t, root, filepath.Join(root, "first"))
	a.gitlab = connectedPipelineMCP(t, root, filepath.Join(root, "second"))
	definitions, routes, err := a.availableTools(context.Background())
	if err != nil || len(definitions) != 8 {
		t.Fatalf("definitions=%v err=%v", definitions, err)
	}
	for _, server := range []string{"mrkpipeline", "mrkgitlab"} {
		call := ToolCall{Function: ToolCallFunction{Name: "mcp__" + server + "__save_to_file", Arguments: `{"filename":"route.md","text":"saved"}`}}
		result := a.runToolCall(context.Background(), call, routes, nil)
		if strings.Contains(result, "Ошибка") {
			t.Fatal(result)
		}
	}
	for _, dir := range []string{"first", "second"} {
		if _, err := os.Stat(filepath.Join(root, dir, "route.md")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestToolCallBudgetStopsLargeBatch(t *testing.T) {
	model := &scriptedToolModel{}
	reply := Message{}
	for i := 0; i < maxToolCalls+1; i++ {
		reply.ToolCalls = append(reply.ToolCalls, ToolCall{ID: fmt.Sprint(i), Function: ToolCallFunction{Name: "missing", Arguments: `{}`}})
	}
	model.replies = []Message{reply}
	a := NewAgent(model, "", nil)
	a.gitlab = connectedGitLabMCP(t, nil)
	_, err := a.completeTurn(context.Background(), nil, nil, nil, a.complete)
	if err == nil || !strings.Contains(err.Error(), "предел вызовов") {
		t.Fatalf("err=%v", err)
	}
}

func TestErrorReturnsToModelBeforeSwitchingServers(t *testing.T) {
	root := t.TempDir()
	scheduler, err := NewScheduler(filepath.Join(root, "reminders.json"))
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedToolModel{replies: []Message{
		{ToolCalls: []ToolCall{{ID: "bad-save", Type: "function", Function: ToolCallFunction{Name: "mcp__mrkpipeline__save_to_file", Arguments: `{"filename":"../bad.md","text":"text"}`}}}},
		{ToolCalls: []ToolCall{{ID: "check", Type: "function", Function: ToolCallFunction{Name: "mcp__mrkscheduler__list_reminders", Arguments: `{}`}}}},
		{Content: "Сохранение не удалось; напоминаний нет"},
	}}
	a := NewAgent(model, "", nil)
	a.pipeline = connectedPipelineMCP(t, root, filepath.Join(root, "reports"))
	a.scheduler = connectedSchedulerMCP(t, scheduler)
	_, err = a.completeTurn(context.Background(), nil, nil, nil, a.complete)
	if err != nil {
		t.Fatal(err)
	}
	returned := model.seen[1][len(model.seen[1])-1]
	if returned.Role != "tool" || returned.ToolCallID != "bad-save" || !strings.Contains(returned.Content, "Ошибка инструмента") {
		t.Fatalf("error not returned: %+v", returned)
	}
	if len(scheduler.Summary().Jobs) != 0 {
		t.Fatal("unexpected reminder")
	}
	if _, err := os.Stat(filepath.Join(root, "reports")); !os.IsNotExist(err) {
		t.Fatalf("report created after error: %v", err)
	}
}

func TestToolSubcommandsComplete(t *testing.T) {
	for _, name := range []string{"search", "summarize", "save_to_file", "run_pipeline"} {
		value := "/mcp mrkpipeline call " + name
		options := commandCompletions(value[:len(value)-1])
		found := false
		for _, option := range options {
			found = found || option.Value == value
		}
		if !found {
			t.Fatalf("missing completion %s", value)
		}
	}
}
