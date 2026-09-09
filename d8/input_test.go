package main

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestClearLineKeys(t *testing.T) {
	for _, key := range []string{"\x08", "\x15", "\x1b[127;5u", "\x1b[8;5u", "\x1b[27;5;127~", "\x1b[27;5;8~", "\x1b[3;5~"} {
		var out bytes.Buffer
		line, err := readEditedLine(bufio.NewReader(strings.NewReader("Удалить всё"+key+"новая строка\n")), &out)
		if err != nil || line != "новая строка" {
			t.Fatalf("%q: line=%q err=%v", key, line, err)
		}
	}
}
func TestBackspaceUnicodeAndEscape(t *testing.T) {
	var out bytes.Buffer
	line, err := readEditedLine(bufio.NewReader(strings.NewReader("Привет\x7f!\x1b[A\n")), &out)
	if err != nil || line != "Приве!" {
		t.Fatalf("line=%q err=%v", line, err)
	}
}
func TestTerminalSectionsInOrder(t *testing.T) {
	var out bytes.Buffer
	a := NewAgent(&Meter{Client: &terminalFake{}}, "system", nil, "test")
	if err := runTerminal(context.Background(), a, strings.NewReader(""), &out, "hi"); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	last := -1
	for _, part := range []string{"hi", "Запрос ≈", "Ответ", "Токены (оценка)", "за запуск"} {
		i := strings.Index(text, part)
		if i <= last {
			t.Fatalf("wrong section order: %s\n%s", part, text)
		}
		last = i
	}
}

func TestHistoryNavigationAndDraft(t *testing.T) {
	var out bytes.Buffer
	keys := "черновик\x1b[A\x1b[A\x1b[A\x1b[B\x1b[B\n"
	got, err := readEditedLine(bufio.NewReader(strings.NewReader(keys)), &out, "первое", "второе")
	if err != nil || got != "черновик" {
		t.Fatalf("%q %v", got, err)
	}
	for _, keys := range []string{"\x1b[A\n", "\x1bOA\n"} {
		got, err = readEditedLine(bufio.NewReader(strings.NewReader(keys)), &out, "первое", "второе")
		if err != nil || got != "второе" {
			t.Fatalf("%q %v", got, err)
		}
	}
	got, err = readEditedLine(bufio.NewReader(strings.NewReader("\x1b[A!\n")), &out, "текст")
	if err != nil || got != "текст!" {
		t.Fatalf("%q %v", got, err)
	}
	if strings.Contains(out.String(), "Вы>") {
		t.Fatal("old prompt")
	}
}
