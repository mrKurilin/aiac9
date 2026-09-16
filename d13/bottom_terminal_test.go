package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestBottomTerminalKeepsStatusAndArrowOnLastRows(t *testing.T) {
	var out bytes.Buffer
	terminal, err := openBottomTerminal(&out, func() (int, int) { return 80, 24 })
	if err != nil {
		t.Fatal(err)
	}
	state, _ := NewTaskState("test")
	if err := terminal.setState(&state); err != nil {
		t.Fatal(err)
	}
	if err := terminal.drawEditor(&lineEditor{text: []rune("новое сообщение")}); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(terminal, "ответ агента")
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	frame := out.String()
	for _, want := range []string{
		"\x1b[?1049h",
		"\x1b[?1000h",
		"\x1b[?1006h",
		"\x1b[23;1H",
		"Состояние: активна  ·  Этап: planning",
		"\x1b[24;1H\x1b[2K\x1b[36m→\x1b[0m новое сообщение",
		"ответ агента",
		"\x1b[?1006l",
		"\x1b[?1000l",
		"\x1b[?1049l",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame misses %q", want)
		}
	}
}

func TestBottomTerminalScrollsTranscriptWithMouseOffset(t *testing.T) {
	var out bytes.Buffer
	terminal, err := openBottomTerminal(&out, func() (int, int) { return 30, 8 })
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		fmt.Fprintf(terminal, "строка %02d\n", index)
	}
	if err := terminal.scrollChat(1); err != nil {
		t.Fatal(err)
	}
	terminal.mu.Lock()
	offset := terminal.scrollOffset
	terminal.mu.Unlock()
	if offset != 3 {
		t.Fatalf("scroll offset=%d", offset)
	}
	if err := terminal.scrollChat(-1); err != nil {
		t.Fatal(err)
	}
	terminal.mu.Lock()
	offset = terminal.scrollOffset
	terminal.mu.Unlock()
	if offset != 0 {
		t.Fatalf("scroll offset after down=%d", offset)
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBottomTerminalCanReleaseMouseForNativeSelection(t *testing.T) {
	var out bytes.Buffer
	terminal, err := openBottomTerminal(&out, func() (int, int) { return 60, 10 })
	if err != nil {
		t.Fatal(err)
	}
	if err := terminal.toggleSelectionMode(); err != nil {
		t.Fatal(err)
	}
	terminal.mu.Lock()
	selectionMode := terminal.selectionMode
	terminal.mu.Unlock()
	if !selectionMode || !strings.Contains(out.String(), "Режим выделения мышью") {
		t.Fatalf("selection mode not enabled: %q", out.String())
	}
	if err := terminal.toggleSelectionMode(); err != nil {
		t.Fatal(err)
	}
	terminal.mu.Lock()
	selectionMode = terminal.selectionMode
	terminal.mu.Unlock()
	if selectionMode {
		t.Fatal("selection mode not disabled")
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBottomTerminalShowsCommandPaletteAboveStatus(t *testing.T) {
	var out bytes.Buffer
	terminal, err := openBottomTerminal(&out, func() (int, int) { return 80, 24 })
	if err != nil {
		t.Fatal(err)
	}
	editor := &lineEditor{}
	for _, r := range "/res" {
		editor.feed(r)
	}
	if err := terminal.drawEditor(editor); err != nil {
		t.Fatal(err)
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	frame := out.String()
	if !strings.Contains(frame, "› /resume") || !strings.Contains(frame, "  /reset") {
		t.Fatalf("command palette missing: %q", frame)
	}
}

func TestBottomTerminalShowsLoaderWhileWaiting(t *testing.T) {
	var out bytes.Buffer
	terminal, err := openBottomTerminal(&out, func() (int, int) { return 80, 24 })
	if err != nil {
		t.Fatal(err)
	}
	state, _ := NewTaskState("test")
	if err := terminal.setActivity(&state, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Состояние: рассуждает  ·  Этап: planning  ⠋") {
		t.Fatalf("loader missing from status: %q", out.String())
	}
	if err := terminal.setState(&state); err != nil {
		t.Fatal(err)
	}
	terminal.mu.Lock()
	loading := terminal.loading
	terminal.mu.Unlock()
	if loading {
		t.Fatal("loader did not stop after response")
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBottomTerminalShowsSuggestedReplyForTab(t *testing.T) {
	var out bytes.Buffer
	terminal, err := openBottomTerminal(&out, func() (int, int) { return 80, 24 })
	if err != nil {
		t.Fatal(err)
	}
	state, _ := NewTaskState("test")
	if err := terminal.setState(&state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "План утверждаю  [Tab]") {
		t.Fatalf("suggested reply missing: %q", out.String())
	}
	if got := terminal.suggestedReply(); got != "План утверждаю" {
		t.Fatalf("suggested reply=%q", got)
	}
	if err := terminal.setActivity(&state, true); err != nil {
		t.Fatal(err)
	}
	if got := terminal.suggestedReply(); got != "" {
		t.Fatalf("suggestion must be hidden while waiting: %q", got)
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMultilineComposerMovesStatusAndCursorUp(t *testing.T) {
	var out bytes.Buffer
	terminal, err := openBottomTerminal(&out, func() (int, int) { return 40, 12 })
	if err != nil {
		t.Fatal(err)
	}
	editor := &lineEditor{text: []rune("первая строка\nвторая"), cursor: len([]rune("первая строка\nвторая"))}
	if err := terminal.drawEditor(editor); err != nil {
		t.Fatal(err)
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	frame := out.String()
	for _, want := range []string{
		"\x1b[10;1H\x1b[2K\x1b[2mСостояние:",
		"\x1b[11;1H\x1b[2K\x1b[36m→\x1b[0m первая строка",
		"\x1b[12;1H\x1b[2K\x1b[36m│\x1b[0m вторая",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame misses %q", want)
		}
	}
}
