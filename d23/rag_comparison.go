package main

import (
	"fmt"
	"io"
	"strings"
)

// Keep d22's two-pane layout, with labels and evidence for d23's two modes.
func printComparison(out io.Writer, c Comparison) {
	columns, _ := terminalSize(out)
	width := (columns - 3) / 2
	if width < 2 {
		width = 2
	}
	rows := func(left, right string) {
		leftLines, rightLines := comparisonLines(left, width), comparisonLines(right, width)
		for i := 0; i < len(leftLines) || i < len(rightLines); i++ {
			l, r := "", ""
			if i < len(leftLines) {
				l = leftLines[i]
			}
			if i < len(rightLines) {
				r = rightLines[i]
			}
			fmt.Fprintf(out, "%s │ %s\n", comparisonPad(l, width), comparisonPad(r, width))
		}
	}
	fmt.Fprintln(out)
	rows("БЕЗ rewrite/фильтра", "С rewrite/фильтром")
	fmt.Fprintf(out, "%s─┼─%s\n", strings.Repeat("─", width), strings.Repeat("─", width))
	rows(c.Base, c.Improved)
	fmt.Fprintf(out, "%s─┴─%s\n", strings.Repeat("─", width), strings.Repeat("─", width))
	fmt.Fprintf(out, "Вопрос: %s\nЗапрос после rewrite: %s\nКандидатов до фильтра: %d\nИсточники базового поиска:\n", c.Question, c.Rewritten, c.Candidates)
	printHits(out, c.Before)
	fmt.Fprintln(out, "Источники после фильтра:")
	printHits(out, c.After)
}

func comparisonLines(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{"—"}
	}
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		lines = append(lines, wrapLine([]rune(strings.ReplaceAll(line, "\t", "    ")), width)...)
	}
	return lines
}

func comparisonPad(text string, width int) string {
	for _, r := range text {
		width -= runeCells(r)
	}
	if width < 0 {
		width = 0
	}
	return text + strings.Repeat(" ", width)
}
