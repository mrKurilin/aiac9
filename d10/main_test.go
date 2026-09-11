package main

import "testing"

func TestLaunchAcceptsNoArguments(t *testing.T) {
	if err := validateLaunchArgs(nil); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-help"}, {"-session", "study"}, {"-prompt", "hello"}, {"-cli"}, {"hello"}} {
		if validateLaunchArgs(args) == nil {
			t.Fatalf("accepted launch arguments: %v", args)
		}
	}
}
func TestOptionalPrices(t *testing.T) {
	const name = "MRKAI_TEST_PRICE"
	if value, err := optionalPrice("MRKAI_TEST_UNSET_PRICE"); value != nil || err != nil {
		t.Fatal("missing price should use model tariff")
	}
	for _, raw := range []string{"-1", "NaN", "Inf", "", "invalid"} {
		t.Setenv(name, raw)
		if _, err := optionalPrice(name); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"0", "0.25"} {
		t.Setenv(name, raw)
		if value, err := optionalPrice(name); err != nil || value == nil {
			t.Fatalf("rejected %q: %v", raw, err)
		}
	}
}
