package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvariantStoreIsIndependentFromDialogueAndReset(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "task-state.json")
	invariantPath := filepath.Join(dir, "invariants.json")
	agent := NewAgent(
		&captureClient{},
		"",
		NewJSONStateStore(statePath),
		NewJSONInvariantStore(invariantPath),
	)
	item, err := agent.AddInvariant("стек", "Использовать только Go")
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "INV-001" {
		t.Fatalf("id=%q", item.ID)
	}
	if err := agent.StartTask("Сделать сервис"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("task state still exists: %v", err)
	}
	if _, err := os.Stat(invariantPath); err != nil {
		t.Fatalf("invariant file missing: %v", err)
	}
	restarted := NewAgent(
		&captureClient{},
		"",
		NewJSONStateStore(statePath),
		NewJSONInvariantStore(invariantPath),
	)
	items := restarted.Invariants()
	if len(items) != 1 || items[0] != item || restarted.Snapshot() != nil {
		t.Fatalf("items=%+v state=%+v", items, restarted.Snapshot())
	}
}

func TestAgentRefusesConflictBeforeGeneratingSolution(t *testing.T) {
	client := &captureClient{reply: `{"checks":[{"id":"INV-001","status":"conflict","explanation":"Запрос требует Python, хотя разрешён только Go."}]}`}
	agent := NewAgent(client, "", nil, NewJSONInvariantStore(filepath.Join(t.TempDir(), "invariants.json")))
	if _, err := agent.AddInvariant("стек", "Использовать только Go"); err != nil {
		t.Fatal(err)
	}
	result, err := agent.Ask(context.Background(), "Предложи реализацию на Python")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Refused || !strings.Contains(result.Answer, "INV-001 [стек]") || !strings.Contains(result.Answer, "Запрос требует Python") {
		t.Fatalf("result=%+v", result)
	}
	if result.InvariantSummary != "INV-001 — conflict" || agent.Snapshot() != nil {
		t.Fatalf("summary=%q state=%+v", result.InvariantSummary, agent.Snapshot())
	}
	calls := client.callSnapshot()
	if len(calls) != 1 {
		t.Fatalf("model calls=%d; solution generation must be blocked", len(calls))
	}
	joined := ""
	for _, message := range calls[0] {
		joined += message.Content
	}
	if !strings.Contains(joined, "Использовать только Go") || !strings.Contains(joined, "Предложи реализацию на Python") {
		t.Fatalf("review prompt=%s", joined)
	}
}

func TestCompatibleRequestIsCheckedBeforeTaskDiscovery(t *testing.T) {
	client := &captureClient{replies: []string{
		`{"checks":[{"id":"INV-001","status":"compatible","explanation":"Запрос прямо выбирает Go."}]}`,
		`{"answer":"Задача определена","task_ready":true,"goal":"Сервис на Go","current_step":"Составить план","expected_action":"Агент автоматически составляет план"}`,
	}}
	agent := NewAgent(client, "", nil, NewJSONInvariantStore(filepath.Join(t.TempDir(), "invariants.json")))
	if _, err := agent.AddInvariant("стек", "Использовать только Go"); err != nil {
		t.Fatal(err)
	}
	result, err := agent.Ask(context.Background(), "Сделай сервис на Go")
	if err != nil {
		t.Fatal(err)
	}
	if result.Refused || !result.TaskCreated || result.InvariantSummary != "INV-001 — compatible" {
		t.Fatalf("result=%+v", result)
	}
	calls := client.callSnapshot()
	if len(calls) != 2 {
		t.Fatalf("model calls=%d", len(calls))
	}
	mainPrompt := ""
	for _, message := range calls[1] {
		mainPrompt += message.Content
	}
	if !strings.Contains(mainPrompt, "ACTIVE_INVARIANTS") || !strings.Contains(mainPrompt, "Использовать только Go") {
		t.Fatalf("main prompt does not retain invariants: %s", mainPrompt)
	}
}

func TestIncompleteInvariantReviewFailsClosed(t *testing.T) {
	client := &captureClient{reply: `{"checks":[]}`}
	agent := NewAgent(client, "", nil, NewJSONInvariantStore(filepath.Join(t.TempDir(), "invariants.json")))
	if _, err := agent.AddInvariant("архитектура", "Сохранять слоистую архитектуру"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Ask(context.Background(), "Измени архитектуру"); err == nil || !strings.Contains(err.Error(), "ответ заблокирован") {
		t.Fatalf("err=%v", err)
	}
	if len(client.callSnapshot()) != 1 || agent.Snapshot() != nil {
		t.Fatal("agent continued after incomplete invariant review")
	}
}

func TestInvariantTerminalCommands(t *testing.T) {
	agent := NewAgent(&captureClient{}, "", nil, NewJSONInvariantStore(filepath.Join(t.TempDir(), "invariants.json")))
	var out bytes.Buffer
	for _, command := range []string{
		"/invariant add бизнес-правило | Нельзя удалять оплаченные заказы",
		"/invariants",
		"/invariant remove INV-001",
		"/invariants",
	} {
		handled, exit := runCommand(agent, command, &out)
		if !handled || exit {
			t.Fatalf("command=%q handled=%v exit=%v", command, handled, exit)
		}
	}
	text := out.String()
	for _, want := range []string{"Инвариант INV-001 сохранён", "INV-001 [бизнес-правило]", "Инвариант INV-001 удалён", "Инварианты не заданы"} {
		if !strings.Contains(text, want) {
			t.Errorf("output misses %q:\n%s", want, text)
		}
	}
}

func TestClearInvariantsKeepsTaskAndPersistsEmptyList(t *testing.T) {
	dir := t.TempDir()
	stateStore := NewJSONStateStore(filepath.Join(dir, "task-state.json"))
	invariantStore := NewJSONInvariantStore(filepath.Join(dir, "invariants.json"))
	agent := NewAgent(&captureClient{}, "", stateStore, invariantStore)
	if _, err := agent.AddInvariant("стек", "Использовать Go"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.AddInvariant("архитектура", "Сохранять границы слоёв"); err != nil {
		t.Fatal(err)
	}
	if err := agent.StartTask("Сделать сервис"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	handled, exit := runCommand(agent, "/invariant clear", &out)
	if !handled || exit {
		t.Fatalf("handled=%v exit=%v", handled, exit)
	}
	if !strings.Contains(out.String(), "Удалены все инварианты: 2") {
		t.Fatalf("output=%q", out.String())
	}
	if len(agent.Invariants()) != 0 || agent.Snapshot() == nil || agent.Snapshot().Goal != "Сделать сервис" {
		t.Fatalf("invariants=%+v state=%+v", agent.Invariants(), agent.Snapshot())
	}
	restarted := NewAgent(&captureClient{}, "", stateStore, invariantStore)
	if len(restarted.Invariants()) != 0 || restarted.Snapshot() == nil {
		t.Fatalf("invariants=%+v state=%+v", restarted.Invariants(), restarted.Snapshot())
	}
}
