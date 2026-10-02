package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type scenarioClient struct {
	calls [][]Message
	state TaskState
}

func (c *scenarioClient) Complete(_ context.Context, messages []Message) (string, error) {
	c.calls = append(c.calls, append([]Message(nil), messages...))
	prompt := messages[len(messages)-1].Content
	if strings.Contains(prompt, "Clarification: mobile") {
		c.state.Clarifications = append(c.state.Clarifications, "mobile users first")
	}
	if strings.Contains(prompt, "Constraint: budget") {
		c.state.Constraints = append(c.state.Constraints, "budget under 500")
	}
	if strings.Contains(prompt, "Clarification: international") {
		c.state.Clarifications = append(c.state.Clarifications, "international team")
	}
	if strings.Contains(prompt, "Constraint: deadline") {
		c.state.Constraints = append(c.state.Constraints, "deadline next week")
	}
	answer := "Answer based on retrieved material"
	if strings.Contains(prompt, "zxqv lunar florp") {
		answer = "В базе недостаточно сведений. Какое правило нужно добавить?"
	}
	reply, _ := json.Marshal(modelReply{Answer: answer, Task: c.state})
	return string(reply), nil
}

type scriptedTurnClient struct {
	replies []modelReply
	calls   [][]Message
}

func (c *scriptedTurnClient) Complete(_ context.Context, messages []Message) (string, error) {
	c.calls = append(c.calls, append([]Message(nil), messages...))
	reply, _ := json.Marshal(c.replies[len(c.calls)-1])
	return string(reply), nil
}
func TestUserClarificationFillsMissingDatabaseFactForDocument(t *testing.T) {
	goal := "Create a one-page onboarding guide for project ZephyraX"
	fact := "ZephyraX coordinator is Mira; kickoff is Monday at 10:00"
	client := &scriptedTurnClient{replies: []modelReply{
		{Answer: "В базе нет координатора ZephyraX. Кто это и когда встреча?", Task: TaskState{Goal: goal}},
		{Answer: "Записал координатора и время встречи.", Task: TaskState{Goal: goal, Clarifications: []string{fact}}},
		{Answer: "Координатор — Mira, встреча в понедельник в 10:00.", Task: TaskState{Goal: goal, Clarifications: []string{fact}}},
		{Answer: "# Onboarding guide\nCoordinator: Mira\nKickoff: Monday 10:00", Task: TaskState{Goal: goal, Clarifications: []string{fact}}},
	}}
	a := NewAgent(client, t.TempDir())
	if err := a.Build(context.Background(), "knowledge", nil); err != nil {
		t.Fatal(err)
	}
	a.task.Goal = goal
	questions := []string{
		"Who is the ZephyraX coordinator and when is the kickoff?",
		"For ZephyraX, coordinator is Mira and kickoff is Monday at 10:00. Add these details.",
		"Who coordinates ZephyraX and when is its kickoff?",
		"Draft the ZephyraX onboarding guide from our decisions.",
	}
	for i, question := range questions {
		answer, err := a.Answer(context.Background(), question, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(answer, "Источники:\nНе найдены") {
			t.Fatalf("turn %d: unexpected source claim: %s", i+1, answer)
		}
		if i >= 2 && !strings.Contains(answer, "Mira") {
			t.Fatalf("turn %d lost user detail: %s", i+1, answer)
		}
	}
	if !strings.Contains(client.calls[3][len(client.calls[3])-1].Content, fact) {
		t.Fatal("final draft request lost user clarification")
	}
	if a.task.Goal != goal || len(a.task.Clarifications) != 1 || len(a.history) != 8 {
		t.Fatal("task or dialogue history lost")
	}
}
func TestTwoLongScenariosKeepGoalAndSources(t *testing.T) {
	scenarios := []struct{ goal, question, source, clarification, constraint string }{
		{"Plan CloudDrive rollout", "What is the maximum file size in CloudDrive?", "Product_Specs_CloudDrive.txt", "Clarification: mobile", "Constraint: budget"},
		{"Plan expense reporting", "When must an expense report be submitted?", "Expense_Policy_2025.txt", "Clarification: international", "Constraint: deadline"},
	}
	for _, tc := range scenarios {
		t.Run(tc.goal, func(t *testing.T) {
			client := &scenarioClient{state: TaskState{Goal: tc.goal, Terms: map[string]string{"owner": "team"}}}
			a := NewAgent(client, t.TempDir())
			if err := a.Build(context.Background(), "knowledge", nil); err != nil {
				t.Fatal(err)
			}
			a.task = client.state
			for i := 0; i < 12; i++ {
				question := tc.question
				if i == 3 {
					question += " " + tc.clarification
				}
				if i == 7 {
					question += " " + tc.constraint
				}
				answer, err := a.Answer(context.Background(), question, nil)
				if err != nil {
					t.Fatalf("turn %d: %v", i+1, err)
				}
				if !strings.Contains(answer, "Источники:") || !strings.Contains(answer, tc.source) {
					t.Fatalf("turn %d lacks source: %s", i+1, answer)
				}
				prompt := client.calls[i][len(client.calls[i])-1].Content
				if !strings.Contains(prompt, tc.goal) || !strings.Contains(prompt, "Найденные фрагменты:") {
					t.Fatalf("turn %d lost goal or retrieval", i+1)
				}
				if i > 3 && !strings.Contains(prompt, client.state.Clarifications[0]) {
					t.Fatalf("turn %d lost clarification", i+1)
				}
				if i > 7 && !strings.Contains(prompt, client.state.Constraints[0]) {
					t.Fatalf("turn %d lost constraint", i+1)
				}
			}
			if len(a.history) != 24 || len(client.calls) != 12 || len(client.calls[11]) != 23 {
				t.Fatalf("history length: %d, requests: %d", len(a.history), len(client.calls))
			}
			if a.task.Goal != tc.goal || len(a.task.Clarifications) != 1 || len(a.task.Constraints) != 1 {
				t.Fatalf("memory lost: %+v", a.task)
			}
		})
	}
}
func TestMissingContextStillReportsSourcesAndKeepsMemory(t *testing.T) {
	client := &scenarioClient{state: TaskState{Goal: "Find lunar policy"}}
	a := NewAgent(client, t.TempDir())
	if err := a.Build(context.Background(), "knowledge", nil); err != nil {
		t.Fatal(err)
	}
	answer, err := a.Answer(context.Background(), "zxqv lunar florp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "Источники:\nНе найдены") || !strings.Contains(answer, "недостаточно") {
		t.Fatal(answer)
	}
	if a.task.Goal != client.state.Goal {
		t.Fatal("memory not updated")
	}
}

type resetWriter struct {
	bytes.Buffer
	clears int
}

func (w *resetWriter) ClearChat() error { w.clears++; w.Reset(); return nil }
func TestResetAliasesClearHistoryAndMemoryKeepIndex(t *testing.T) {
	for _, command := range []string{"/reset", "/clear"} {
		t.Run(command, func(t *testing.T) {
			client := &scenarioClient{state: TaskState{Goal: "first goal"}}
			a := NewAgent(client, t.TempDir())
			if err := a.Build(context.Background(), "knowledge", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Answer(context.Background(), "What is the maximum file size in CloudDrive?", nil); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(a.indexDir, indexFile))
			if err != nil {
				t.Fatal(err)
			}
			out := &resetWriter{}
			out.WriteString("old transcript")
			handled, exit := runCommand(context.Background(), a, command, out)
			if !handled || exit || out.clears != 1 || len(a.history) != 0 || a.task.Goal != "" || strings.Contains(out.String(), "old transcript") {
				t.Fatal("reset failed")
			}
			after, err := os.ReadFile(filepath.Join(a.indexDir, indexFile))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("index changed")
			}
			if _, err := a.Answer(context.Background(), "When must an expense report be submitted?", nil); err != nil {
				t.Fatal(err)
			}
			if len(client.calls[1]) != 1 {
				t.Fatal("old history sent after reset")
			}
		})
	}
}
func TestCommandMetadata(t *testing.T) {
	for _, command := range commands {
		if command.Value == "" || command.Description == "" {
			t.Fatalf("incomplete command: %+v", command)
		}
	}
}
