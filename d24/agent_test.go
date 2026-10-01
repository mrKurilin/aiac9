package main

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

type recordingClient struct{ requests [][]Message }

func (c *recordingClient) Complete(_ context.Context, messages []Message) (string, error) {
	c.requests = append(c.requests, append([]Message(nil), messages...))
	return "Ответ", nil
}

type resetWriter struct {
	bytes.Buffer
	clears int
}

func (w *resetWriter) ClearChat() error { w.clears++; w.Buffer.Reset(); return nil }
func TestResetAliasesClearChatWithoutDeletingIndex(t *testing.T) {
	for _, command := range []string{"/reset", "/clear"} {
		t.Run(command, func(t *testing.T) {
			client := &recordingClient{}
			a := NewAgent(client, t.TempDir())
			if err := os.MkdirAll(a.indexDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(a.indexPath(), []byte(`{"version":1,"chunks":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			a.rag = false
			for _, q := range []string{"первый", "второй"} {
				if _, _, err := a.Answer(context.Background(), q, nil); err != nil {
					t.Fatal(err)
				}
			}
			if len(client.requests[1]) != 3 {
				t.Fatal("chat context not retained")
			}
			before, err := os.ReadFile(a.indexPath())
			if err != nil {
				t.Fatal(err)
			}
			a.rag = true
			config := a.config
			out := &resetWriter{}
			out.WriteString("старый ответ")
			handled, exit := runCommand(context.Background(), a, command, out)
			if !handled || exit || len(a.history) != 0 || out.clears != 1 || strings.Contains(out.String(), "старый ответ") {
				t.Fatal("reset failed")
			}
			after, err := os.ReadFile(a.indexPath())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || !reflect.DeepEqual(config, a.config) || !a.rag {
				t.Fatal("durable state changed")
			}
			a.rag = false
			if _, _, err := a.Answer(context.Background(), "новый", nil); err != nil {
				t.Fatal(err)
			}
			if len(client.requests[2]) != 1 {
				t.Fatal("old model context survived reset")
			}
		})
	}
}
func TestEveryCommandHasMetadata(t *testing.T) {
	descriptors := terminalCommands()
	if len(descriptors) != len(commands) {
		t.Fatal("metadata count differs")
	}
	for i, command := range commands {
		if descriptors[i].Value != command || descriptors[i].Description == "" {
			t.Errorf("missing metadata for %s", command)
		}
	}
}
