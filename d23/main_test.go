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

type fakeClient struct{ calls []string }

func (f *fakeClient) Complete(_ context.Context, messages []Message) (string, error) {
	last := messages[len(messages)-1].Content
	f.calls = append(f.calls, last)
	if strings.Contains(messages[0].Content, "Переформулируй") {
		return "правила отпуска PTO", nil
	}
	if strings.Contains(last, "PTO") && strings.Contains(last, "25 дней") {
		return "25 дней PTO.", nil
	}
	return "Нет данных.", nil
}

func testAgent(t *testing.T) (*Agent, *fakeClient) {
	t.Helper()
	client := &fakeClient{}
	a := NewAgent(client, t.TempDir())
	index := Index{Version: 1, Chunks: []Chunk{
		{Source: "handbook.md", Section: "Отпуск", Text: "Сотрудник получает 25 дней PTO в год.", Vector: vector("Сотрудник получает 25 дней PTO в год.")},
		{Source: "noise.md", Section: "Шум", Text: "Сборка проекта использует Go.", Vector: vector("Сборка проекта использует Go.")},
	}}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(a.indexDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.indexPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	return a, client
}

func TestCompareUsesRewriteAndFilters(t *testing.T) {
	a, client := testAgent(t)
	a.config.CandidateK, a.config.FinalK, a.config.Threshold = 2, 1, 0.2
	c, err := a.Compare(context.Background(), "Сколько дней отпуска?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Rewritten != "правила отпуска PTO" || c.Candidates != 2 || len(c.Before) != 2 || len(c.After) != 1 {
		t.Fatalf("comparison=%+v", c)
	}
	if c.After[0].Chunk.Source != "handbook.md" || c.Improved != "25 дней PTO." {
		t.Fatalf("after=%+v, answer=%q", c.After, c.Improved)
	}
	if len(client.calls) != 3 {
		t.Fatalf("calls=%d, want rewrite + two answers", len(client.calls))
	}
}

func TestRAGCommandsAndCompletion(t *testing.T) {
	a, _ := testAgent(t)
	var out bytes.Buffer
	for _, line := range []string{"/rag top 4", "/rag final 2", "/rag threshold 0.4", "/rag rewrite off", "/rag on", "/rag status"} {
		handled, exit := runCommand(context.Background(), a, line, &out)
		if !handled || exit {
			t.Fatalf("command not handled: %s", line)
		}
	}
	if !a.rag || a.config.CandidateK != 4 || a.config.FinalK != 2 || a.config.Rewrite || a.config.Threshold != 0.4 {
		t.Fatalf("config=%+v rag=%t", a.config, a.rag)
	}
	for _, command := range commands {
		found := false
		for _, completion := range completions(command) {
			if completion == command {
				found = true
			}
		}
		if !found {
			t.Errorf("no completion for %s", command)
		}
	}
}

func TestBuildExcludesPrivateLookingFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "public.md"), []byte("# Fact\nPublic information"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api_token.txt"), []byte("must not index"), 0600); err != nil {
		t.Fatal(err)
	}
	idx, err := buildIndex(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Chunks) != 1 || idx.Chunks[0].Source != "public.md" {
		t.Fatalf("chunks=%+v", idx.Chunks)
	}
}

func TestD22EditorSelectsNestedCommandWithTab(t *testing.T) {
	e := lineEditor{}
	for _, r := range "/rag th" {
		e.feed(r)
	}
	if len(e.options) != 1 || e.options[0].Value != "/rag threshold" {
		t.Fatalf("options=%v", e.options)
	}
	e.feed('\t')
	for _, r := range " 0.4" {
		e.feed(r)
	}
	ready, stop := e.feed('\r')
	if !ready || stop || string(e.text) != "/rag threshold 0.4" {
		t.Fatalf("ready=%t stop=%t text=%q", ready, stop, string(e.text))
	}
}

func TestD22EditorDeletesPreviousWordWithAltBackspace(t *testing.T) {
	e := lineEditor{}
	for _, r := range "один два" {
		e.feed(r)
	}
	e.feed(27)
	e.feed(127)
	if got := string(e.text); got != "один " {
		t.Fatalf("text=%q", got)
	}
}
