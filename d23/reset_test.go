package main

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

type recordingChat struct{ requests [][]Message }

func (c *recordingChat) Complete(_ context.Context, messages []Message) (string, error) {
	c.requests = append(c.requests, append([]Message(nil), messages...))
	return "Ответ", nil
}

func TestResetAliasesClearConversationAndScreenPreservingIndex(t *testing.T) {
	for _, command := range []string{"/reset", "/clear"} {
		t.Run(command, func(t *testing.T) {
			a, _ := testAgent(t)
			client := &recordingChat{}
			a.client = client
			for _, question := range []string{"первый вопрос", "второй вопрос"} {
				if _, _, err := a.Answer(context.Background(), question, nil); err != nil {
					t.Fatal(err)
				}
			}
			if len(client.requests[1]) != 3 || client.requests[1][0].Content != "первый вопрос" {
				t.Fatal("chat did not retain conversation")
			}
			before, err := os.ReadFile(a.indexPath())
			if err != nil {
				t.Fatal(err)
			}
			config := a.config
			a.rag = true
			var output bytes.Buffer
			footer, err := openBottomTerminal(&output, func() (int, int) { return 80, 24 })
			if err != nil {
				t.Fatal(err)
			}
			defer footer.Close()
			_, _ = footer.Write([]byte("старый ответ\n"))
			handled, exit := runCommand(context.Background(), a, command, footer)
			if !handled || exit || len(a.history) != 0 {
				t.Fatal("reset failed")
			}
			footer.mu.Lock()
			transcript := string(footer.transcript)
			footer.mu.Unlock()
			if strings.Contains(transcript, "старый ответ") || !strings.Contains(transcript, "Новый диалог") {
				t.Fatalf("transcript=%q", transcript)
			}
			after, err := os.ReadFile(a.indexPath())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || !reflect.DeepEqual(a.config, config) || !a.rag {
				t.Fatal("reset changed index or RAG settings")
			}
			a.rag = false
			if _, _, err := a.Answer(context.Background(), "новый вопрос", nil); err != nil {
				t.Fatal(err)
			}
			if len(client.requests[2]) != 1 {
				t.Fatal("old context sent after reset")
			}
			completion, ok := exactCommand(command)
			if !ok || !completion.Submit {
				t.Fatal("reset missing from executable completions")
			}
		})
	}
}
