package main

import (
	"io"
	"os"
	"regexp"
	"strings"
)

// Buffer one line so Markdown delimiters split across API chunks remain intact.
// Pipes retain the original Markdown for use by other programs.
type markdownWriter struct {
	out     io.Writer
	styled  bool
	pending string
	fence   string
}

func newMarkdownWriter(out io.Writer) *markdownWriter {
	m := &markdownWriter{out: out}
	_, noColor := os.LookupEnv("NO_COLOR")
	m.styled = terminalOutput(out) && !noColor

	return m
}

var headingMarkdown = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
var bulletMarkdown = regexp.MustCompile(`^(\s*)[-+*]\s+`)
var inlineMarkdown = regexp.MustCompile("`([^`]+)`|\\*\\*(.+?)\\*\\*|__(.+?)__|\\[([^]]+)\\]\\(([^)]+)\\)|\\*([^*]+)\\*")
var terminalControls = regexp.MustCompile(`[\x00-\x08\x0b-\x1f\x7f-\x9f]`)

func (m *markdownWriter) Write(chunk string) error {
	if !m.styled {
		_, err := io.WriteString(m.out, chunk)
		return err
	}
	m.pending += chunk
	for {
		i := strings.IndexByte(m.pending, '\n')
		if i < 0 {
			return nil
		}
		line := m.pending[:i]
		m.pending = m.pending[i+1:]
		if err := m.line(line, "\n"); err != nil {
			return err
		}
	}
}

func (m *markdownWriter) Flush() error {
	if m.pending == "" {
		return nil
	}
	line := m.pending
	m.pending = ""
	return m.line(line, "")
}

func (m *markdownWriter) line(line, end string) error {
	line = terminalControls.ReplaceAllString(line, "")
	trimmed := strings.TrimSpace(line)
	if m.fence != "" {
		if strings.HasPrefix(trimmed, m.fence) && strings.Trim(trimmed, string(m.fence[0])) == "" {
			m.fence = ""
			line = "\x1b[2m  └──\x1b[0m"
		} else {
			line = "\x1b[36m  " + line + "\x1b[0m"
		}
	} else if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
		char := trimmed[0]
		n := 0
		for n < len(trimmed) && trimmed[n] == char {
			n++
		}
		m.fence = trimmed[:n]
		line = "\x1b[2m  ┌── " + strings.TrimSpace(trimmed[n:]) + "\x1b[0m"
	} else {
		heading := headingMarkdown.FindStringSubmatch(line)
		if heading != nil {
			line = heading[1]
		}
		line = bulletMarkdown.ReplaceAllString(line, "${1}• ")
		if strings.HasPrefix(line, "> ") {
			line = "│ " + line[2:]
		}
		line = inlineMarkdown.ReplaceAllStringFunc(line, func(s string) string {
			p := inlineMarkdown.FindStringSubmatch(s)
			switch {
			case p[1] != "":
				return "\x1b[36m" + p[1] + "\x1b[0m"
			case p[2] != "":
				return "\x1b[1m" + p[2] + "\x1b[0m"
			case p[3] != "":
				return "\x1b[1m" + p[3] + "\x1b[0m"
			case p[4] != "":
				return p[4] + " (" + p[5] + ")"
			default:
				return "\x1b[3m" + p[6] + "\x1b[0m"
			}
		})
		if heading != nil {
			line = "\x1b[1;34m" + line + "\x1b[0m"
		}
	}
	_, err := io.WriteString(m.out, line+end)
	return err
}
