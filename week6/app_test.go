package week6

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestTerminalApplicationChatsAndResets(t *testing.T) {
	input := strings.NewReader("привет\nвторой вопрос\n/reset\nтретий вопрос\n/exit\n")
	var output bytes.Buffer
	client := &fakeLocal{}
	if err := RunWith(context.Background(), 27, client, "qwen3:4b-instruct", input, &output); err != nil {
		t.Fatal(err)
	}
	if len(client.last) != 1 {
		t.Fatalf("после /reset модель получила %d сообщений вместо одного", len(client.last))
	}
	if !strings.Contains(output.String(), "Новый диалог") || !strings.Contains(output.String(), "ответ") {
		t.Fatalf("неверный вывод приложения: %s", output.String())
	}
}
