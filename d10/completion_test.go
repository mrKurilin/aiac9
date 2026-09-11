package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestCommandAndStrategySelectionWithArrowsAndTab(t *testing.T) {
	a := NewAgent(&terminalFake{}, "", nil, "menus")
	for _, test := range []struct{ keys, want string }{
		{"/\n\x1b[B\n", "/contextManagementStrategy facts"},
		{"/cont\t\x1b[B\n", "/contextManagementStrategy facts"},
		{"/contextManagementStrategy\n", "/contextManagementStrategy window"},
		{"/\x1b[B\nfeature\n", "/addBranch feature"},
		{"/stats\t\n", "/stats"},
		{"/contextManagementStrategy facts 12\n", "/contextManagementStrategy facts 12"},
	} {
		var out bytes.Buffer
		got, err := readEditedLineCompletions(bufio.NewReader(strings.NewReader(test.keys)), &out, false, false, agentCompleter(a))
		if err != nil || got != test.want {
			t.Fatalf("%q: got=%q want=%q err=%v", test.keys, got, test.want, err)
		}
		if !strings.Contains(out.String(), "↑/↓") {
			t.Fatal("menu not shown")
		}
	}
}
func TestBranchSuggestionsReflectCreatedBranches(t *testing.T) {
	a := NewAgent(&terminalFake{}, "", nil, "branches")
	complete := agentCompleter(a)
	mustBranch(t, a, "branch", "alpha")
	mustBranch(t, a, "branch", "beta")
	var out bytes.Buffer
	got, err := readEditedLineCompletions(bufio.NewReader(strings.NewReader("/branch \x1b[B\n")), &out, false, false, complete)
	if err != nil || got != "/branch beta" || !strings.Contains(out.String(), "beta (активная)") {
		t.Fatalf("%q %v %s", got, err, out.String())
	}
	got, err = readEditedLineCompletions(bufio.NewReader(strings.NewReader("/branch m\t\n")), &out, false, false, complete)
	if err != nil || got != "/branch main" {
		t.Fatalf("%q %v", got, err)
	}
}
func TestMenuNavigationKeepsHistoryAndEscapeDismisses(t *testing.T) {
	a := NewAgent(&terminalFake{}, "", nil, "menus")
	e := lineEditor{complete: agentCompleter(a), history: []string{"previous"}, position: 1}
	e.feed('/')
	for _, r := range "\x1b[B" {
		e.feed(r)
	}
	if e.position != 1 || string(e.text) != "/" || e.selected != 1 {
		t.Fatalf("menu arrow used history: %+v", e)
	}
	e.dismissMenu()
	if len(e.options) != 0 {
		t.Fatal("menu not dismissed")
	}
	for _, r := range "\x1b[A" {
		e.feed(r)
	}
	if string(e.text) != "previous" {
		t.Fatal("normal history not restored")
	}
	e.feed(21)
	e.feed('/')
	if len(e.options) == 0 {
		t.Fatal("new input did not reopen menu")
	}
	e.feed(27)
	e.feed('s')
	if string(e.text) != "/s" {
		t.Fatal("typing immediately after Esc lost a character")
	}
}
func TestMenuScrollAndCleanup(t *testing.T) {
	a := NewAgent(&terminalFake{}, "", nil, "menus")
	e := lineEditor{complete: agentCompleter(a)}
	e.feed('/')
	for i := 0; i < len(slashCommands)-1; i++ {
		for _, r := range "\x1b[B" {
			e.feed(r)
		}
	}
	var out bytes.Buffer
	rows, err := renderInputMenu(&out, &e, 0)
	if err != nil || rows > 7 || !strings.Contains(out.String(), "› /exit") || strings.Contains(out.String(), "/contextManagementStrategy") {
		t.Fatalf("menu did not scroll: %s", out.String())
	}
	e.dismissMenu()
	out.Reset()
	remaining, err := renderInputMenu(&out, &e, rows)
	if err != nil || remaining != 0 || strings.Count(out.String(), "\x1b[2K") != rows+1 {
		t.Fatalf("stale menu left on screen: %q", out.String())
	}
}
