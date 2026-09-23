package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestTerminalTaskWorkflow(t *testing.T) {
	client := &captureClient{replies: []string{
		`{"answer":"Задача понятна","task_ready":true,"goal":"Реализовать сервис"}`,
		`{"answer":"Продолжаю работу"}`,
	}}
	agent := NewAgent(client, "", nil)
	in := strings.NewReader("/plan-mode enable\nРеализовать сервис\n/pause обед\n/state\n/resume\nПродолжай\n/exit\n")
	var out bytes.Buffer
	if err := runTerminal(context.Background(), agent, in, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Состояние: нет задачи", `"paused": true`, "Продолжаю без повторного сбора контекста", "Продолжаю работу"} {
		if !strings.Contains(text, want) {
			t.Errorf("output misses %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Этап:") || strings.Contains(text, "planning") {
		t.Fatalf("stage shown: %s", text)
	}
	if calls := client.callSnapshot(); len(calls) != 2 {
		t.Fatalf("model calls=%d", len(calls))
	}
}

type clearableBuffer struct {
	bytes.Buffer
	cleared int
}

func (b *clearableBuffer) clearChat() error {
	b.Buffer.Reset()
	b.cleared++
	return nil
}

func TestResetClearsInteractiveChat(t *testing.T) {
	agent := NewAgent(&captureClient{}, "", nil)
	if err := agent.StartTask("Старая задача"); err != nil {
		t.Fatal(err)
	}
	out := &clearableBuffer{}
	out.WriteString("старый диалог")
	handled, exit := runCommand(agent, "/reset", out)
	if !handled || exit || out.cleared != 1 {
		t.Fatalf("handled=%v exit=%v cleared=%d", handled, exit, out.cleared)
	}
	if strings.Contains(out.String(), "старый диалог") || !strings.Contains(out.String(), "Чат, задача и сохранённый контекст удалены.") {
		t.Fatalf("output=%q", out.String())
	}
	if agent.Snapshot() != nil {
		t.Fatal("task state was not reset")
	}
}

func TestPlanModeCommandSwitchesChat(t *testing.T) {
	agent := NewAgent(&captureClient{}, "", nil)
	var out bytes.Buffer
	for _, step := range []struct {
		command string
		enabled bool
	}{
		{"/plan-mode enable", true},
		{"/plan-mode disable", false},
	} {
		handled, exit := runCommand(agent, step.command, &out)
		if !handled || exit || agent.PlanMode() != step.enabled {
			t.Fatalf("command=%q enabled=%v", step.command, agent.PlanMode())
		}
	}
	if !strings.Contains(out.String(), "Режим планирования включён") || !strings.Contains(out.String(), "Обычный чат включён") {
		t.Fatalf("output=%q", out.String())
	}
}

func TestLoadedTaskStartsInPlainChat(t *testing.T) {
	store := NewJSONStateStore(t.TempDir() + "/task-state.json")
	if err := store.Save(TaskState{Goal: "Старая цель"}); err != nil {
		t.Fatal(err)
	}
	client := &captureClient{reply: `{"answer":"Привет!"}`}
	agent := NewAgent(client, "", store)
	var out bytes.Buffer
	if err := runTerminal(context.Background(), agent, strings.NewReader("Привет\n/exit\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "◆ Привет!") || strings.Contains(out.String(), "Загружена сохранённая задача") {
		t.Fatalf("output=%q", out.String())
	}
	joined := ""
	for _, message := range client.callSnapshot()[0] {
		joined += message.Content
	}
	if strings.Contains(joined, "Старая цель") || strings.Contains(joined, "task_ready") {
		t.Fatalf("saved goal leaked into chat prompt: %s", joined)
	}
}

func TestUserTranscriptKeepsMultilineMessageReadable(t *testing.T) {
	if got := userTranscript("первая\nвторая"); got != "\n› первая\n  вторая" {
		t.Fatalf("got %q", got)
	}
}

func TestDoubleInterruptWindowRequiresSameKey(t *testing.T) {
	first := time.Unix(100, 0)
	ctrlC := terminalInterrupt{key: "Ctrl+C"}
	escape := terminalInterrupt{key: "Esc"}
	if isDoubleInterrupt("", time.Time{}, ctrlC, first) || !isDoubleInterrupt("Ctrl+C", first, ctrlC, first.Add(500*time.Millisecond)) || !isDoubleInterrupt("Esc", first, escape, first.Add(500*time.Millisecond)) || isDoubleInterrupt("Ctrl+C", first, escape, first.Add(500*time.Millisecond)) || isDoubleInterrupt("Ctrl+C", first, ctrlC, first.Add(2*time.Second)) {
		t.Fatal("unexpected double interrupt detection")
	}
}

func TestTerminalPrintsStreamAsSeparateAnswerBlock(t *testing.T) {
	agent := NewAgent(&scriptedStreamingClient{chunks: []string{
		`{"answer":"Поток`,
		`овый ответ"}`,
	}}, "", nil)
	if err := agent.StartTask("Проверить интерфейс"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := handleTerminalLine(context.Background(), agent, "Ответь", &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "\n◆ Потоковый ответ\n\n" {
		t.Fatalf("unexpected answer block: %q", got)
	}
}

type blockingClient struct {
	started chan struct{}
	release chan struct{}
}

func (c *blockingClient) Complete(ctx context.Context, _ []Message) (string, error) {
	select {
	case c.started <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case <-c.release:
		return `{"answer":"Ответ"}`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestTerminalQueuesMessagesWhileModelIsThinking(t *testing.T) {
	client := &blockingClient{started: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	agent := NewAgent(client, "", nil)
	if err := agent.StartTask("Очередь сообщений"); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runTerminal(context.Background(), agent, reader, &out) }()
	_, _ = fmt.Fprintln(writer, "Первое сообщение")
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not start")
	}
	_, _ = fmt.Fprintln(writer, "Второе сообщение")
	client.release <- struct{}{}
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("queued request did not start")
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
	if strings.Count(out.String(), "◆ Ответ") != 2 {
		t.Fatalf("queued messages were not processed: %s", out.String())
	}
}
