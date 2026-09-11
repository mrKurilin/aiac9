package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestMarkdownChunkBoundaries(t *testing.T) {
	var out bytes.Buffer
	m := &markdownWriter{out: &out, styled: true}
	for _, part := range []string{"# Заг", "оловок\n**жи", "рный** и `код`\n```go\n", "**literal**\n```\n", "остаток"} {
		if err := m.Write(part); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"\x1b[1;34mЗаголовок", "\x1b[1mжирный", "\x1b[36mкод", "**literal**", "остаток"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

type terminalStreamFake struct {
	t    *testing.T
	out  *bytes.Buffer
	fail bool
}

func (f terminalStreamFake) Complete(context.Context, []Message) (string, error) {
	f.t.Fatal("expected streaming request")
	return "", nil
}

func (f terminalStreamFake) CompleteStream(_ context.Context, _ []Message, emit func(string) error) (string, error) {
	if err := emit("**При"); err != nil {
		return "", err
	}
	if !strings.HasSuffix(f.out.String(), "**При") {
		f.t.Fatal("chunk was not written immediately")
	}
	if f.fail {
		return "", fmt.Errorf("stream interrupted")
	}
	if err := emit("вет**"); err != nil {
		return "", err
	}
	return "**Привет**", nil
}

func TestTerminalStreaming(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var out bytes.Buffer
		a := NewAgent(terminalStreamFake{t, &out, fail}, "", nil, "terminal")
		err := runTerminal(context.Background(), a, strings.NewReader(""), &out, "hello")
		if (err != nil) != fail {
			t.Fatalf("error: %v", err)
		}
		if fail {
			if len(a.History()) != 0 || !strings.HasSuffix(out.String(), "**При\n") {
				t.Fatal("partial answer was saved or not separated")
			}
		} else if !strings.HasSuffix(out.String(), "**Привет**\n") || len(a.History()) != 2 {
			t.Fatal("unexpected output or history")
		}
	}
}
