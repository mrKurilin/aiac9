package terminal

import (
	"fmt"
	"io"
	"strings"
)

// PrintComparison renders aligned, wrapped answer panes with shared evidence.
func PrintComparison(out io.Writer, leftLabel, rightLabel, left, right, question, evidence string) {
	columns, _ := terminalSize(out)
	width := maxComparison(20, (columns-3)/2)
	leftLines := comparisonLines(left, width)
	rightLines := comparisonLines(right, width)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s │ %s\n", comparisonPad(leftLabel, width), comparisonPad(rightLabel, width))
	fmt.Fprintf(out, "%s─┼─%s\n", strings.Repeat("─", width), strings.Repeat("─", width))
	for row := 0; row < maxComparison(len(leftLines), len(rightLines)); row++ {
		leftLine, rightLine := "", ""
		if row < len(leftLines) {
			leftLine = leftLines[row]
		}
		if row < len(rightLines) {
			rightLine = rightLines[row]
		}
		fmt.Fprintf(out, "%s │ %s\n", comparisonPad(leftLine, width), comparisonPad(rightLine, width))
	}
	fmt.Fprintf(out, "%s─┴─%s\n", strings.Repeat("─", width), strings.Repeat("─", width))
	fmt.Fprintf(out, "Вопрос: %s\n%s\n", question, evidence)
}

func comparisonLines(value string, width int) []string {
	var lines []string
	for _, sourceLine := range strings.Split(strings.TrimSpace(value), "\n") {
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
	return lines
}

func comparisonPad(line string, width int) string {
	cells := 0
	for _, r := range line {
		cells += runeCells(r)
	}
	return line + strings.Repeat(" ", maxComparison(0, width-cells))
}

func maxComparison(a, b int) int {
	if a > b {
		return a
	}
	return b
}
