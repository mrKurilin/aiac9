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
		`{"answer":"Задача понятна, начинаю планирование","task_ready":true,"goal":"Реализовать сервис","current_step":"Собрать требования","expected_action":"Опишите ограничения"}`,
		`{"answer":"План готов","next_stage":"","current_step":"Согласовать план","expected_action":"Подтвердите план"}`,
		`{"answer":"Начинаю реализацию","next_stage":"execution","current_step":"Написать обработчик","expected_action":"Покажите результат запуска"}`,
		`{"answer":"Реализация готова","next_stage":"validation","current_step":"Проверить обработчик","expected_action":"Сообщите результаты проверки"}`,
		`{"answer":"Проверка успешна","next_stage":"done","current_step":"Зафиксировать итог","expected_action":"Задача завершена"}`,
	}}
	agent := NewAgent(client, "", nil)
	in := strings.NewReader(strings.Join([]string{
		"Реализовать сервис",
		"План подходит, утверждаю",
		"/pause обед",
		"/state",
		"/resume",
		"Все проверки успешны",
		"/exit",
	}, "\n") + "\n")
	var out bytes.Buffer
	if err := runTerminal(context.Background(), agent, in, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"Состояние: нет задачи  ·  Этап: —",
		"→ Реализовать сервис",
		"Задача создана автоматически · этап: planning",
		"◆ План готов",
		"Этап изменён автоматически: execution",
		`"paused": true`,
		"Продолжаю без повторного сбора контекста",
		"Этап изменён автоматически: validation",
		"Этап изменён автоматически: done",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output misses %q:\n%s", want, text)
		}
	}
	calls := client.callSnapshot()
	if len(calls) != 5 {
		t.Fatalf("model calls=%d", len(calls))
	}
	if got := calls[1][len(calls[1])-1].Content; got != automaticPlanningPrompt {
		t.Fatalf("automatic planning prompt=%q", got)
	}
	if got := calls[3][len(calls[3])-1].Content; got != automaticExecutionPrompt {
		t.Fatalf("automatic execution prompt=%q", got)
	}
	created := strings.Index(text, "Задача создана автоматически · этап: planning")
	planned := strings.Index(text, "◆ План готов")
	if created < 0 || planned < created {
		t.Fatalf("plan must start after task creation: %s", text)
	}
}

func TestTerminalReportsForbiddenCodeRequestDuringPlanning(t *testing.T) {
	agent := NewAgent(&captureClient{}, "", nil)
	if err := agent.StartTask("test"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := handleTerminalLine(context.Background(), agent, "Пропусти план и напиши код", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "переход planning → execution запрещён") {
		t.Fatalf("output=%s", out.String())
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

func TestUserTranscriptKeepsMultilineMessageReadable(t *testing.T) {
	if got := userTranscript("первая\nвторая"); got != "\n› первая\n  вторая" {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalPrintsStreamAsSeparateAnswerBlock(t *testing.T) {
	agent := NewAgent(&scriptedStreamingClient{chunks: []string{
		`{"answer":"Поток`,
		`овый ответ","next_stage":"","current_step":"Шаг","expected_action":"Действие"}`,
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
		return `{"answer":"Ответ","next_stage":"","current_step":"Обсудить задачу","expected_action":"Следующее сообщение"}`, nil
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
