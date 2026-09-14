package main

import "testing"

func TestMemoryCompleterFiltersCommandsAndLayers(t *testing.T) {
	complete := memoryCompleter(nil)
	if got := complete("/rem"); len(got) != 1 || got[0].Value != "/remember " {
		t.Fatalf("commands=%+v", got)
	}
	if got := complete("/remember w"); len(got) != 1 || got[0].Value != "/remember working " {
		t.Fatalf("layers=%+v", got)
	}
	if got := complete("/remember working "); len(got) != 0 {
		t.Fatalf("completion should yield to key input: %+v", got)
	}
	if got := complete("/clear "); len(got) != 4 {
		t.Fatalf("clear layers=%+v", got)
	}
	if got := complete("ordinary text"); got != nil {
		t.Fatalf("unexpected=%+v", got)
	}
}

func TestLineEditorCompletionAndHistory(t *testing.T) {
	editor := &lineEditor{complete: memoryCompleter(nil), history: []string{"previous"}, position: 1}
	for _, r := range "/rem" {
		editor.feed(r)
	}
	if ready, _ := editor.feed('\t'); ready || string(editor.text) != "/remember " {
		t.Fatalf("text=%q", editor.text)
	}
	for _, r := range "w" {
		editor.feed(r)
	}
	if ready, _ := editor.feed('\t'); ready || string(editor.text) != "/remember working " {
		t.Fatalf("text=%q", editor.text)
	}
	editor.text = nil
	editor.options = nil
	editor.dismissed = true
	editor.position = 1
	for _, r := range "\x1b[A" {
		editor.feed(r)
	}
	if string(editor.text) != "previous" {
		t.Fatalf("history=%q", editor.text)
	}
}

func TestEnterSubmitsCompleteCommand(t *testing.T) {
	editor := &lineEditor{complete: memoryCompleter(nil)}
	for _, r := range "/inf" {
		editor.feed(r)
	}
	ready, _ := editor.feed('\r')
	if !ready || string(editor.text) != "/info" {
		t.Fatalf("ready=%v text=%q", ready, editor.text)
	}
}
