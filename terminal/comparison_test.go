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
