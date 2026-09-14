package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestTerminalMemoryWorkflow(t *testing.T) {
	agent := NewAgent(&captureClient{}, "", nil, "terminal")
	in := strings.NewReader("/remember working goal ship\nhello\n/memory working\n/forget working goal\n/exit\n")
	var out bytes.Buffer
	if err := runTerminal(context.Background(), agent, in, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Сохранено явно", "MrKai > Ответ", `"goal": "ship"`, "Удалено"} {
		if !strings.Contains(text, want) {
			t.Errorf("output misses %q:\n%s", want, text)
		}
	}
	if len(agent.Snapshot().Working) != 0 {
		t.Fatal("working entry remains")
	}
}
