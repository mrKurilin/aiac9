package main

import "testing"

func TestBuiltInScenarioDoesNotDependOnParentRepository(t *testing.T) {
	scenario := &script{}
	if err := scenario.load(); err != nil {
		t.Fatal(err)
	}
	if len(scenario.blocks) != 15 {
		t.Fatalf("built-in scenario has %d messages, want 15", len(scenario.blocks))
	}
	if scenario.blocks[0] == "" || scenario.blocks[14] == "" {
		t.Fatal("built-in scenario contains an empty boundary message")
	}
}
