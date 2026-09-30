package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

type comparisonOutput struct {
	bytes.Buffer
	columns int
}

func (o *comparisonOutput) terminalSize() (int, int) { return o.columns, 24 }

func TestComparisonKeepsMultilineUnicodeAnswersInTheirPanes(t *testing.T) {
	for _, columns := range []int{24, 40, 80, 120} {
		t.Run(fmt.Sprint(columns), func(t *testing.T) {
			out := &comparisonOutput{columns: columns}
			c := Comparison{Question: "Сколько дней?", Rewritten: "PTO", Base: "Краткий ответ.\nВторая строка.", Improved: strings.Repeat("Длинный ответ 界🙂 е́. ", 8)}
			printComparison(out, c)
			var left, right strings.Builder
			body := false
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.Contains(line, "┴") {
					break
				}
				if strings.Contains(line, "┼") {
					body = true
					continue
				}
				if line == "" {
					continue
				}
				parts := strings.Split(line, " │ ")
				if len(parts) != 2 {
					t.Fatalf("missing divider: %q", line)
				}
				cells, leftCells := 0, 0
				for _, r := range line {
					cells += runeCells(r)
				}
				for _, r := range parts[0] {
					leftCells += runeCells(r)
				}
				if cells > columns || leftCells != (columns-3)/2 {
					t.Fatalf("misaligned %d-column row: %q", columns, line)
				}
				if body {
					left.WriteString(strings.TrimSpace(parts[0]))
					right.WriteString(strings.TrimSpace(parts[1]))
				}
			}
			compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
			if compact(left.String()) != compact(c.Base) || compact(right.String()) != compact(c.Improved) {
				t.Fatal("answer text lost or moved to the other pane")
			}
			for _, want := range []string{"Вопрос: " + c.Question, "Запрос после rewrite: PTO", "Источники базового поиска:", "Источники после фильтра:"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q", want)
				}
			}
		})
	}
}

func TestComparisonBlankAndTabbedAnswer(t *testing.T) {
	if got := comparisonLines("  \n", 20); len(got) != 1 || got[0] != "—" {
		t.Fatalf("empty answer: %v", got)
	}
	if got := strings.Join(comparisonLines("a\tb", 20), ""); got != "a    b" {
		t.Fatalf("tabs: %q", got)
	}
}
