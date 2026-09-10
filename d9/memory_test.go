package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

type recordingSummarizer struct {
	calls [][]Message
	err   error
}

func (s *recordingSummarizer) Summarize(_ context.Context, previous string, messages []Message) (string, error) {
	s.calls = append(s.calls, append([]Message(nil), messages...))
	if s.err != nil {
		return "", s.err
	}
	return strings.TrimSpace(previous + " факт из старой истории"), nil
}

func TestCompressionKeepsTailAndInjectsSeparateSummary(t *testing.T) {
	client := &terminalFake{}
	summarizer := &recordingSummarizer{}
	agent := NewAgent(client, "system", nil, "compressed")
	if err := agent.ConfigureCompression(CompressionConfig{Enabled: true, KeepLast: 2, SummaryEvery: 4}, summarizer); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{
		"one " + strings.Repeat("длинная исходная деталь ", 20),
		"two " + strings.Repeat("ещё одна исходная деталь ", 20),
		"three",
		"four",
	} {
		if _, err := agent.Ask(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
	}
	if len(summarizer.calls) != 1 || len(summarizer.calls[0]) != 4 {
		t.Fatalf("summary calls: %+v", summarizer.calls)
	}
	last := client.calls[len(client.calls)-1]
	if len(last) != 5 || last[1].Role != "system" || !strings.HasPrefix(last[1].Content, summaryContextPrefix) {
		t.Fatalf("summary was not injected separately: %+v", last)
	}
	joined := fmt.Sprint(last)
	if strings.Contains(joined, "one") || strings.Contains(joined, "two") {
		t.Fatalf("folded messages leaked into request: %s", joined)
	}
	if !strings.Contains(joined, "three") || !strings.Contains(joined, "four") {
		t.Fatalf("verbatim tail missing: %s", joined)
	}
	if len(agent.History()) != 4 || agent.Summary() == "" {
		t.Fatalf("unexpected memory: summary=%q history=%+v", agent.Summary(), agent.History())
	}
	stats := agent.CompressionSnapshot().Stats
	if stats.Runs != 1 || stats.FoldedMessages != 4 || stats.LastBefore <= stats.LastAfter || stats.SavedTokens <= 0 {
		t.Fatalf("bad stats: %+v", stats)
	}
}

func TestCompressionPersistsSummaryAndTail(t *testing.T) {
	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(&terminalFake{}, "system", store, "persist")
	if err := agent.ConfigureCompression(CompressionConfig{Enabled: true, KeepLast: 2, SummaryEvery: 2}, &recordingSummarizer{}); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{
		"one " + strings.Repeat("важная подробность ", 20),
		"two " + strings.Repeat("следующая подробность ", 20),
		"three",
	} {
		if _, err := agent.Ask(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
	}
	restored := NewAgent(&terminalFake{}, "system", store, "persist")
	if restored.Summary() == "" || len(restored.History()) != 4 {
		t.Fatalf("not restored: summary=%q history=%+v", restored.Summary(), restored.History())
	}
}

func TestIneffectiveSummaryDoesNotReplaceHistory(t *testing.T) {
	client := &terminalFake{}
	agent := NewAgent(client, "system", nil, "ineffective")
	summarizer := &recordingSummarizer{}
	if err := agent.ConfigureCompression(CompressionConfig{Enabled: true, KeepLast: 0, SummaryEvery: 2}, summarizer); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Ask(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Ask(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	if agent.Summary() != "" || len(agent.History()) != 4 {
		t.Fatalf("ineffective summary replaced source: summary=%q history=%+v", agent.Summary(), agent.History())
	}
	if warning := agent.DrainCompressionWarning(); !strings.Contains(warning, "не уменьшает контекст") {
		t.Fatalf("warning=%q", warning)
	}
}

func TestMemoryCommandsToggleAndReport(t *testing.T) {
	agent := NewAgent(&terminalFake{}, "system", nil, "commands")
	if err := agent.ConfigureCompression(CompressionConfig{Enabled: false, KeepLast: 8, SummaryEvery: 10}, &recordingSummarizer{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if !memoryCommand(agent, "/compress on", &out) || !memoryCommand(agent, "/memory", &out) {
		t.Fatal("memory command was not handled")
	}
	for _, want := range []string{"Сжатие новых пакетов: включено", "последние 8 дословно", "пакет от 10", "Сжатий 0"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}

func TestCompressionFailureFallsBackWithoutLosingHistory(t *testing.T) {
	client := &terminalFake{}
	agent := NewAgent(client, "system", nil, "fallback")
	if err := agent.ConfigureCompression(CompressionConfig{Enabled: true, KeepLast: 2, SummaryEvery: 2}, &recordingSummarizer{err: fmt.Errorf("offline")}); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"one", "two", "three"} {
		if _, err := agent.Ask(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
	}
	if agent.Summary() != "" || len(agent.History()) != 6 {
		t.Fatalf("history changed after failed compression: summary=%q history=%+v", agent.Summary(), agent.History())
	}
	last := fmt.Sprint(client.calls[len(client.calls)-1])
	if !strings.Contains(last, "one") || !strings.Contains(last, "two") {
		t.Fatalf("full fallback context missing: %s", last)
	}
	if warning := agent.DrainCompressionWarning(); !strings.Contains(warning, "offline") {
		t.Fatalf("warning=%q", warning)
	}
}
