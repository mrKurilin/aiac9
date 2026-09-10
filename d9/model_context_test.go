package main

import "testing"

func TestSupportedModelsProvideTheirOwnContextWindow(t *testing.T) {
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"} {
		limits, ok := limitsForModel(model)
		if !ok || limits.Context != 1_048_576 || limits.MaxOutput != 384_000 {
			t.Fatalf("%s: %+v, known=%v", model, limits, ok)
		}
	}
	if _, ok := limitsForModel("unknown"); ok {
		t.Fatal("unknown model received an invented context window")
	}
}
