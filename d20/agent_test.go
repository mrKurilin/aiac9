package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type captureClient struct {
	mu      sync.Mutex
	calls   [][]Message
	reply   string
	replies []string
	err     error
}

type scriptedStreamingClient struct{ chunks []string }

func (c *scriptedStreamingClient) Complete(context.Context, []Message) (string, error) {
	return strings.Join(c.chunks, ""), nil
}

func (c *scriptedStreamingClient) CompleteStream(_ context.Context, _ []Message, emit func(string) error) (string, error) {
	for _, chunk := range c.chunks {
		if err := emit(chunk); err != nil {
			return "", err
		}
	}
	return strings.Join(c.chunks, ""), nil
}

func (c *captureClient) Complete(_ context.Context, messages []Message) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, append([]Message(nil), messages...))
	if c.err != nil {
		return "", c.err
	}
	if len(c.replies) > 0 {
		reply := c.replies[0]
		c.replies = c.replies[1:]
		return reply, nil
	}
	if c.reply == "" {
		return `{"answer":"Готово"}`, nil
	}
	return c.reply, nil
}

func (c *captureClient) callSnapshot() [][]Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([][]Message, len(c.calls))
	for i := range c.calls {
		result[i] = append([]Message(nil), c.calls[i]...)
	}
	return result
}

func TestAgentRestoresGoalAndHistoryWithoutStages(t *testing.T) {
	path := t.TempDir() + "/task-state.json"
	first := NewAgent(&captureClient{reply: `{"answer":"Первый ответ"}`}, "system", NewJSONStateStore(path))
	if err := first.StartTask("Сделать сервис"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Ask(context.Background(), "Первый вопрос"); err != nil {
		t.Fatal(err)
	}
	if err := first.Pause("до завтра"); err != nil {
		t.Fatal(err)
	}
	secondClient := &captureClient{reply: `{"answer":"Продолжаю"}`}
	second := NewAgent(secondClient, "system", NewJSONStateStore(path))
	if second.PlanMode() || second.DisplayState() != nil {
		t.Fatal("loaded task unexpectedly enabled plan mode")
	}
	second.SetPlanMode(true)
	if err := second.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Ask(context.Background(), "Второй вопрос"); err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, message := range secondClient.callSnapshot()[0] {
		joined += message.Content
	}
	for _, want := range []string{`"goal":"Сделать сервис"`, "Первый вопрос", "Первый ответ", "Второй вопрос"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, `"stage"`) || strings.Contains(joined, "next_stage") {
		t.Fatalf("stage metadata remains: %s", joined)
	}
}

func TestAgentDiscoversAndClarifiesTask(t *testing.T) {
	client := &captureClient{replies: []string{
		`{"task_ready":false,"answer":"Какой результат нужен?","goal":""}`,
		`{"task_ready":true,"answer":"Задача понятна","goal":"CSV-конвертер"}`,
	}}
	agent := NewAgent(client, "", nil)
	agent.SetPlanMode(true)
	first, err := agent.Ask(context.Background(), "Нужна утилита")
	if err != nil || first.TaskCreated || agent.Snapshot() != nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := agent.Ask(context.Background(), "Конвертировать CSV в JSON")
	if err != nil || !second.TaskCreated || agent.Snapshot().Goal != "CSV-конвертер" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	joined := ""
	for _, message := range client.callSnapshot()[1] {
		joined += message.Content
	}
	if !strings.Contains(joined, "Какой результат нужен?") {
		t.Fatalf("clarification lost: %s", joined)
	}
}

func TestPlainChatDoesNotDiscoverOrSaveGoal(t *testing.T) {
	client := &captureClient{replies: []string{`{"answer":"Привет!"}`, `{"answer":"Мы говорили о приветствии."}`}}
	agent := NewAgent(client, "", nil)
	for _, prompt := range []string{"Привет", "О чём мы говорили?"} {
		result, err := agent.Ask(context.Background(), prompt)
		if err != nil || result.TaskCreated || agent.Snapshot() != nil {
			t.Fatalf("result=%+v err=%v state=%+v", result, err, agent.Snapshot())
		}
	}
	calls := client.callSnapshot()
	if len(calls) != 2 {
		t.Fatalf("model calls=%d", len(calls))
	}
	joined := ""
	for _, message := range calls[1] {
		joined += message.Content
	}
	if !strings.Contains(joined, "Привет!") || strings.Contains(joined, "task_ready") || strings.Contains(joined, "TASK_STATE") {
		t.Fatalf("plain chat prompt=%s", joined)
	}
}

func TestPlanModeSwitchControlsDiscoveryWithExistingState(t *testing.T) {
	client := &captureClient{replies: []string{
		`{"answer":"Обычный ответ"}`,
		`{"task_ready":true,"answer":"Задача понятна","goal":"Создать отчёт"}`,
		`{"answer":"Снова обычный ответ"}`,
	}}
	agent := NewAgent(client, "", nil)
	if _, err := agent.Ask(context.Background(), "Сделай отчёт"); err != nil {
		t.Fatal(err)
	}
	agent.SetPlanMode(true)
	created, err := agent.Ask(context.Background(), "Уточнение: отчёт за неделю")
	if err != nil || !created.TaskCreated || agent.Snapshot().Goal != "Создать отчёт" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	agent.SetPlanMode(false)
	if _, err := agent.Ask(context.Background(), "Привет"); err != nil {
		t.Fatal(err)
	}
	if agent.DisplayState() != nil || agent.Snapshot().Goal != "Создать отчёт" {
		t.Fatalf("state changed in plain chat: %+v", agent.Snapshot())
	}
	joined := ""
	for _, message := range client.callSnapshot()[2] {
		joined += message.Content
	}
	if strings.Contains(joined, "TASK_STATE") || strings.Contains(joined, "task_ready") {
		t.Fatalf("plan prompt leaked into plain chat: %s", joined)
	}
}

func TestPausedAgentRejectsQuestionsWithoutCallingModel(t *testing.T) {
	client := &captureClient{}
	agent := NewAgent(client, "", nil)
	if err := agent.StartTask("Пауза"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Pause("ожидание"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Ask(context.Background(), "Продолжай"); err == nil || !strings.Contains(err.Error(), "/resume") {
		t.Fatalf("err=%v", err)
	}
	if len(client.callSnapshot()) != 0 {
		t.Fatal("model called while paused")
	}
}

func TestFailedModelCallDoesNotChangeHistory(t *testing.T) {
	agent := NewAgent(&captureClient{err: errors.New("offline")}, "", nil)
	if err := agent.StartTask("Проверить откат"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Ask(context.Background(), "Не сохраняй"); err == nil {
		t.Fatal("expected error")
	}
	if got := len(agent.Snapshot().History); got != 0 {
		t.Fatalf("history=%d", got)
	}
}

func TestAgentStreamsOnlyDecodedAnswer(t *testing.T) {
	client := &scriptedStreamingClient{chunks: []string{`{"ans`, `wer":"Привет\nмир \u`, `263A"}`}}
	agent := NewAgent(client, "", nil)
	if err := agent.StartTask("Проверить поток"); err != nil {
		t.Fatal(err)
	}
	var chunks []string
	result, err := agent.AskStream(context.Background(), "Ответь", func(chunk string) error { chunks = append(chunks, chunk); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(chunks, ""); got != "Привет\nмир ☺" || result.Answer != got || len(chunks) < 2 {
		t.Fatalf("chunks=%q result=%+v", chunks, result)
	}
}

func TestAgentStreamsClarificationBeforeTaskCreation(t *testing.T) {
	client := &scriptedStreamingClient{chunks: []string{`{"task_ready":false,"ans`, `wer":"Какой результат`, ` нужен?","goal":""}`}}
	agent := NewAgent(client, "", nil)
	agent.SetPlanMode(true)
	var chunks []string
	result, err := agent.AskStream(context.Background(), "Нужна утилита", func(chunk string) error { chunks = append(chunks, chunk); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(chunks, ""); got != "Какой результат нужен?" || result.TaskCreated || agent.Snapshot() != nil {
		t.Fatalf("chunks=%q result=%+v", chunks, result)
	}
}

func TestAnswerStreamDecoderHandlesLargeResponseIncrementally(t *testing.T) {
	want := strings.Repeat("абвгд", 4000)
	raw := `{"answer":"` + want + `"}`
	decoder := &answerStreamDecoder{}
	var output strings.Builder
	emissions := 0
	for index := 0; index < len(raw); index++ {
		if err := decoder.feed(raw[index:index+1], func(chunk string) error { emissions++; output.WriteString(chunk); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if output.String() != want || emissions < 1000 {
		t.Fatalf("decoded bytes=%d emissions=%d", output.Len(), emissions)
	}
}
