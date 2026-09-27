package main

import "testing"

func TestLaunchAcceptsOnlyTerminalMode(t *testing.T) {
	if err := validateLaunchArgs(nil); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"web"}, {"-demo"}, {"extra"}} {
		if validateLaunchArgs(args) == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
