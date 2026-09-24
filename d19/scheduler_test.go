package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type safeOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *safeOutput) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(data)
}

func (o *safeOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func TestScheduledOutputAppearsAfterAgentAnswer(t *testing.T) {
	client := &blockingClient{started: make(chan struct{}, 1), release: make(chan struct{}, 1)}
	agent := NewAgent(client, "", nil)
	if err := agent.StartTask("Проверить порядок вывода"); err != nil {
		t.Fatal(err)
	}
	updates := make(chan SchedulerUpdate, 1)
	agent.scheduler = &MCPClient{updates: updates}
	reader, writer := io.Pipe()
	var output safeOutput
	done := make(chan error, 1)
	go func() { done <- runTerminal(context.Background(), agent, reader, &output) }()
	_, _ = fmt.Fprintln(writer, "Ответь")
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not start")
	}
	updates <- SchedulerUpdate{Events: []ScheduledEvent{{JobID: "R1", Text: "Отчёт"}}, Summary: SchedulerData{TotalRuns: 1, Jobs: []ScheduledJob{{Active: true}}, Events: []ScheduledEvent{{JobID: "R1"}}}}
	if strings.Contains(output.String(), "Напоминание: Отчёт") {
		t.Fatal("scheduled output interrupted the agent")
	}
	client.release <- struct{}{}
	_, _ = fmt.Fprintln(writer, "/exit")
	_ = writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal did not stop")
	}
	shown := output.String()
	answer := strings.Index(shown, "◆ Ответ")
	reminder := strings.Index(shown, "Напоминание: Отчёт")
	if answer < 0 || reminder < answer || strings.Contains(shown, "Запусков всего") {
		t.Fatalf("wrong output order: %s", shown)
	}
}

func TestScheduledMRSummarySkipsOnlyConsecutiveDuplicate(t *testing.T) {
	var out bytes.Buffer
	var gate summaryGate
	first := SchedulerUpdate{MRResults: []ScheduledMRResult{{JobID: "R1", MergeRequests: []MergeRequestSummary{{Reference: "project!1", Title: "Исправление", Author: "alice"}}}}}
	other := SchedulerUpdate{MRResults: []ScheduledMRResult{{JobID: "R1", MergeRequests: []MergeRequestSummary{{Reference: "project!2", Title: "Новая задача", Author: "bob"}}}}}
	gate.printUpdate(&out, first)
	gate.printUpdate(&out, first)
	if got := strings.Count(out.String(), "Открытые MR: 1"); got != 1 {
		t.Fatalf("consecutive summary repeated %d times: %q", got, out.String())
	}
	gate.printUpdate(&out, other)
	gate.printUpdate(&out, first)
	if got := strings.Count(out.String(), "project!1"); got != 2 {
		t.Fatalf("summary after different summary missing: %q", out.String())
	}
	gate.otherMessage()
	gate.printUpdate(&out, first)
	if got := strings.Count(out.String(), "project!1"); got != 3 {
		t.Fatalf("summary after another message missing: %q", out.String())
	}
	gate.printUpdate(&out, first)
	gate.printUpdate(&out, SchedulerUpdate{Events: []ScheduledEvent{{JobID: "R2", Text: "Напоминание"}}})
	gate.printUpdate(&out, first)
	if got := strings.Count(out.String(), "project!1"); got != 4 || !strings.Contains(out.String(), "Напоминание: Напоминание") {
		t.Fatalf("summary after reminder missing: %q", out.String())
	}
}

func TestSchedulerPersistsAndAggregates(t *testing.T) {
	path := t.TempDir() + "/reminders.json"
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, err := NewScheduler(path)
	if err != nil {
		t.Fatal(err)
	}
	one, err := s.Add("разовое", 2, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	periodic, err := s.Add("периодическое", 1, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if fired, err := s.Tick(now); err != nil || len(fired) != 0 {
		t.Fatalf("early=%v %v", fired, err)
	}
	s, err = NewScheduler(path)
	if err != nil {
		t.Fatal(err)
	}
	fired, err := s.Tick(now.Add(2 * time.Second))
	if err != nil || len(fired) != 2 {
		t.Fatalf("due=%v %v", fired, err)
	}
	summary := s.Summary()
	if summary.TotalRuns != 2 || len(summary.Events) != 2 || summary.Jobs[0].Active || !summary.Jobs[1].Active {
		t.Fatalf("summary=%+v", summary)
	}
	if summary.Jobs[0].ID != one.ID || summary.Jobs[1].ID != periodic.ID {
		t.Fatal("IDs changed")
	}
	s, err = NewScheduler(path)
	if err != nil {
		t.Fatal(err)
	}
	fired, err = s.Tick(now.Add(time.Hour))
	if err != nil || len(fired) != 1 {
		t.Fatalf("restart=%v %v", fired, err)
	}
	if s.Summary().TotalRuns != 3 {
		t.Fatal("aggregate lost")
	}
	if err = s.Cancel(periodic.ID); err != nil {
		t.Fatal(err)
	}
	fired, err = s.Tick(now.Add(2 * time.Hour))
	if err != nil || len(fired) != 0 {
		t.Fatalf("cancelled fired: %v", fired)
	}
}

func TestTickSnapshotKeepsGenerationAcrossReset(t *testing.T) {
	s, err := NewScheduler(t.TempDir() + "/reminders.json")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.Add("Напоминание", 1, 0, now); err != nil {
		t.Fatal(err)
	}
	events, snapshot, err := s.tickWithSnapshot(now.Add(time.Second))
	if err != nil || len(events) != 1 || snapshot.Generation != 0 {
		t.Fatalf("events=%v snapshot=%+v err=%v", events, snapshot, err)
	}
	newGeneration, err := s.CancelAll()
	if err != nil || newGeneration != 1 || snapshot.Generation == newGeneration {
		t.Fatalf("snapshot generation=%d new generation=%d err=%v", snapshot.Generation, newGeneration, err)
	}
}

func TestSchedulerRejectsInvalidInputWithoutMutation(t *testing.T) {
	s, err := NewScheduler(t.TempDir() + "/schedule.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		text         string
		delay, every int64
	}{{"", 1, 0}, {"x", 0, 0}, {"x", 1, 5}, {"x", 1, -1}} {
		if _, err := s.Add(input.text, input.delay, input.every, time.Now()); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
	if len(s.Summary().Jobs) != 0 {
		t.Fatal("invalid jobs saved")
	}
}

func TestSchedulerMCPProtocol(t *testing.T) {
	s, err := NewScheduler(t.TempDir() + "/schedule.json")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"schedule_reminder","arguments":{"text":"проверка","delay_seconds":1,"every_seconds":10}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"reminder_summary","arguments":{}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := serveSchedulerMCP(strings.NewReader(input), &out, s, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("responses=%s", out.String())
	}
	var response mcpMessage
	if err := json.Unmarshal([]byte(lines[1]), &response); err != nil {
		t.Fatal(err)
	}
	var tools struct {
		Tools []MCPTool `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &tools); err != nil || len(tools.Tools) != 6 {
		t.Fatalf("tools=%+v %v", tools, err)
	}
	if s.Summary().Jobs[0].Text != "проверка" {
		t.Fatal("tool did not schedule")
	}
	if !strings.Contains(lines[3], "total_runs") {
		t.Fatal("summary not returned")
	}
}
