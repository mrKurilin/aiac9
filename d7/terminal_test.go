package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

type terminalFake struct{ calls [][]Message }

func (f *terminalFake) Complete(_ context.Context, messages []Message) (string, error) {
	f.calls = append(f.calls, messages)
	if messages[len(messages)-1].Content == "fail" {
		return "", fmt.Errorf("test failure")
	}
	return "Ответ", nil
}

func TestTerminalMemoryAndReset(t *testing.T) {
	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &terminalFake{}
	a := NewAgent(f, "system", store, "terminal")
	var out bytes.Buffer
	err = runTerminal(context.Background(), a, strings.NewReader("one\nfail\ntwo\n/reset\nthree\n/exit\nignored\n"), &out, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 4 || len(f.calls[2]) != 4 || len(f.calls[3]) != 2 {
		t.Fatalf("unexpected history: %+v", f.calls)
	}
	restored := NewAgent(f, "system", store, "terminal").History()
	if len(restored) != 2 || restored[0].Content != "three" {
		t.Fatalf("restored: %+v", restored)
	}
}

func TestTerminalSingleAndEOF(t *testing.T) {
	f := &terminalFake{}
	a := NewAgent(f, "", nil, "terminal")
	var out bytes.Buffer
	if err := runTerminal(context.Background(), a, strings.NewReader(""), &out, "hello"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Ответ\n" {
		t.Fatal(out.String())
	}
	if err := runTerminal(context.Background(), a, strings.NewReader(""), &out, "fail"); err == nil {
		t.Fatal("expected API error")
	}
	if err := runTerminal(context.Background(), a, strings.NewReader(""), &out, ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../escape", "a/b", strings.Repeat("a", 65)} {
		if validID(id) {
			t.Fatalf("accepted %q", id)
		}
	}
}
