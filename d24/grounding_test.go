package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type scriptedClient struct {
	replies map[string]string
	calls   int
}

type rawClient string

func (c rawClient) Complete(context.Context, []Message) (string, error) { return string(c), nil }

func (c *scriptedClient) Complete(_ context.Context, messages []Message) (string, error) {
	c.calls++
	prompt := messages[len(messages)-1].Content
	for question, answer := range c.replies {
		if strings.HasSuffix(prompt, "Вопрос: "+question) {
			return answer, nil
		}
	}
	return "Не знаю", nil
}

func TestWeakContextSkipsModelAndAsksClarification(t *testing.T) {
	a := NewAgent(&scriptedClient{}, t.TempDir())
	idx := Index{Version: 1, Chunks: []Chunk{{Source: "handbook.md", Section: "Отпуск", Text: "Отпуск 25 дней.", Vector: vector("отпуск 25 дней")}}}
	data, _ := json.Marshal(idx)
	if err := os.MkdirAll(a.indexDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.indexPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	a.rag = true
	client := a.client.(*scriptedClient)
	got, _, err := a.Answer(context.Background(), "астрономическая сингулярность", nil)
	if err != nil || !strings.Contains(got, "Не знаю") || !strings.Contains(got, "уточните") || client.calls != 0 {
		t.Fatalf("answer=%q err=%v calls=%d", got, err, client.calls)
	}
}

func TestModelAnswerFormatsAndEvidence(t *testing.T) {
	for _, raw := range []string{
		"25 days per year",
		`{"answer":"25 days per year","citations":[{"source":"invented.md","quote":"made up"}]}`,
		"```json\n{\"answer\":\"25 days per year\"}\n```",
	} {
		if got := modelAnswer(raw); got != "25 days per year" {
			t.Errorf("raw=%q answer=%q", raw, got)
		}
	}
	hits := []Hit{{Chunk: Chunk{Source: "handbook.md", Section: "PTO", Text: "3-5 years of service: 25 days per year"}, Score: 0.5}}
	question := "How many PTO days do employees with 3-5 years of service receive?"
	citation, ok := evidenceForAnswer("25 days per year", question, hits)
	if !ok || citation.Source != "handbook.md" || !strings.Contains(citation.Quote, "25 days") {
		t.Fatalf("citation=%+v ok=%t", citation, ok)
	}
	if _, ok := evidenceForAnswer("35 days per year", question, hits); ok {
		t.Fatal("unsupported numeric claim accepted")
	}
}

func TestRAGRepairsModelCitationFromCorpus(t *testing.T) {
	for _, raw := range []string{
		"25 days per year",
		"```json\n{\"answer\":\"25 days per year\",\"citations\":[{\"source\":\"invented.md\",\"quote\":\"made up\"}]}\n```",
	} {
		a := NewAgent(rawClient(raw), t.TempDir())
		a.rag = true
		if err := a.Build(context.Background(), "knowledge", nil); err != nil {
			t.Fatal(err)
		}
		got, _, err := a.Answer(context.Background(), "How many PTO days do employees with 3-5 years of service receive?", nil)
		if err != nil || !strings.Contains(got, "25 days per year") || !strings.Contains(got, "Employee_Handbook_2025.txt") || !strings.Contains(got, "3-5 years of service: 25 days per year") || strings.Contains(got, "invented.md") {
			t.Fatalf("answer=%q err=%v", got, err)
		}
	}
}

func TestReportedQuestionsWithNaturalSentenceAnswers(t *testing.T) {
	a := NewAgent(nil, t.TempDir())
	a.rag = true
	if err := a.Build(context.Background(), "knowledge", nil); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ question, answer, source string }{
		{"How many PTO days do employees with 3-5 years of service receive?", "Employees with 3-5 years of service receive 25 days of PTO per year.", "Employee_Handbook_2025.txt"},
		{"How far in advance should a PTO request be submitted?", "A PTO request should be submitted at least 2 weeks in advance.", "Employee_Handbook_2025.txt"},
		{"Who is eligible for fully remote work?", "All full-time employees are eligible for fully remote work.", "Employee_Handbook_2025.txt"},
		{"What is the monthly co-working-space allowance?", "The monthly co-working space allowance is $200 per month.", "Employee_Handbook_2025.txt"},
	} {
		a.client = rawClient(test.answer)
		got, _, err := a.Answer(context.Background(), test.question, nil)
		if err != nil || !strings.Contains(got, test.answer) || !strings.Contains(got, test.source) || !strings.Contains(got, "«") {
			t.Errorf("question=%q answer=%q err=%v", test.question, got, err)
		}
	}
}

func TestRAGRejectsUnsupportedModelAnswer(t *testing.T) {
	a := NewAgent(rawClient("35 days per year"), t.TempDir())
	a.rag = true
	if err := a.Build(context.Background(), "knowledge", nil); err != nil {
		t.Fatal(err)
	}
	got, _, err := a.Answer(context.Background(), "How many PTO days do employees with 3-5 years of service receive?", nil)
	if err != nil || !strings.Contains(got, "Не знаю") || strings.Contains(got, "35 days") {
		t.Fatalf("answer=%q err=%v", got, err)
	}
}
