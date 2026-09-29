package main

import (
	"bytes"
	"strings"
	"testing"
)

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

func TestStartupChecksExplainMissingLocalDependencies(t *testing.T) {
	var out bytes.Buffer
	printStartupChecks(&out, "", "http://localhost:11434/api/embed", "", false)
	for _, want := range []string{"DEEPSEEK_API_KEY не задан", deepSeekKeysURL, "Локальный Ollama не найден", ollamaDownloadURL, "ollama pull embeddinggemma"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("startup output misses %q: %s", want, out.String())
		}
	}
}

func TestStartupChecksExplainMissingCloudToken(t *testing.T) {
	var out bytes.Buffer
	printStartupChecks(&out, "configured", "https://ollama.com/api/embed", "", false)
	if !strings.Contains(out.String(), "OLLAMA_API_KEY не задан") || !strings.Contains(out.String(), ollamaKeysURL) {
		t.Fatalf("unexpected startup output: %s", out.String())
	}
	if strings.Contains(out.String(), "Локальный Ollama") {
		t.Fatalf("cloud setup should not require a local executable: %s", out.String())
	}
}
