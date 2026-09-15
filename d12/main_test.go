package main

import "testing"

func TestLaunchModes(t *testing.T) {
	for _, args := range [][]string{nil, {"web"}} {
		if err := validateLaunchArgs(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"-cli"}, {"web", "extra"}} {
		if validateLaunchArgs(args) == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
