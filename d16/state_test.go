package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskStateAllowsOnlyDeclaredTransitions(t *testing.T) {
	state, err := NewTaskState("Собрать сервис")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Transition(StageValidation, "", ""); err == nil || !strings.Contains(err.Error(), "запрещён") {
		t.Fatalf("planning -> validation accepted: %v", err)
	}
	if err := state.ApprovePlan(); err != nil {
		t.Fatal(err)
	}
	if err := state.Transition(StageExecution, "Реализовать API", "Предоставьте следующий пример"); err != nil {
		t.Fatal(err)
	}
	if state.Stage != StageExecution || state.CurrentStep != "Реализовать API" || state.ExpectedAction != "Предоставьте следующий пример" {
		t.Fatalf("unexpected state: %+v", state)
	}
	if err := state.Transition(StagePlanning, "", ""); err != nil {
		t.Fatal("execution -> planning must be allowed:", err)
	}
	if state.PlanApproved {
		t.Fatal("return to planning must invalidate approval")
	}
	if err := state.ApprovePlan(); err != nil {
		t.Fatal(err)
	}
	if err := state.Transition(StageExecution, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := state.Transition(StageValidation, "", ""); err == nil || !strings.Contains(err.Error(), "завершите реализацию") {
		t.Fatalf("execution -> validation without completed implementation accepted: %v", err)
	}
	if err := state.CompleteImplementation(); err != nil {
		t.Fatal(err)
	}
	if err := state.Transition(StageValidation, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := state.Transition(StageDone, "", ""); err == nil || !strings.Contains(err.Error(), "успешную валидацию") {
		t.Fatalf("validation -> done without successful validation accepted: %v", err)
	}
	if err := state.PassValidation(); err != nil {
		t.Fatal(err)
	}
	if err := state.Transition(StageDone, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := state.Transition(StageExecution, "", ""); err == nil {
		t.Fatal("transition from done accepted")
	}
}

func TestPauseAndResumeKeepExactState(t *testing.T) {
	state, _ := NewTaskState("Продолжить позже")
	_ = state.ApprovePlan()
	if err := state.Transition(StageExecution, "Шаг 2 из 4: токены", "Реализовать проверку срока"); err != nil {
		t.Fatal(err)
	}
	state.History = []Message{{Role: "user", Content: "Шаг 1 готов"}, {Role: "assistant", Content: "Перехожу к шагу 2"}}
	before := state
	if err := state.Pause("конец рабочего дня"); err != nil {
		t.Fatal(err)
	}
	if err := state.Resume(); err != nil {
		t.Fatal(err)
	}
	if state.Stage != before.Stage || state.CurrentStep != before.CurrentStep || state.ExpectedAction != before.ExpectedAction {
		t.Fatalf("resume changed state: before=%+v after=%+v", before, state)
	}
	if len(state.History) != 2 || state.History[0].Content != "Шаг 1 готов" {
		t.Fatalf("resume lost context: %+v", state.History)
	}
}

func TestPauseIsAvailableAtEveryStage(t *testing.T) {
	for _, stage := range []Stage{StagePlanning, StageExecution, StageValidation, StageDone} {
		t.Run(string(stage), func(t *testing.T) {
			state, _ := NewTaskState("Проверить паузу")
			state.Stage = stage
			state.CurrentStep, state.ExpectedAction = defaultsFor(stage)
			if err := state.Pause("проверка"); err != nil {
				t.Fatal(err)
			}
			if !state.Paused || state.Stage != stage {
				t.Fatalf("state=%+v", state)
			}
			if err := state.Resume(); err != nil {
				t.Fatal(err)
			}
			if state.Paused || state.Stage != stage {
				t.Fatalf("state=%+v", state)
			}
		})
	}
}

func TestJSONStoreRestoresStateAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "task-state.json")
	store := NewJSONStateStore(path)
	state, _ := NewTaskState("Пережить перезапуск")
	_ = state.ApprovePlan()
	_ = state.Transition(StageExecution, "Собрать модуль", "Запустить тесты")
	state.History = []Message{{Role: "user", Content: "План утверждён"}}
	_ = state.Pause("перезапуск")
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	restored, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if restored == nil || !restored.Paused || restored.Stage != StageExecution || !restored.PlanApproved || restored.CurrentStep != "Собрать модуль" || len(restored.History) != 1 || len(restored.Transitions) != 1 {
		t.Fatalf("restored=%+v", restored)
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if restored, err = store.Load(); err != nil || restored != nil {
		t.Fatalf("after delete: state=%+v err=%v", restored, err)
	}
}
