package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMarkdownChunkBoundaries(t *testing.T) {
	var out bytes.Buffer
	writer := &markdownWriter{out: &out, styled: true}
	for _, chunk := range []string{"# Заг", "оловок\n**жи", "рный** и `код`\n```go\n", "**literal**\n```\n"} {
		if err := writer.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\x1b[1;34mЗаголовок", "\x1b[1mжирный", "\x1b[36mкод", "**literal**"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}
