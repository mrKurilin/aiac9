package terminal

import (
	"fmt"
	"io"
	"strings"
)

// PrintComparison keeps multiline answers in two aligned panes.
func PrintComparison(out io.Writer, leftTitle, rightTitle, leftText, rightText string) {
	columns, _ := terminalSize(out)
	width := (columns - 3) / 2
	if width < 18 {
		width = 18
	}
	left := comparisonLines(leftText, width)
	right := comparisonLines(rightText, width)
	fmt.Fprintf(out, "%s │ %s\n", comparisonPad(leftTitle, width), comparisonPad(rightTitle, width))
	fmt.Fprintf(out, "%s─┼─%s\n", strings.Repeat("─", width), strings.Repeat("─", width))
	rows := len(left)
	if len(right) > rows {
		rows = len(right)
	}
	for row := 0; row < rows; row++ {
		leftLine, rightLine := "", ""
		if row < len(left) {
			leftLine = left[row]
		}
		if row < len(right) {
			rightLine = right[row]
		}
		fmt.Fprintf(out, "%s │ %s\n", comparisonPad(leftLine, width), comparisonPad(rightLine, width))
	}
	fmt.Fprintf(out, "%s─┴─%s\n", strings.Repeat("─", width), strings.Repeat("─", width))
}

func comparisonLines(value string, width int) []string {
	var result []string
	for _, source := range strings.Split(strings.TrimSpace(value), "\n") {
		var line strings.Builder
		cells := 0
		for _, r := range strings.ReplaceAll(source, "\t", "    ") {
			n := runeCells(r)
			if cells > 0 && cells+n > width {
				result = append(result, line.String())
				line.Reset()
				cells = 0
			}
			line.WriteRune(r)
			cells += n
		}
		result = append(result, line.String())
	}
	if len(result) == 0 {
		return []string{"—"}
	}
	return result
}

func comparisonPad(value string, width int) string {
	cells := 0
	for _, r := range value {
		cells += runeCells(r)
	}
	if cells >= width {
		return value
	}
	return value + strings.Repeat(" ", width-cells)
}
