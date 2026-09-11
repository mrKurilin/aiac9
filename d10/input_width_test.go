package main

import (
	"strings"
	"testing"
)

func TestInputUsesTerminalWidth(t *testing.T) {
	text := []rune(strings.Repeat("я", 100))
	if got := inputViewport(text, 117); got != string(text) {
		t.Fatal("wide input truncated")
	}
	if got := inputViewport(text, 77); len([]rune(got)) != 77 || !strings.HasPrefix(got, "…") {
		t.Fatal("wrong scroll width")
	}
	if got := inputViewport([]rune("你好世界"), 5); got != "…世界" {
		t.Fatalf("wide characters: %q", got)
	}
	if got := inputViewport(text, 0); got != "" {
		t.Fatal("zero width")
	}
	if got := inputViewport([]rune("е\u0301"), 1); got != "е\u0301" {
		t.Fatal("combining accent")
	}
}
