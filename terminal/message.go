package terminal

import (
	"io"
	"strings"
)

// PrintMessage keeps a chat turn visually separate from status and diagnostics.
func PrintMessage(out io.Writer, label, body string) {
	var block strings.Builder
	block.WriteString("\n╭─ ")
	block.WriteString(label)
	block.WriteByte('\n')
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		block.WriteString("│ ")
		block.WriteString(line)
		block.WriteByte('\n')
	}
	block.WriteString("╰─\n")
	_, _ = io.WriteString(out, block.String())
}

// PrintDiagnostic gives slow-operation updates a consistent, compact prefix.
func PrintDiagnostic(out io.Writer, message string) {
	var block strings.Builder
	for _, line := range strings.Split(strings.TrimRight(message, "\n"), "\n") {
		block.WriteString("  · ")
		block.WriteString(line)
		block.WriteByte('\n')
	}
	_, _ = io.WriteString(out, block.String())
}
