package terminal

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

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
func TestEscapeCancelsSlowOperationAndSecondEscapeExits(t *testing.T) {
	var output bytes.Buffer
	footer, err := openBottomTerminal(&output, func() (int, int) { return 80, 24 })
	if err != nil {
		t.Fatal(err)
	}
	defer footer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	handle := func(ctx context.Context, _ string, _ io.Writer) bool {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return false
	}
	in := make(timedInput, 1024)
	done := make(chan error, 1)
	go func() { done <- runInteractive(ctx, bufio.NewReader(in), footer, handle) }()
	send := func(s string) {
		for _, b := range []byte(s) {
			in <- b
		}
	}
	send("вопрос\r")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("operation did not start")
	}
	send("\x1b[27u")
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Esc did not cancel")
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
		t.Fatal("missing cancellation log")
	}
}
