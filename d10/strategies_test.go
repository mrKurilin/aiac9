package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func mustAsk(t *testing.T, a *Agent, prompt string) string {
	t.Helper()
	answer, err := a.Ask(context.Background(), prompt)
	if err != nil {
		t.Fatal(err)
	}
	return answer
}
func mustConfigure(t *testing.T, a *Agent, mode string, n int) {
	t.Helper()
	if err := a.Configure(mode, n); err != nil {
		t.Fatal(err)
	}
}
func mustBranch(t *testing.T, a *Agent, command string, args ...string) {
	t.Helper()
	if err := a.BranchCommand(command, args...); err != nil {
		t.Fatal(err)
	}
}
func TestWindowDiscardsMessagesAndPersistsExactBound(t *testing.T) {
	for _, n := range []int{0, 1, 3, 4} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			store, err := NewJSONFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			client := &terminalFake{}
			a := NewAgent(client, "system", store, "window")
			mustConfigure(t, a, "window", n)
			for _, prompt := range []string{"first", "second", "third", "fourth"} {
				mustAsk(t, a, prompt)
			}
			restored := NewAgent(client, "system", store, "window")
			if len(restored.History()) != n || restored.keepLast != n {
				t.Fatalf("restored %+v", restored.snapshot())
			}
			last := client.calls[len(client.calls)-1]
			want := n + 1
			if n == 0 {
				want = 2
			}
			if len(last) != want || last[len(last)-1].Content != "fourth" || strings.Contains(fmt.Sprint(last), "first") {
				t.Fatalf("request %+v", last)
			}
			if len(restored.branches) != 0 || len(restored.checkpoints) != 0 {
				t.Fatal("hidden archive")
			}
		})
	}
}
func TestFactsUpdateEachTurnAndSurviveWindowAndRestart(t *testing.T) {
	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meter := &Meter{Client: rehearsalClient{}}
	a := NewAgent(meter, "system", store, "facts")
	a.extractor = LLMFacts{meter}
	mustConfigure(t, a, "facts", 2)
	for _, prompt := range comparisonScenario {
		mustAsk(t, a, prompt)
	}
	if meter.Turns != 24 {
		t.Fatalf("facts overhead not metered: %d", meter.Turns)
	}
	restored := NewAgent(meter, "system", store, "facts")
	if restored.facts["Бюджет"] != "80000 рублей" || restored.facts["Срок"] != "15 декабря" || len(restored.History()) != 2 || restored.mode != "facts" {
		t.Fatalf("restored %+v", restored.snapshot())
	}
	messages := restored.contextMessages()
	if messages[0].Content != "system" || !strings.HasPrefix(messages[1].Content, factsContextPrefix) {
		t.Fatalf("facts block %+v", messages)
	}
}

type factFunc func(context.Context, map[string]string, []Message, string) (map[string]string, error)

func (f factFunc) Update(c context.Context, m map[string]string, h []Message, p string) (map[string]string, error) {
	return f(c, m, h, p)
}

type failingStore struct {
	state ConversationState
	fail  bool
}

func (s *failingStore) Load(string) (ConversationState, error) { return s.state, nil }
func (s *failingStore) Save(_ string, state ConversationState) error {
	if s.fail {
		return fmt.Errorf("disk failure")
	}
	s.state = cloneState(state)
	return nil
}
func TestFailedTurnRollsBackFactsHistoryAndDisk(t *testing.T) {
	for _, failure := range []string{"answer", "emit", "save"} {
		t.Run(failure, func(t *testing.T) {
			store := &failingStore{}
			client := &terminalFake{}
			a := NewAgent(client, "system", store, "rollback")
			a.extractor = factFunc(func(_ context.Context, f map[string]string, _ []Message, p string) (map[string]string, error) {
				f["goal"] = p
				if p == "extract" {
					return nil, fmt.Errorf("extract failed")
				}
				return f, nil
			})
			mustConfigure(t, a, "facts", 2)
			mustAsk(t, a, "before")
			before := a.snapshot()
			persisted := cloneState(store.state)
			prompt := failure
			if failure == "answer" {
				prompt = "fail"
			}
			store.fail = failure == "save"
			var emit func(string) error
			if failure == "emit" {
				emit = func(string) error { return fmt.Errorf("writer failed") }
			}
			if _, err := a.AskStream(context.Background(), prompt, emit); err == nil {
				t.Fatal("expected failure")
			}
			if !reflect.DeepEqual(before, a.snapshot()) || !reflect.DeepEqual(persisted, store.state) {
				t.Fatal("failed turn changed memory")
			}
		})
	}
}

// A facts update that cannot be parsed keeps the previous facts and still
// answers: the question never reaches the model otherwise.
func TestFailedFactsExtractionKeepsTheTurn(t *testing.T) {
	store := &failingStore{}
	a := NewAgent(&terminalFake{}, "system", store, "extract")
	a.extractor = factFunc(func(_ context.Context, _ map[string]string, _ []Message, _ string) (map[string]string, error) {
		return nil, fmt.Errorf("ожидался JSON-объект facts")
	})
	mustConfigure(t, a, "facts", 2)
	answer, err := a.AskStream(context.Background(), "сколько стоит?", nil)
	if err != nil || answer == "" {
		t.Fatalf("answer lost to a facts failure: %q %v", answer, err)
	}
	if snapshot := a.snapshot(); len(snapshot.Facts) != 0 || len(snapshot.History) != 2 {
		t.Fatalf("unexpected memory: facts %v history %d", snapshot.Facts, len(snapshot.History))
	}
	if warning := a.DrainFactsWarning(); warning == "" {
		t.Fatal("skipped facts update was not reported")
	}
	if warning := a.DrainFactsWarning(); warning != "" {
		t.Fatal("warning reported twice")
	}
}
func TestBranchCheckpointIsImmutableAndBranchesSurviveRestart(t *testing.T) {
	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := NewAgent(rehearsalClient{}, "system", store, "branches")
	mustConfigure(t, a, "window", 24)
	mustAsk(t, a, "Бюджет: 80000 рублей")
	mustBranch(t, a, "checkpoint", "base")
	mustBranch(t, a, "branch", "economy", "base")
	mustAsk(t, a, "Бюджет: 50000 рублей")
	mustBranch(t, a, "branch", "extended", "base")
	mustAsk(t, a, "Бюджет: 150000 рублей")
	a = NewAgent(rehearsalClient{}, "system", store, "branches")
	for _, test := range []struct{ name, budget string }{{"economy", "50000"}, {"extended", "150000"}, {"main", "80000"}} {
		mustBranch(t, a, "switch", test.name)
		answer := mustAsk(t, a, comparisonScenario[11])
		if !strings.Contains(answer, test.budget) {
			t.Fatalf("%s: %s", test.name, answer)
		}
	}
	if len(a.checkpoints["base"].History) != 2 {
		t.Fatal("checkpoint mutated")
	}
	for _, command := range []struct {
		cmd  string
		args []string
	}{{"checkpoint", []string{"base"}}, {"branch", []string{"economy", "base"}}, {"switch", []string{"missing"}}, {"branch", []string{"bad", "missing"}}, {"checkpoint", []string{"../bad"}}} {
		before := a.snapshot()
		if a.BranchCommand(command.cmd, command.args...) == nil {
			t.Fatal("accepted invalid command")
		}
		if !reflect.DeepEqual(before, a.snapshot()) {
			t.Fatal("invalid command mutated state")
		}
	}
	mustConfigure(t, a, "window", 2)
	if len(a.History()) != 2 || len(a.branches) != 2 || len(a.checkpoints) != 1 {
		t.Fatal("strategy switch lost branches")
	}
	mustBranch(t, a, "checkpoint", "in-window")
}

type replyClient string

func (r replyClient) Complete(context.Context, []Message) (string, error) { return string(r), nil }
func TestFactJSONValidationAndDeletion(t *testing.T) {
	for _, reply := range []string{"null", "[]", "text", "{\"\":\"value\"}", "{\"goal\":\"\"}"} {
		if _, err := (LLMFacts{replyClient(reply)}).Update(context.Background(), map[string]string{}, nil, "hello"); err == nil {
			t.Fatalf("accepted %q", reply)
		}
	}
	facts, err := (LLMFacts{replyClient(`{}`)}).Update(context.Background(), map[string]string{"deadline": "Friday"}, nil, "Отмени срок")
	if err != nil || len(facts) != 0 {
		t.Fatal("deleted fact survived")
	}
	// Models fence their JSON and answer with numbers; neither should cost the
	// whole update.
	for reply, want := range map[string]string{
		"```json\n{\"budget\": \"120000\"}\n```":      "120000",
		"Вот обновлённые facts: {\"budget\": 120000}": "120000",
		`{"budget": true}`: "true",
	} {
		facts, err := (LLMFacts{replyClient(reply)}).Update(context.Background(), nil, nil, "бюджет")
		if err != nil || facts["budget"] != want {
			t.Fatalf("reply %q gave %v (%v)", reply, facts, err)
		}
	}
}
func TestDamagedStoreIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := store.path("bad")
	original := []byte(`{"version":99}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	a := NewAgent(&terminalFake{}, "", store, "bad")
	if a.Configure("window", 4) == nil || a.Reset() == nil {
		t.Fatal("ignored load error")
	}
	if _, err := a.Ask(context.Background(), "hello"); err == nil {
		t.Fatal("ignored load error on ask")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("damaged store overwritten")
	}
	if _, err := store.Load("../bad"); err == nil {
		t.Fatal("path traversal")
	}
}
func TestScenarioComparison(t *testing.T) {
	results, err := compareScenario(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatal(results)
	}
	for i, want := range []int{0, 6, 6} {
		r := results[i]
		if r.Retained != want || r.Input <= 0 || r.Output <= 0 {
			t.Fatalf("unexpected comparison %+v", r)
		}
		t.Logf("%s: %d/6; calls=%d; estimated input=%d output=%d; answer=%s", r.Mode, r.Retained, r.Calls, r.Input, r.Output, r.Answer)
	}
	if results[1].Calls != 2*results[0].Calls || results[2].Input <= results[0].Input {
		t.Fatal("wrong cost accounting")
	}
}
func TestTerminalStrategyCommandsDoNotCallAPI(t *testing.T) {
	client := &terminalFake{}
	a := NewAgent(client, "", nil, "commands")
	var out bytes.Buffer
	input := "/contextManagementStrategy facts\n/checkpoint base\n/addBranch left base\n/addBranch right base\n/branch left\n/branch\n/facts\n/memory\n/contextManagementStrategy window\n/unknown\n/exit\n"
	if err := runTerminal(context.Background(), a, strings.NewReader(input), &out, ""); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 0 || a.mode != "window" {
		t.Fatal("commands called API or failed")
	}
	for _, want := range []string{"left (активная)", "right", "base", "Неизвестная команда"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
}

func TestSinglePromptCommandsNeverReachModel(t *testing.T) {
	client := &terminalFake{}
	a := NewAgent(client, "", nil, "single")
	for _, command := range []string{"/stats", "/reset", "/exit", "/unknown"} {
		var out bytes.Buffer
		err := runTerminal(context.Background(), a, strings.NewReader(""), &out, command)
		if (err != nil) != (command == "/unknown") {
			t.Fatalf("%s: %v", command, err)
		}
	}
	if len(client.calls) != 0 {
		t.Fatal("command sent to LLM")
	}
}
func TestFillPreservesBranchWorkspaceAndBoundsWindow(t *testing.T) {
	a := NewAgent(&Meter{Client: &terminalFake{}, ContextLimit: 1000}, "", nil, "fill")
	mustConfigure(t, a, "window", 24)
	mustAsk(t, a, "base")
	mustBranch(t, a, "checkpoint", "base")
	mustBranch(t, a, "branch", "left", "base")
	if _, err := a.FillContext(20); err != nil {
		t.Fatal(err)
	}
	mustBranch(t, a, "switch", "main")
	if len(a.History()) != 2 || len(a.branches["left"].History) != 3 {
		t.Fatal("fill crossed branch boundary")
	}
	mustConfigure(t, a, "window", 1)
	if _, err := a.FillContext(20); err != nil {
		t.Fatal(err)
	}
	if len(a.History()) != 1 {
		t.Fatal("fill bypassed window")
	}
	mustConfigure(t, a, "window", 0)
	if _, err := a.FillContext(20); err == nil {
		t.Fatal("pretended to retain fill with N=0")
	}
}

func TestBranchesHaveIndependentStrategiesAndWindows(t *testing.T) {
	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := NewAgent(rehearsalClient{}, "system", store, "settings")
	a.extractor = LLMFacts{rehearsalClient{}}
	mustConfigure(t, a, "facts", 4)
	mustAsk(t, a, "Бюджет: 80000 рублей")
	mustBranch(t, a, "branch", "alternative") // no checkpoint needed
	mustConfigure(t, a, "window", 2)
	mustAsk(t, a, "Бюджет: 50000 рублей")
	mustBranch(t, a, "switch", "main")
	if a.mode != "facts" || a.keepLast != 4 || a.facts["Бюджет"] != "80000 рублей" {
		t.Fatal("branch settings leaked")
	}
	mustAsk(t, a, "Бюджет: 90000 рублей")
	restored := NewAgent(rehearsalClient{}, "system", store, "settings")
	mustBranch(t, restored, "switch", "alternative")
	if restored.mode != "window" || restored.keepLast != 2 || len(restored.facts) != 0 || !strings.Contains(fmt.Sprint(restored.History()), "50000") {
		t.Fatalf("restored %+v", restored.snapshot())
	}
	mustBranch(t, restored, "switch", "main")
	if restored.facts["Бюджет"] != "90000 рублей" {
		t.Fatal("main not persisted")
	}
}
func TestBranchSaveFailureDoesNotSwitchOrEraseMemory(t *testing.T) {
	store := &failingStore{}
	a := NewAgent(&terminalFake{}, "", store, "save")
	mustAsk(t, a, "one")
	before := a.snapshot()
	store.fail = true
	if a.BranchCommand("branch", "new") == nil {
		t.Fatal("ignored disk failure")
	}
	if !reflect.DeepEqual(before, a.snapshot()) {
		t.Fatal("failed branch creation changed memory")
	}
}
func TestVersionOneBranchingMigratesWithoutLosingBranches(t *testing.T) {
	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	history := []Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "two"}, {Role: "user", Content: "three"}}
	old := ConversationState{Version: 1, Mode: "branching", KeepLast: 1, Active: "main", History: history, Facts: map[string]string{}, Branches: map[string]Memory{"other": {History: history, Facts: map[string]string{}}}, Checkpoints: map[string]Memory{"base": {History: history, Facts: map[string]string{}}}}
	if err := store.Save("legacy", old); err != nil {
		t.Fatal(err)
	}
	a := NewAgent(&terminalFake{}, "", store, "legacy")
	if a.loadErr != nil || a.mode != "window" || a.keepLast != 3 || len(a.History()) != 3 {
		t.Fatalf("migration failed: %+v %v", a.snapshot(), a.loadErr)
	}
	mustBranch(t, a, "switch", "other")
	if a.keepLast != 3 || len(a.History()) != 3 {
		t.Fatal("branch lost during migration")
	}
	mustBranch(t, a, "branch", "from-checkpoint", "base")
	if len(a.History()) != 3 {
		t.Fatal("checkpoint lost during migration")
	}
	restored := NewAgent(&terminalFake{}, "", store, "legacy")
	if restored.loadErr != nil || restored.active != "from-checkpoint" {
		t.Fatal("migrated session not persisted")
	}
}

func TestDefaultWindowAndStrategyCommandUseFiveMessages(t *testing.T) {
	a := NewAgent(&terminalFake{}, "", nil, "default")
	if a.keepLast != 5 {
		t.Fatalf("new session N=%d", a.keepLast)
	}
	for _, mode := range []string{"window", "facts"} {
		mustConfigure(t, a, mode, 12)
		var out bytes.Buffer
		if !memoryCommand(a, "/contextManagementStrategy "+mode, &out) || a.keepLast != 5 {
			t.Fatalf("omitted N did not reset to five: %s", out.String())
		}
		memoryCommand(a, "/contextManagementStrategy "+mode+" 6", &out)
		if a.keepLast != 6 {
			t.Fatal("explicit N ignored")
		}
	}
}
