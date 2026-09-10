package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestFillContextPersistsExactIncrement(t *testing.T) {
	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &terminalFake{}
	m := &Meter{Client: f, ContextLimit: 4096, Reserve: 512}
	a := NewAgent(m, "system", store, "fill")
	before := estimateMessages(a.contextMessages())
	for i := 0; i < 2; i++ {
		added, err := a.FillContext(20)
		if err != nil {
			t.Fatal(err)
		}
		after := estimateMessages(a.contextMessages())
		if added != 820 || after-before != 820 {
			t.Fatalf("increment = %d; actual %d", added, after-before)
		}
		before = after
	}
	restored := NewAgent(m, "system", store, "fill")
	if len(restored.History()) != 2 || len(f.calls) != 0 || m.Turns != 0 {
		t.Fatal("persistence or unwanted API call")
	}
	if _, err := restored.Ask(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !strings.Contains(f.calls[0][1].Content, "Учебный наполнитель") {
		t.Fatal("filler not sent")
	}
}

func TestContextBreakdownAfterFill(t *testing.T) {
	m := &Meter{Client: &terminalFake{}, ContextLimit: 4096, Reserve: 512}
	a := NewAgent(m, "system", nil, "fill")
	a.history = []Message{{Role: "user", Content: "обычное сообщение"}}
	if _, err := a.FillContext(20); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.ShowContext(&out)
	for _, want := range []string{"system ≈", "диалог ≈", "1 сообщ.", "наполнитель ≈820 (1 блок.)", "свободно ≈"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}

func TestFillerEstimateHandlesOldAndCurrentFormats(t *testing.T) {
	prefix := (len(contextFillerPrefix) + 2) / 3
	if got := estimateText(contextFillerPrefix + strings.Repeat(" x", 10)); got != prefix+10 {
		t.Fatalf("current filler: %d", got)
	}
	if got := estimateText(contextFillerPrefix + strings.Repeat(" x ", 10)); got != prefix+20 {
		t.Fatalf("old filler: %d", got)
	}
}

func TestFillCommandExplainsWhatWasAdded(t *testing.T) {
	m := &Meter{Client: &terminalFake{}, ContextLimit: 4096, Reserve: 0}
	a := NewAgent(m, "system", nil, "fill")
	var out bytes.Buffer
	if !contextCommand(a, "/fill 20", &out) {
		t.Fatal("fill command was not handled")
	}
	for _, want := range []string{
		"добавлено 1 сообщение role=user",
		"≈820 токенов",
		"+20% модельного окна",
		"служебная метка",
		"повторов ` x`",
		"уйдёт в API со следующим обычным сообщением",
		"сейчас API не вызывался",
		"Всего наполнителя: ≈820 токенов; блоков: 1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}
func TestFillCommandsAndOverflow(t *testing.T) {
	f := &terminalFake{}
	m := &Meter{Client: f, ContextLimit: 4096, Reserve: 128}
	a := NewAgent(m, "system", nil, "fill")
	var out bytes.Buffer
	input := "+20%\n/fill 20\n+100%\n/context\nhello\n/reset\nhello\n/exit\n"
	if err := runTerminal(context.Background(), a, strings.NewReader(input), &out, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || len(a.History()) != 2 {
		t.Fatal("overflow did not block or reset failed")
	}
	for _, want := range []string{"+20% модельного окна", "Окно с резервом переполнено", "запрос не отправлен"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("missing", want)
		}
	}
}
func TestFillInvalidDoesNotMutate(t *testing.T) {
	m := &Meter{Client: &terminalFake{}}
	a := NewAgent(m, "", nil, "fill")
	if _, err := a.FillContext(20); err == nil {
		t.Fatal("missing limit accepted")
	}
	m.ContextLimit = 4096
	for _, n := range []int{-1, 0, 101} {
		if _, err := a.FillContext(n); err == nil {
			t.Fatal("invalid percentage accepted")
		}
	}
	m.ContextLimit = 1
	if _, err := a.FillContext(20); err == nil {
		t.Fatal("too small")
	}
	m.ContextLimit = 10_000_000
	if _, err := a.FillContext(100); err == nil {
		t.Fatal("allocation limit ignored")
	}
	if len(a.History()) != 0 {
		t.Fatal("history mutated")
	}
}
