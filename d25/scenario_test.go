package main

import (
	"context"
	"strings"
	"testing"
)

func TestTenMessageOnboardingDocumentScenario(t *testing.T) {
	goal := "Create a one-page remote onboarding guide for the ZephyraX cohort"
	buddy := "ZephyraX buddy: Mira; orientation: Monday 10:00"
	pickup := "ZephyraX loaner pickup: reception Desk B, Monday 09:30"
	state := func(details ...string) TaskState { return TaskState{Goal: goal, Clarifications: details} }
	client := &scriptedTurnClient{replies: []modelReply{
		{Answer: "Full-time employees can work remotely.", Task: state()},
		{Answer: "The co-working allowance is $200/month.", Task: state()},
		{Answer: "Employees with 3–5 years receive 25 PTO days.", Task: state()},
		{Answer: "Submit PTO requests at least two weeks ahead.", Task: state()},
		{Answer: "The documents do not name a ZephyraX buddy or orientation time. Who and when?", Task: state()},
		{Answer: "I recorded Mira and Monday 10:00 for ZephyraX.", Task: state(buddy)},
		{Answer: "Submit expense reports within 30 days.", Task: state(buddy)},
		{Answer: "The documents do not give a ZephyraX pickup point. Where is it?", Task: state(buddy)},
		{Answer: "I recorded reception Desk B, Monday 09:30.", Task: state(buddy, pickup)},
		{Answer: "# ZephyraX onboarding guide\nRemote work, co-working, PTO, expenses.\nBuddy Mira. Orientation Monday 10:00. Pickup at Desk B Monday 09:30.", Task: state(buddy, pickup)},
	}}
	turns := []struct{ question, source string }{
		{"We need a one-page remote onboarding guide for the ZephyraX cohort. Who is eligible for fully remote work?", "Employee_Handbook_2025.txt"},
		{"What is the monthly co-working-space allowance?", "Employee_Handbook_2025.txt"},
		{"How many PTO days do employees with 3-5 years of service receive?", "Employee_Handbook_2025.txt"},
		{"How far in advance should a PTO request be submitted?", "Employee_Handbook_2025.txt"},
		{"Who is the ZephyraX onboarding buddy, and when is orientation?", ""},
		{"For ZephyraX the buddy is Mira; orientation is Monday at 10:00.", ""},
		{"By when must an expense report be submitted?", "Expense_Policy_2025.txt"},
		{"Where is the ZephyraX loaner laptop pickup point?", ""},
		{"For ZephyraX, loaner pickup is reception Desk B, Monday 09:30.", ""},
		{"Create the final ZephyraX onboarding guide covering remote eligibility, co-working allowance, PTO days and request timing, expense report deadline, buddy, orientation and loaner pickup. Clearly separate company policy from the ZephyraX details I provided.", "Employee_Handbook_2025.txt"},
	}
	a := NewAgent(client, t.TempDir())
	if err := a.Build(context.Background(), "knowledge", nil); err != nil {
		t.Fatal(err)
	}
	for i, turn := range turns {
		answer, err := a.Answer(context.Background(), turn.question, nil)
		if err != nil {
			t.Fatalf("turn %d: %v", i+1, err)
		}
		if !strings.Contains(answer, "Источники:") {
			t.Fatalf("turn %d has no source heading", i+1)
		}
		if turn.source == "" && !strings.Contains(answer, "Источники:\nНе найдены") {
			t.Fatalf("turn %d claims unrelated document: %s", i+1, answer)
		}
		if turn.source != "" && !strings.Contains(answer, turn.source) {
			t.Fatalf("turn %d lacks document: %s", i+1, answer)
		}
		prompt := client.calls[i][len(client.calls[i])-1].Content
		if i > 0 && !strings.Contains(prompt, goal) {
			t.Fatalf("turn %d lost goal", i+1)
		}
		if i > 5 && !strings.Contains(prompt, buddy) {
			t.Fatalf("turn %d lost buddy detail", i+1)
		}
		if i > 8 && !strings.Contains(prompt, pickup) {
			t.Fatalf("turn %d lost pickup detail", i+1)
		}
		if i == 9 && (!strings.Contains(answer, "Mira") || !strings.Contains(answer, "Desk B")) {
			t.Fatal("final document lost local decisions")
		}
	}
	if len(a.history) != 20 || len(a.task.Clarifications) != 2 {
		t.Fatal("ten-message dialogue incomplete")
	}
}
