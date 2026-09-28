package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScheduledMergeRequestsFetchAndDisplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "opened" || r.Header.Get("Authorization") != "Bearer placeholder" {
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"project_id":1,"iid":2,"title":"Исправить отчёт","author":{"username":"dev"},"web_url":"https://gitlab.com/group/project/-/merge_requests/2","references":{"full":"group/project!2"}}]`)
	}))
	defer server.Close()
	api, err := NewGitLabAPI(server.URL, "placeholder", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/reminders.json"
	scheduler, err := NewScheduler(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	job, err := scheduler.AddMergeRequests(30, "group/project", now)
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err = NewScheduler(path)
	if err != nil {
		t.Fatal(err)
	}
	if scheduler.Summary().Jobs[0].Kind != mergeRequestsJobKind {
		t.Fatal("job kind was not restored")
	}
	if _, fired, err := runScheduledTick(context.Background(), scheduler, api, now.Add(29*time.Second)); err != nil || fired {
		t.Fatalf("fired early: %v %v", fired, err)
	}
	update, fired, err := runScheduledTick(context.Background(), scheduler, api, now.Add(30*time.Second))
	if err != nil || !fired || len(update.MRResults) != 1 || len(update.MRResults[0].MergeRequests) != 1 {
		t.Fatalf("update=%+v fired=%v err=%v", update, fired, err)
	}
	if update.MRResults[0].JobID != job.ID || update.Summary.TotalRuns != 1 {
		t.Fatalf("update=%+v", update)
	}
	var out bytes.Buffer
	printSchedulerUpdate(&out, update)
	if !strings.Contains(out.String(), "group/project!2") || !strings.Contains(out.String(), "Исправить отчёт") || !strings.Contains(out.String(), "Открытые MR: 1") || strings.Contains(out.String(), "Запусков всего") || strings.Contains(out.String(), "Планировщик: MR") {
		t.Fatalf("output=%s", out.String())
	}
}

func TestResetDuringScheduledFetchDropsOldUpdate(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	api, err := NewGitLabAPI(server.URL, "placeholder", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := NewScheduler(t.TempDir() + "/reminders.json")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := scheduler.AddMergeRequests(10, "group/project", now); err != nil {
		t.Fatal(err)
	}
	type tickResult struct {
		update SchedulerUpdate
		fired  bool
		err    error
	}
	done := make(chan tickResult, 1)
	go func() {
		update, fired, err := runScheduledTick(context.Background(), scheduler, api, now.Add(10*time.Second))
		done <- tickResult{update, fired, err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled fetch did not start")
	}
	if generation, err := scheduler.CancelAll(); err != nil || generation != 1 {
		t.Fatalf("generation=%d err=%v", generation, err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.fired || len(result.update.Events) != 0 {
			t.Fatalf("old update escaped reset: %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled fetch did not stop")
	}
}

func TestNaturalLanguageSchedulesMRThroughModelToolCall(t *testing.T) {
	scheduler, err := NewScheduler(t.TempDir() + "/reminders.json")
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewGitLabAPI(defaultGitLabAPIURL, "placeholder", nil)
	if err != nil {
		t.Fatal(err)
	}
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveSchedulerMCP(serverIn, serverOut, scheduler, time.Now, api)
		_ = serverOut.Close()
	}()
	defer func() { _ = clientOut.Close(); _ = clientIn.Close(); <-serverDone }()
	mcp := &MCPClient{stdin: clientOut, stdout: bufio.NewReader(clientIn)}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := mcp.call("initialize", map[string]any{}, &initialized); err != nil {
		t.Fatal(err)
	}
	if err := mcp.notify("notifications/initialized"); err != nil {
		t.Fatal(err)
	}
	model := &scriptedToolModel{replies: []Message{
		{ToolCalls: []ToolCall{{ID: "schedule-1", Type: "function", Function: ToolCallFunction{Name: "mcp__mrkscheduler__schedule_open_merge_requests", Arguments: `{"every_seconds":30}`}}}},
		{Content: `{"answer":"Готово, буду выводить открытые MR каждые 30 секунд"}`},
	}}
	agent := NewAgent(model, "", nil)
	agent.scheduler = mcp
	answer, err := agent.AskStreamWithTools(context.Background(), "Выводи открытые МРы каждые 30 секунд", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer.Answer, "каждые 30 секунд") || len(model.seen) != 2 || model.seen[1][3].ToolCallID != "schedule-1" {
		t.Fatalf("answer=%+v model messages=%+v", answer, model.seen)
	}
	jobs := scheduler.Summary().Jobs
	if len(jobs) != 1 || jobs[0].Kind != mergeRequestsJobKind || jobs[0].EverySeconds != 30 {
		t.Fatalf("jobs=%+v", jobs)
	}
}

func TestResetStopsPersistentSchedule(t *testing.T) {
	path := t.TempDir() + "/reminders.json"
	scheduler, err := NewScheduler(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Add("Отчёт", 1, 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.AddMergeRequests(30, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveSchedulerMCP(serverIn, serverOut, scheduler, time.Now)
		_ = serverOut.Close()
	}()
	defer func() { _ = clientOut.Close(); _ = clientIn.Close(); <-serverDone }()
	mcp := &MCPClient{stdin: clientOut, stdout: bufio.NewReader(clientIn)}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := mcp.call("initialize", map[string]any{}, &initialized); err != nil {
		t.Fatal(err)
	}
	if err := mcp.notify("notifications/initialized"); err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(&captureClient{}, "", nil)
	agent.scheduler = mcp
	if err := agent.Reset(); err != nil {
		t.Fatal(err)
	}
	if agent.SchedulerGeneration() != 1 || activeJobs(scheduler.Summary().Jobs) != 0 {
		t.Fatalf("schedule still active: %+v", scheduler.Summary())
	}
	if shouldShowSchedulerUpdate(agent, SchedulerUpdate{Generation: 0, Summary: scheduler.Summary()}) {
		t.Fatal("queued update from before reset would still be shown")
	}
	restored, err := NewScheduler(path)
	if err != nil {
		t.Fatal(err)
	}
	if activeJobs(restored.Summary().Jobs) != 0 {
		t.Fatal("schedule restarted after reset")
	}
}

func TestScheduleMRRequiresToken(t *testing.T) {
	scheduler, err := NewScheduler(t.TempDir() + "/reminders.json")
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"schedule_open_merge_requests","arguments":{"every_seconds":30}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := serveSchedulerMCP(strings.NewReader(input), &out, scheduler, time.Now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "GITLAB_API_TOKEN") || len(scheduler.Summary().Jobs) != 0 {
		t.Fatalf("output=%s", out.String())
	}
}

func TestMRProjectValidation(t *testing.T) {
	scheduler, err := NewScheduler(t.TempDir() + "/reminders.json")
	if err != nil {
		t.Fatal(err)
	}
	first, err := scheduler.AddMergeRequests(30, "group/project", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := scheduler.AddMergeRequests(30, "group/project", time.Now())
	if err != nil || second.ID != first.ID || len(scheduler.Summary().Jobs) != 1 {
		t.Fatalf("duplicate schedule: first=%+v second=%+v err=%v", first, second, err)
	}
	if _, err := scheduler.AddMergeRequests(30, "https://example.com", time.Now()); err == nil {
		t.Fatal("URL should be rejected")
	}
	if _, err := scheduler.AddMergeRequests(5, "", time.Now()); err == nil {
		t.Fatal("short period should be rejected")
	}
}
