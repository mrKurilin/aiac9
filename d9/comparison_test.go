package main

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestComparisonBroadcastCompressionAndCommands(t *testing.T) {
	leftClient, rightClient := &terminalFake{}, &terminalFake{}
	leftMeter := &Meter{Client: leftClient, ContextLimit: 10000}
	rightMeter := &Meter{Client: rightClient, ContextLimit: 10000}
	left := NewAgent(leftMeter, "system", nil, "full")
	right := NewAgent(rightMeter, "system", nil, "compact")
	if err := right.ConfigureCompression(CompressionConfig{Enabled: true, KeepLast: 2, SummaryEvery: 2}, &recordingSummarizer{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	view := newComparisonView(left, right, &out)
	prompt := strings.Repeat("длинное описание проекта ", 30)
	input := strings.NewReader(prompt + "\n" + prompt + "\nпроверь факты\n/exit\n")
	if err := runTerminalView(context.Background(), left, input, &out, "", view); err != nil {
		t.Fatal(err)
	}
	if len(leftClient.calls) != 3 || len(rightClient.calls) != 3 {
		t.Fatal("not broadcast to both agents")
	}
	if left.Summary() != "" || len(left.History()) != 6 {
		t.Fatal("left history compressed")
	}
	if right.Summary() == "" || len(right.History()) != 4 {
		t.Fatal("right history not compressed")
	}
	if leftMeter.Input <= rightMeter.Input {
		t.Fatal("compression did not reduce input")
	}
	for _, value := range []string{"БЕЗ СЖАТИЯ", "СО СЖАТИЕМ", "Контекст", "Стоимость", "Сжатий 1"} {
		if !strings.Contains(out.String(), value) {
			t.Fatalf("missing %q", value)
		}
	}
	view.command("/compress off")
	if !right.CompressionSnapshot().Config.Enabled || left.CompressionSnapshot().Config.Enabled {
		t.Fatal("comparison modes changed")
	}
	view.command("/fill 1")
	if left.fillerDetails().Blocks != 1 || right.fillerDetails().Blocks != 1 {
		t.Fatal("fill not broadcast")
	}
	view.command("/overflow off")
	if leftMeter.AllowOverflow || rightMeter.AllowOverflow {
		t.Fatal("overflow not broadcast")
	}
	view.command("/reset")
	if len(left.History()) != 0 || len(right.History()) != 0 || right.Summary() != "" {
		t.Fatal("reset not broadcast")
	}
	if leftMeter.Turns != 3 || rightMeter.Turns != 3 {
		t.Fatal("reset lost spent tokens")
	}
}

type barrierClient struct{ entered chan<- struct{} }

func (c barrierClient) Complete(ctx context.Context, _ []Message) (string, error) {
	c.entered <- struct{}{}
	<-ctx.Done()
	return "", ctx.Err()
}

func TestComparisonConcurrentCancellation(t *testing.T) {
	entered := make(chan struct{}, 2)
	left := NewAgent(&Meter{Client: barrierClient{entered}}, "", nil, "left")
	right := NewAgent(&Meter{Client: barrierClient{entered}}, "", nil, "right")
	var out bytes.Buffer
	v := newComparisonView(left, right, &out)
	// Exercise the live render ticker under the race detector.
	v.interactive = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- v.ask(ctx, "hello") }()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("requests were not concurrent")
		}
	}
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("requests did not stop")
	}
	if len(left.History()) != 0 || len(right.History()) != 0 {
		t.Fatal("cancelled turn saved")
	}
	if strings.Contains(out.String(), "\x1b[2J") {
		t.Fatal("streaming clears the entire screen")
	}
}

func TestComparisonPaintOnlyChangedRows(t *testing.T) {
	var out bytes.Buffer
	v := &comparisonView{out: &out}
	v.paint([]string{"first", "second"}, 80, 24)
	out.Reset()
	v.paint([]string{"first", "second"}, 80, 24)
	if out.Len() != 0 {
		t.Fatal("unchanged frame was repainted")
	}
	v.paint([]string{"first", "updated"}, 80, 24)
	if strings.Contains(out.String(), "first") || !strings.Contains(out.String(), "\x1b[2;1Hupdated\x1b[K") {
		t.Fatal("wrong row updated:", out.String())
	}
	if strings.ContainsAny(out.String(), "\n\r") || strings.Contains(out.String(), "\x1b[2J") {
		t.Fatal("paint can scroll or blank the screen")
	}
}

func TestComparisonRoleBlocksAndFixedInput(t *testing.T) {
	p := &comparisonPane{}
	p.message("user", "Первый вопрос\nсо второй строкой")
	p.message("assistant", "Первый ответ")
	p.message("user", "Второй вопрос")
	p.message("assistant", "Второй ответ")
	lines := p.lines(24)
	text := strings.Join(lines, "\n")
	previous := -1
	for _, expected := range []string{"┃ ВЫ", "┃ Первый вопрос", "┃ со второй строкой", "МОДЕЛЬ", "Первый ответ", "┃ Второй вопрос", "Второй ответ"} {
		position := strings.Index(text[previous+1:], expected)
		if position < 0 {
			t.Fatalf("missing or reordered %q: %s", expected, text)
		}
		previous += position + 1
	}
	var out bytes.Buffer
	line, err := readEditedLineLayout(bufio.NewReader(strings.NewReader("тест\n")), &out, false, true)
	if err != nil || line != "тест" {
		t.Fatal(line, err)
	}
	if strings.Contains(out.String(), "\n") || strings.Contains(out.String(), "\x1b[1A") || strings.Contains(out.String(), "ВВОД") {
		t.Fatal("fixed input shifts the conversation")
	}
}

func TestPaneWrapUnicodeAndControls(t *testing.T) {
	for _, line := range paneLines("Привет 世界🙂\n\x1b[2Jввод\tтекст", 8) {
		cells := 0
		for _, r := range line {
			if r == 27 {
				t.Fatal("escape sequence leaked")
			}
			cells += runeCells(r)
		}
		if cells > 8 {
			t.Fatalf("wide line %q", line)
		}
	}
}
