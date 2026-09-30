package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// Match the tty's timed empty reads without depending on a physical terminal.
type timedInput chan byte

func (in timedInput) Read(p []byte) (int, error) {
	select {
	case b := <-in:
		p[0] = b
		return 1, nil
	case <-time.After(10 * time.Millisecond):
		return 0, io.EOF
	}
}

type cancellingClient struct{ started, cancelled chan struct{} }

func (c cancellingClient) Complete(ctx context.Context, _ []Message) (string, error) {
	close(c.started)
	<-ctx.Done()
	close(c.cancelled)
	return "", ctx.Err()
}

func TestEscapeCancelsActiveModelAndSecondEscapeExits(t *testing.T) {
	var output bytes.Buffer
	footer, err := openBottomTerminal(&output, func() (int, int) { return 80, 24 })
	if err != nil {
		t.Fatal(err)
	}
	defer footer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := cancellingClient{make(chan struct{}), make(chan struct{})}
	a := NewAgent(client, t.TempDir())
	in := make(timedInput, 1024)
	done := make(chan error, 1)
	go func() { done <- runInteractive(ctx, a, bufio.NewReader(in), footer) }()
	defer func() { cancel() }()
	send := func(s string) {
		for _, b := range []byte(s) {
			in <- b
		}
	}
	send("вопрос\r")
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("model not called")
	}
	send("\x1b[27u")
	select {
	case <-client.cancelled:
	case <-time.After(time.Second):
		t.Fatal("Esc did not cancel model")
	}
	send("\x1b[27u")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Esc did not exit")
	}
	footer.mu.Lock()
	transcript := string(footer.transcript)
	footer.mu.Unlock()
	if !strings.Contains(transcript, "останавливаю") {
		t.Fatalf("missing cancellation diagnostic: %s", transcript)
	}
}
