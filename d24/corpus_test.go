package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type corpusQuestion struct{ question, answer, source, quote string }

var corpusQuestions = []corpusQuestion{
	{"How many PTO days do employees with 3-5 years of service receive?", "25 days per year", "Employee_Handbook_2025.txt", "3-5 years of service: 25 days per year"},
	{"How far in advance should a PTO request be submitted?", "At least 2 weeks", "Employee_Handbook_2025.txt", "Submit request in BambooHR at least 2 weeks in advance"},
	{"Who is eligible for fully remote work?", "All full-time employees", "Employee_Handbook_2025.txt", "All full-time employees are eligible for fully remote work."},
	{"What is the monthly co-working-space allowance?", "$200/month", "Employee_Handbook_2025.txt", "$200/month stipend for co-working space membership"},
	{"At what amount is expense pre-approval always required?", "Over $500", "Expense_Policy_2025.txt", "Any single expense over $500"},
	{"By when must an expense report be submitted?", "Within 30 days", "Expense_Policy_2025.txt", "Submit within 30 days of expense date"},
	{"Are expenses older than 90 days reimbursed?", "No", "Expense_Policy_2025.txt", "Expenses over 90 days old: Not reimbursable"},
	{"What is the maximum file size in CloudDrive?", "50 GB per file", "Product_Specs_CloudDrive.txt", "Maximum file size: 50 GB per file"},
	{"How many minimum days off are required in the unlimited PTO pilot?", "15 days per year", "Q1_2025_AllHands_Notes.txt", "minimum 15 days off per year"},
	{"What is the target quarter for profitability?", "Q3 2026", "Q1_2025_AllHands_Notes.txt", "Q3 2026 is our target."},
}

func TestExternalCorpusTenQuestions(t *testing.T) {
	a := NewAgent(nil, t.TempDir())
	a.rag = true
	if err := a.Build(context.Background(), "knowledge", nil); err != nil {
		t.Fatal(err)
	}
	idx, err := a.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Chunks) < 20 {
		t.Fatalf("got %d chunks; text sections were not split", len(idx.Chunks))
	}
	client := &scriptedClient{replies: map[string]string{}}
	for _, test := range corpusQuestions {
		hits := search(idx, test.question, a.config.FinalK)
		var matched *Hit
		for i := range hits {
			if hits[i].Chunk.Source == test.source && strings.Contains(hits[i].Chunk.Text, test.quote) && hits[i].Score >= a.config.Threshold {
				matched = &hits[i]
				break
			}
		}
		if matched == nil {
			t.Errorf("not retrieved: %q, best=%v", test.question, describeHits(hits))
			continue
		}
		client.replies[test.question] = test.answer
	}
	a.client = client
	for _, test := range corpusQuestions {
		if _, ok := client.replies[test.question]; !ok {
			continue
		}
		got, _, err := a.Answer(context.Background(), test.question, nil)
		if err != nil || !strings.Contains(got, test.answer) || !strings.Contains(got, test.source) || !strings.Contains(got, test.quote) || !strings.Contains(got, "«") {
			t.Errorf("question=%q answer=%q err=%v", test.question, got, err)
		}
	}
	if client.calls != len(client.replies) {
		t.Errorf("calls=%d answered=%d", client.calls, len(client.replies))
	}
}
func describeHits(hits []Hit) string {
	parts := make([]string, 0, len(hits))
	for _, hit := range hits {
		parts = append(parts, fmt.Sprintf("%s/%s %.3f", hit.Chunk.Source, hit.Chunk.Section, hit.Score))
	}
	return strings.Join(parts, "; ")
}
