package terminal

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintComparisonKeepsLinesAndUnicode(t *testing.T) {
	var out bytes.Buffer
	PrintComparison(&out, "БАЗОВЫЙ", "ЭКОНОМНЫЙ", "Первая строка\nВторая строка", "Ответ\nЕщё ответ")
	value := out.String()
	for _, expected := range []string{"БАЗОВЫЙ", "ЭКОНОМНЫЙ", "Первая строка", "Вторая строка", "Ещё ответ", "│"} {
		if !strings.Contains(value, expected) {
			t.Fatalf("нет %q в %q", expected, value)
		}
	}
}

func TestComparisonWrapsUnicodeAndPreservesLines(t *testing.T) {
	var out bytes.Buffer
	PrintComparison(&out, "ЛОКАЛЬНАЯ", "ОБЛАЧНАЯ", "Первая строка\n"+strings.Repeat("界", 35), "Ответ\nВторая строка", "Вопрос?", "Источники: policy.txt")
	value := out.String()
	if !strings.Contains(value, "Вопрос: Вопрос?") || !strings.Contains(value, "Источники: policy.txt") || !strings.Contains(value, "Первая строка") || !strings.Contains(value, "Вторая строка") {
		t.Fatal(value)
	}
	for _, line := range strings.Split(value, "\n") {
		if strings.Contains(line, " │ ") && len([]rune(line)) > 80 {
			t.Fatalf("панели вышли за ширину: %q", line)
		}
	}
}
