package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func terminalOutput(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0 && os.Getenv("TERM") != "dumb"
}

func fieldHeader(out io.Writer, name, color string) {
	prefix := "─ " + name + " "
	width := terminalColumns(out) - 1 - len([]rune(prefix))
	if width < 0 {
		width = 0
	}
	text := prefix + strings.Repeat("─", width)
	if newMarkdownWriter(out).styled {
		fmt.Fprintf(out, "\x1b[%sm%s\x1b[0m\n", color, text)
	} else {
		fmt.Fprintln(out, text)
	}
}

// Metadata stays next to its message, with a subtle gutter instead of a large panel.
type infoWriter struct{ out io.Writer }

func (w infoWriter) Write(p []byte) (int, error) {
	text := strings.TrimRight(string(p), "\n")
	if text == "" {
		return len(p), nil
	}
	text = "  · " + strings.ReplaceAll(strings.TrimSpace(text), "\n", "\n  · ")
	if newMarkdownWriter(w.out).styled {
		text = "\x1b[2m" + text + "\x1b[0m"
	}
	_, err := fmt.Fprintln(w.out, text)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
