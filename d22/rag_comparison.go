package main

import (
	"fmt"
	"io"
	"strings"
)

// printRAGComparison renders the result of /rag compare as two panes.  The
// question is shared deliberately: the only variable between the panes is the
// retrieved context supplied to the right-hand request.
func printRAGComparison(out io.Writer, question, withoutRAG, withRAG string, hits []IndexHit) {
	columns, _ := terminalSize(out)
	width := maxRAGComparison(20, (columns-3)/2)
	left := ragComparisonLines(withoutRAG, width)
	right := ragComparisonLines(withRAG, width)

	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "%s │ %s\n", ragComparisonPad("БЕЗ RAG", width), ragComparisonPad("С RAG", width))
	fmt.Fprintf(out, "%s─┼─%s\n", strings.Repeat("─", width), strings.Repeat("─", width))
	for row := 0; row < maxRAGComparison(len(left), len(right)); row++ {
		leftLine, rightLine := "", ""
		if row < len(left) {
			leftLine = left[row]
		}
		if row < len(right) {
			rightLine = right[row]
		}
		fmt.Fprintf(out, "%s │ %s\n", ragComparisonPad(leftLine, width), ragComparisonPad(rightLine, width))
	}
	fmt.Fprintf(out, "%s─┴─%s\n", strings.Repeat("─", width), strings.Repeat("─", width))
	fmt.Fprintf(out, "Вопрос: %s\nИсточники RAG:\n", question)
	printIndexHits(out, hits)
}

func ragComparisonLines(text string, width int) []string {
	var lines []string
	for _, sourceLine := range strings.Split(strings.TrimSpace(text), "\n") {
		var line strings.Builder
		cells := 0
		for _, r := range strings.ReplaceAll(sourceLine, "\t", "    ") {
			runeWidth := runeCells(r)
			if cells > 0 && cells+runeWidth > width {
				lines = append(lines, line.String())
				line.Reset()
				cells = 0
			}
			line.WriteRune(r)
			cells += runeWidth
		}
		lines = append(lines, line.String())
	}
	if len(lines) == 0 {
		return []string{"—"}
	}
	return lines
}

func ragComparisonPad(line string, width int) string {
	cells := 0
	for _, r := range line {
		cells += runeCells(r)
	}
	return line + strings.Repeat(" ", maxRAGComparison(0, width-cells))
}

func maxRAGComparison(left, right int) int {
	if left > right {
		return left
	}
	return right
}
