package main

import "testing"

func feedEditor(editor *lineEditor, keys string) {
	for _, r := range keys {
		editor.feed(r)
	}
}

func TestAltDeleteRemovesPreviousWord(t *testing.T) {
	for _, sequence := range []string{"\x1b\x7f", "\x1b\x08", "\x17", "\x1b[3;3~"} {
		editor := &lineEditor{}
		feedEditor(editor, "один два   "+sequence)
		if got := string(editor.text); got != "один " {
			t.Fatalf("sequence %q: got %q", sequence, got)
		}
	}
}

func TestEditorHistoryAndUnicodeBackspace(t *testing.T) {
	editor := &lineEditor{history: []string{"первое", "второе"}, position: 2}
	feedEditor(editor, "черновик\x1b[A\x1b[B!")
	if got := string(editor.text); got != "черновик!" {
		t.Fatalf("got %q", got)
	}
	feedEditor(editor, "\x7f")
	if got := string(editor.text); got != "черновик" {
		t.Fatalf("got %q", got)
	}
}

func TestUpAndDownRecallPreviouslySentMessages(t *testing.T) {
	editor := &lineEditor{
		history:  []string{"первое сообщение", "/state", "третья\nстрока"},
		position: 3,
	}
	feedEditor(editor, "черновик")
	feedEditor(editor, "\x1b[A")
	if got := string(editor.text); got != "третья\nстрока" {
		t.Fatalf("last message=%q", got)
	}
	feedEditor(editor, "\x1b[1;1A")
	if got := string(editor.text); got != "/state" {
		t.Fatalf("previous command=%q", got)
	}
	feedEditor(editor, "\x1b[A")
	if got := string(editor.text); got != "первое сообщение" {
		t.Fatalf("oldest message=%q", got)
	}
	feedEditor(editor, "\x1b[B\x1b[B\x1b[1;1B")
	if got := string(editor.text); got != "черновик" {
		t.Fatalf("restored draft=%q", got)
	}
}

func TestMouseWheelScrollsChatWithoutChangingInputHistory(t *testing.T) {
	editor := &lineEditor{history: []string{"старое сообщение"}, position: 1}
	feedEditor(editor, "черновик\x1b[<64;10;5M")
	if editor.chatScroll != 1 || string(editor.text) != "черновик" || editor.position != 1 {
		t.Fatalf("after wheel up: scroll=%d text=%q position=%d", editor.chatScroll, editor.text, editor.position)
	}
	feedEditor(editor, "\x1b[<65;10;5M")
	if editor.chatScroll != 0 || string(editor.text) != "черновик" || editor.position != 1 {
		t.Fatalf("after wheel down: scroll=%d text=%q position=%d", editor.chatScroll, editor.text, editor.position)
	}
}

func TestAltSTogglesNativeTextSelectionWithoutEditing(t *testing.T) {
	editor := &lineEditor{}
	feedEditor(editor, "текст\x1bs")
	if !editor.toggleSelect || string(editor.text) != "текст" {
		t.Fatalf("toggle=%v text=%q", editor.toggleSelect, editor.text)
	}
}

func TestCtrlCStopsEditor(t *testing.T) {
	editor := &lineEditor{}
	ready, stop := editor.feed(3)
	if ready || !stop {
		t.Fatalf("ready=%v stop=%v", ready, stop)
	}
}

func TestKeypadEnterSubmitsMessage(t *testing.T) {
	for _, sequence := range []string{"\x1bOM", "\x1b[57414u", "\x1b[57414;1u"} {
		editor := &lineEditor{}
		feedEditor(editor, "сообщение")
		ready := false
		for _, r := range sequence {
			ready, _ = editor.feed(r)
		}
		if !ready || string(editor.text) != "сообщение" {
			t.Fatalf("sequence=%q ready=%v text=%q", sequence, ready, editor.text)
		}
	}
}

func TestTabFillsSuggestedUserMessage(t *testing.T) {
	editor := &lineEditor{suggestion: "План утверждаю"}
	ready, _ := editor.feed('\t')
	if ready || string(editor.text) != "План утверждаю" || editor.cursor != len([]rune("План утверждаю")) {
		t.Fatalf("ready=%v text=%q cursor=%d", ready, editor.text, editor.cursor)
	}
	ready, _ = editor.feed('\r')
	if !ready {
		t.Fatal("filled suggestion was not submitted")
	}
}

func TestStatusLineShowsStateAndStage(t *testing.T) {
	state, _ := NewTaskState("test")
	if got := taskStatus(&state); got != "Состояние: активна  ·  Этап: planning" {
		t.Fatalf("got %q", got)
	}
	_ = state.Pause("later")
	if got := taskStatus(&state); got != "Состояние: пауза  ·  Этап: planning" {
		t.Fatalf("got %q", got)
	}
	if got := taskStatusMode(&state, true); got != "Состояние: рассуждает  ·  Этап: planning" {
		t.Fatalf("got %q", got)
	}
}

func TestSlashCommandCompletion(t *testing.T) {
	editor := &lineEditor{}
	feedEditor(editor, "/res")
	if len(editor.options) != 2 || editor.options[0].Value != "/resume" || editor.options[1].Value != "/reset" {
		t.Fatalf("options=%+v", editor.options)
	}
	editor.feed('\x1b')
	editor.feed('[')
	editor.feed('B')
	if editor.selected != 1 {
		t.Fatalf("selected=%d", editor.selected)
	}
	editor.feed('\t')
	if got := string(editor.text); got != "/reset" {
		t.Fatalf("got %q", got)
	}
	ready, _ := editor.feed('\r')
	if !ready {
		t.Fatal("completed command was not submitted")
	}
}

func TestPauseCompletionLeavesRoomForReason(t *testing.T) {
	editor := &lineEditor{}
	feedEditor(editor, "/pause")
	ready, _ := editor.feed('\r')
	if ready || string(editor.text) != "/pause " {
		t.Fatalf("ready=%v text=%q", ready, editor.text)
	}
}

func TestEscapeDismissesCommandPalette(t *testing.T) {
	editor := &lineEditor{}
	feedEditor(editor, "/")
	if len(editor.options) == 0 {
		t.Fatal("palette did not open")
	}
	editor.dismissCompletions()
	if len(editor.options) != 0 || !editor.dismissed {
		t.Fatalf("editor=%+v", editor)
	}
}

func TestCursorNavigationAndInsertion(t *testing.T) {
	editor := &lineEditor{}
	feedEditor(editor, "hello world")
	for i := 0; i < 5; i++ {
		feedEditor(editor, "\x1b[D")
	}
	feedEditor(editor, "brave ")
	if got := string(editor.text); got != "hello brave world" {
		t.Fatalf("got %q", got)
	}
	feedEditor(editor, "\x1b[H>")
	feedEditor(editor, "\x1b[F<")
	if got := string(editor.text); got != ">hello brave world<" {
		t.Fatalf("got %q", got)
	}
}

func TestWordNavigationAndDeleteAtCursor(t *testing.T) {
	editor := &lineEditor{}
	feedEditor(editor, "one two three")
	feedEditor(editor, "\x1b[1;3D")
	feedEditor(editor, "\x1b[3~")
	if got := string(editor.text); got != "one two hree" {
		t.Fatalf("got %q", got)
	}
	feedEditor(editor, "\x1bd")
	if got := string(editor.text); got != "one two " {
		t.Fatalf("got %q", got)
	}
}

func TestShiftEnterCreatesNewLineAndEnterSubmits(t *testing.T) {
	for _, sequence := range []string{"\x1b[13;2u", "\x1b[27;2;13~", "\n"} {
		editor := &lineEditor{}
		feedEditor(editor, "первая"+sequence+"вторая")
		if got := string(editor.text); got != "первая\nвторая" {
			t.Fatalf("sequence %q: got %q", sequence, got)
		}
		ready, _ := editor.feed('\r')
		if !ready {
			t.Fatalf("sequence %q was not submitted", sequence)
		}
	}
}

func TestVerticalNavigationInMultilineInput(t *testing.T) {
	editor := &lineEditor{}
	feedEditor(editor, "abcde\nxy")
	feedEditor(editor, "\x1b[A!")
	if got := string(editor.text); got != "ab!cde\nxy" {
		t.Fatalf("got %q cursor=%d", got, editor.cursor)
	}
}
