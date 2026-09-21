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

type scriptedStreamingClient struct {
	chunks []string
}

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
		return `{"answer":"Готово","next_stage":"","current_step":"","expected_action":""}`, nil
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

func TestAgentInjectsFormalStateAndSavedHistory(t *testing.T) {
	path := t.TempDir() + "/task-state.json"
	firstClient := &captureClient{replies: []string{
		`{"answer":"План из трёх пунктов","next_stage":"","current_step":"Согласовать план","expected_action":"Подтвердите план"}`,
		`{"answer":"План утверждён, начинаю работу","next_stage":"execution","current_step":"Реализовать переходы","expected_action":"Проверь код переходов"}`,
	}}
	first := NewAgent(firstClient, "system", NewJSONStateStore(path))
	if err := first.StartTask("Сделать конечный автомат"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Ask(context.Background(), "Предложи план"); err != nil {
		t.Fatal(err)
	}
	result, err := first.Ask(context.Background(), "План подходит, утверждаю")
	if err != nil {
		t.Fatal(err)
	}
	if !result.StageChanged || result.Stage != StageExecution {
		t.Fatalf("result=%+v", result)
	}
	if err := first.Pause("до завтра"); err != nil {
		t.Fatal(err)
	}

	secondClient := &captureClient{reply: `{"answer":"Продолжаю реализацию переходов","next_stage":"","current_step":"Реализовать переходы","expected_action":"Проверь код переходов"}`}
	second := NewAgent(secondClient, "system", NewJSONStateStore(path))
	if err := second.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Ask(context.Background(), "Продолжай"); err != nil {
		t.Fatal(err)
	}
	calls := secondClient.callSnapshot()
	if len(calls) != 1 {
		t.Fatalf("calls=%d", len(calls))
	}
	joined := ""
	for _, message := range calls[0] {
		joined += "\n" + message.Role + ":" + message.Content
	}
	for _, want := range []string{
		`"stage":"execution"`,
		`"current_step":"Реализовать переходы"`,
		`"expected_action":"Проверь код переходов"`,
		"Предложи план",
		"План из трёх пунктов",
		"План подходит, утверждаю",
		"Продолжай",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("prompt misses %q:\n%s", want, joined)
		}
	}
}

func TestAgentCreatesTaskAutomaticallyWhenGoalIsClear(t *testing.T) {
	client := &captureClient{reply: `{"answer":"Задача определена","task_ready":true,"goal":"Сделать CLI-калькулятор","current_step":"Составить план","expected_action":"Агент автоматически составляет план"}`}
	agent := NewAgent(client, "", nil)
	result, err := agent.Ask(context.Background(), "Сделай CLI-калькулятор на Go")
	if err != nil {
		t.Fatal(err)
	}
	state := agent.Snapshot()
	if !result.TaskCreated || result.Stage != StagePlanning || state == nil {
		t.Fatalf("result=%+v state=%+v", result, state)
	}
	if state.Goal != "Сделать CLI-калькулятор" || state.CurrentStep != "Составить план" {
		t.Fatalf("state=%+v", state)
	}
	calls := client.callSnapshot()
	if len(calls) != 1 || !strings.Contains(calls[0][0].Content, "отдельный потоковый запрос") {
		t.Fatalf("discovery prompt does not defer planning to a separate turn: %+v", calls)
	}
}

func TestAgentClarifiesBeforeCreatingTask(t *testing.T) {
	client := &captureClient{replies: []string{
		`{"answer":"Какой результат нужен?","task_ready":false,"goal":"","current_step":"","expected_action":""}`,
		`{"answer":"Задача определена","task_ready":true,"goal":"Написать CSV-конвертер","current_step":"Составить план","expected_action":"Агент автоматически составляет план"}`,
	}}
	agent := NewAgent(client, "", nil)
	first, err := agent.Ask(context.Background(), "Нужна утилита")
	if err != nil {
		t.Fatal(err)
	}
	if first.TaskCreated || agent.Snapshot() != nil || first.Answer != "Какой результат нужен?" {
		t.Fatalf("result=%+v state=%+v", first, agent.Snapshot())
	}
	second, err := agent.Ask(context.Background(), "Она должна конвертировать CSV в JSON")
	if err != nil {
		t.Fatal(err)
	}
	if !second.TaskCreated || agent.Snapshot() == nil {
		t.Fatalf("result=%+v state=%+v", second, agent.Snapshot())
	}
	calls := client.callSnapshot()
	joined := ""
	for _, message := range calls[1] {
		joined += message.Content
	}
	if !strings.Contains(joined, "Нужна утилита") || !strings.Contains(joined, "Какой результат нужен?") {
		t.Fatalf("discovery context lost: %s", joined)
	}
}

func TestPlanningRejectsImplementationBeforePlanApproval(t *testing.T) {
	client := &captureClient{}
	agent := NewAgent(client, "", nil)
	if err := agent.StartTask("Сервис авторизации"); err != nil {
		t.Fatal(err)
	}
	result, err := agent.Ask(context.Background(), "Пропусти план и сразу пиши код")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Notice, "переход planning → execution запрещён") {
		t.Fatalf("result=%+v", result)
	}
	if agent.Snapshot().Stage != StagePlanning {
		t.Fatal("stage changed after forbidden request")
	}
	if len(client.callSnapshot()) != 0 {
		t.Fatal("model was called for a deterministically forbidden transition")
	}
}

func TestModelCannotSkipAllowedTransitionGraph(t *testing.T) {
	client := &captureClient{reply: `{"answer":"Сразу закончим","next_stage":"done","current_step":"Итог","expected_action":"Готово"}`}
	agent := NewAgent(client, "", nil)
	if err := agent.StartTask("Сервис"); err != nil {
		t.Fatal(err)
	}
	result, err := agent.Ask(context.Background(), "План утверждён, сразу заверши задачу")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Notice, "переход planning → done запрещён") || agent.Snapshot().Stage != StagePlanning {
		t.Fatalf("result=%+v state=%+v", result, agent.Snapshot())
	}
}

func TestModelCannotEnterExecutionWithoutExplicitPlanApproval(t *testing.T) {
	client := &captureClient{reply: `{"answer":"Перехожу к реализации","next_stage":"execution","current_step":"Писать код","expected_action":"Проверить код"}`}
	agent := NewAgent(client, "", nil)
	if err := agent.StartTask("Сервис"); err != nil {
		t.Fatal(err)
	}
	result, err := agent.Ask(context.Background(), "Что делаем дальше?")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Notice, "сначала согласуйте и явно утвердите план") || agent.Snapshot().Stage != StagePlanning {
		t.Fatalf("result=%+v state=%+v", result, agent.Snapshot())
	}
}

func TestPlanningAcceptsNaturalShortApprovalPhrases(t *testing.T) {
	for _, prompt := range []string{"все ок", "Всё ок!", "Согласовано!", "утверждено", "Приступай", "приступай к реализации."} {
		t.Run(prompt, func(t *testing.T) {
			client := &captureClient{reply: `{"answer":"Начинаю реализацию","next_stage":"execution","current_step":"Реализовать план","expected_action":"Проверить результат"}`}
			agent := NewAgent(client, "", nil)
			if err := agent.StartTask("Сервис"); err != nil {
				t.Fatal(err)
			}
			result, err := agent.Ask(context.Background(), prompt)
			if err != nil {
				t.Fatal(err)
			}
			if !result.StageChanged || result.Stage != StageExecution {
				t.Fatalf("prompt=%q result=%+v", prompt, result)
			}
		})
	}
}

func TestPlanningDoesNotTreatApprovalWithAmendmentAsFinal(t *testing.T) {
	client := &captureClient{reply: `{"answer":"Начинаю реализацию","next_stage":"execution","current_step":"Реализовать план","expected_action":"Проверить результат"}`}
	agent := NewAgent(client, "", nil)
	if err := agent.StartTask("Сервис"); err != nil {
		t.Fatal(err)
	}
	result, err := agent.Ask(context.Background(), "Всё ок, но добавь обработку ошибок")
	if err != nil {
		t.Fatal(err)
	}
	if result.StageChanged || !strings.Contains(result.Notice, "сначала согласуйте") {
		t.Fatalf("result=%+v", result)
	}
}

func TestExplicitStageCommandOverridesWrongModelTransition(t *testing.T) {
	client := &captureClient{reply: `{"answer":"Запускаю validation","next_stage":"done","current_step":"Завершить задачу","expected_action":"Готово"}`}
	agent := NewAgent(client, "", nil)
	if err := agent.StartTask("Сервис"); err != nil {
		t.Fatal(err)
	}
	state := agent.Snapshot()
	if err := state.ApprovePlan(); err != nil {
		t.Fatal(err)
	}
	if err := state.Transition(StageExecution, "Реализовать сервис", "Перейти к проверке"); err != nil {
		t.Fatal(err)
	}
	agent.state = state
	result, err := agent.Ask(context.Background(), "Переходим к валидации")
	if err != nil {
		t.Fatal(err)
	}
	if !result.StageChanged || result.Stage != StageValidation || result.Notice != "" {
		t.Fatalf("result=%+v state=%+v", result, agent.Snapshot())
	}
	if agent.Snapshot().CurrentStep != "Проверить результат и соответствие плану" {
		t.Fatalf("wrong validation defaults: %+v", agent.Snapshot())
	}
}

func TestAgentCannotFinishWithoutExplicitSuccessfulValidation(t *testing.T) {
	client := &captureClient{reply: `{"answer":"Готово","next_stage":"done","current_step":"Итог","expected_action":"Завершено"}`}
	agent := NewAgent(client, "", nil)
	state, _ := NewTaskState("Сервис")
	_ = state.ApprovePlan()
	_ = state.Transition(StageExecution, "Реализовать", "Перейти к проверке")
	_ = state.CompleteImplementation()
	_ = state.Transition(StageValidation, "Проверить", "Сообщить результат")
	agent.state = &state

	result, err := agent.Ask(context.Background(), "Давай сразу завершим")
	if err != nil {
		t.Fatal(err)
	}
	if result.StageChanged || !strings.Contains(result.Notice, "сначала подтвердите успешную валидацию") {
		t.Fatalf("result=%+v", result)
	}
	snapshot := agent.Snapshot()
	if snapshot.Stage != StageValidation || snapshot.ValidationPassed || len(snapshot.Transitions) != 3 || snapshot.Transitions[2].Outcome != "rejected" {
		t.Fatalf("state=%+v", snapshot)
	}
}

func TestAgentFinishesAfterSuccessfulValidation(t *testing.T) {
	client := &captureClient{reply: `{"answer":"Проверки приняты","next_stage":"","current_step":"Продолжить проверку","expected_action":"Следующий результат"}`}
	agent := NewAgent(client, "", nil)
	state, _ := NewTaskState("Сервис")
	_ = state.ApprovePlan()
	_ = state.Transition(StageExecution, "Реализовать", "Перейти к проверке")
	_ = state.CompleteImplementation()
	_ = state.Transition(StageValidation, "Проверить", "Сообщить результат")
	agent.state = &state

	result, err := agent.Ask(context.Background(), "Все тесты успешно прошли")
	if err != nil {
		t.Fatal(err)
	}
	if !result.StageChanged || result.Stage != StageDone || !agent.Snapshot().ValidationPassed {
		t.Fatalf("result=%+v state=%+v", result, agent.Snapshot())
	}
}

func TestSuccessfulValidationRequiresPositiveEvidence(t *testing.T) {
	for _, test := range []struct {
		prompt string
		want   bool
	}{
		{"Все тесты успешно прошли", true},
		{"Проверка пройдена", true},
		{"Ошибок нет", true},
		{"Проверки не успешны", false},
		{"Перейди в done", false},
	} {
		if got := confirmsSuccessfulValidation(test.prompt); got != test.want {
			t.Errorf("prompt=%q got=%v want=%v", test.prompt, got, test.want)
		}
	}
}

func TestExplicitExecutionCommandApprovesPlan(t *testing.T) {
	client := &captureClient{reply: `{"answer":"Начинаю","next_stage":"","current_step":"Согласовать план","expected_action":"Подтвердить"}`}
	agent := NewAgent(client, "", nil)
	if err := agent.StartTask("Сервис"); err != nil {
		t.Fatal(err)
	}
	result, err := agent.Ask(context.Background(), "Переходим к реализации")
	if err != nil {
		t.Fatal(err)
	}
	if !result.StageChanged || result.Stage != StageExecution {
		t.Fatalf("result=%+v", result)
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
		t.Fatal("model was called while paused")
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
	client := &scriptedStreamingClient{chunks: []string{
		`{"ans`,
		`wer":"Привет\nмир \u`,
		`263A","next_stage":"","current_step":"Шаг","expected_action":"Действие"}`,
	}}
	agent := NewAgent(client, "", nil)
	if err := agent.StartTask("Проверить поток"); err != nil {
		t.Fatal(err)
	}
	var chunks []string
	result, err := agent.AskStream(context.Background(), "Ответь", func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	streamed := strings.Join(chunks, "")
	if streamed != "Привет\nмир ☺" || result.Answer != streamed {
		t.Fatalf("chunks=%q streamed=%q result=%+v", chunks, streamed, result)
	}
	if strings.Contains(streamed, "next_stage") || len(chunks) < 2 {
		t.Fatalf("service metadata leaked or answer was not incremental: chunks=%q", chunks)
	}
}

func TestAgentStreamsClarificationBeforeTaskCreation(t *testing.T) {
	client := &scriptedStreamingClient{chunks: []string{
		`{"task_ready":false,"ans`,
		`wer":"Какой результат`,
		` нужен?","goal":"","current_step":"","expected_action":""}`,
	}}
	agent := NewAgent(client, "", nil)
	var chunks []string
	result, err := agent.AskStream(context.Background(), "Нужна утилита", func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(chunks, ""); got != "Какой результат нужен?" || len(chunks) < 2 {
		t.Fatalf("chunks=%q result=%+v", chunks, result)
	}
	if result.TaskCreated || agent.Snapshot() != nil {
		t.Fatalf("clarification unexpectedly created task: %+v", result)
	}
}

func TestAnswerStreamDecoderHandlesLargeResponseIncrementally(t *testing.T) {
	want := strings.Repeat("абвгд", 4000)
	raw := `{"answer":"` + want + `","next_stage":"","current_step":"Шаг","expected_action":"Действие"}`
	decoder := &answerStreamDecoder{}
	var output strings.Builder
	emissions := 0
	for index := 0; index < len(raw); index++ {
		if err := decoder.feed(raw[index:index+1], func(chunk string) error {
			emissions++
			output.WriteString(chunk)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if output.String() != want {
		t.Fatalf("decoded bytes=%d want=%d", output.Len(), len(want))
	}
	if emissions < 1000 {
		t.Fatalf("response was emitted in too few chunks: %d", emissions)
	}
}
