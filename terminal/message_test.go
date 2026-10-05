package terminal

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestMessageBlocksKeepMultilineCodeAndDiagnosticsSeparate(t *testing.T) {
	var out bytes.Buffer
	PrintMessage(&out, "ВЫ", "Напиши Kotlin")
	PrintDiagnostic(&out, "Ollama: отправляю запрос")
	PrintMessage(&out, "МОДЕЛЬ · qwen3:4b-instruct", "```kotlin\nfun sort() {}\n```")
	PrintDiagnostic(&out, "Ollama: готово, 24 токена")
	got := out.String()
	for _, want := range []string{
		"╭─ ВЫ\n│ Напиши Kotlin\n╰─",
		"  · Ollama: отправляю запрос",
		"╭─ МОДЕЛЬ · qwen3:4b-instruct\n│ ```kotlin\n│ fun sort() {}\n│ ```\n╰─",
		"  · Ollama: готово, 24 токена",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestLineModeEchoesUserMessageAsBlock(t *testing.T) {
	var out bytes.Buffer
	err := lineMode(context.Background(), strings.NewReader("Привет\n/exit\n"), &out, func(_ context.Context, line string, outWriter io.Writer) bool {
		if line == "/exit" {
			return true
		}
		PrintMessage(outWriter, "МОДЕЛЬ", "Привет!")
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "╭─ ВЫ\n│ Привет\n╰─") || !strings.Contains(got, "╭─ МОДЕЛЬ\n│ Привет!\n╰─") {
		t.Fatalf("message blocks missing: %q", got)
	}
}
