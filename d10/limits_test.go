package main

import (
	"strings"
	"testing"
)

func TestLengthDiagnosticOutputLimit(t *testing.T) {
	client := &DeepSeekClient{Model: "deepseek-v4-flash", MaxTokens: 1024}
	got := lengthDiagnostic(client, Usage{Prompt: 6457, Completion: 1024})
	for _, want := range []string{"достигнут лимит ответа max_tokens=1024", "Ответ: 1024/1024", "7481/1048576", "максимум ответа модели 384000"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestLengthDiagnosticContextLimit(t *testing.T) {
	client := &DeepSeekClient{Model: "deepseek-v4-pro", MaxTokens: 384000}
	got := lengthDiagnostic(client, Usage{Prompt: 1_048_476, Completion: 100})
	if !strings.Contains(got, "достигнут лимит контекста модели=1048576") {
		t.Fatal(got)
	}
}

func TestDeepSeekContextErrorExplainsExactCount(t *testing.T) {
	err := deepSeekHTTPError(400, "This model's maximum context length is 1048576 tokens. However, you requested 1600979 tokens (1600979 in the messages, 0 in the completion). Please reduce the length of the messages or completion.")
	for _, want := range []string{"сообщения 1600979 + ответ 0 = 1600979", "лимит модели 1048576", "одного текущего запроса", "точнее локальной оценки"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %q", want, err)
		}
	}
}

func TestLengthDiagnosticDoesNotInventCause(t *testing.T) {
	client := &DeepSeekClient{Model: "custom", MaxTokens: 1000}
	got := lengthDiagnostic(client, Usage{Prompt: 100, Completion: 500})
	if !strings.Contains(got, "не передал, какой именно лимит") || !strings.Contains(got, "лимит модели неизвестен") {
		t.Fatal(got)
	}
}
