package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var terminalSessionPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validID(id string) bool { return terminalSessionPattern.MatchString(id) }

func runTerminal(ctx context.Context, agent *Agent, in io.Reader, out io.Writer, prompt string) error {
	ask := func(prompt string) error {
		markdown := newMarkdownWriter(out)
		wrote := false
		_, err := agent.AskStream(ctx, prompt, func(chunk string) error {
			wrote = wrote || chunk != ""
			return markdown.Write(chunk)
		})
		if flushErr := markdown.Flush(); err == nil {
			err = flushErr
		}
		// Separate the next prompt (or error) from a partially received answer.
		if wrote {
			_, newlineErr := fmt.Fprintln(out)
			if err == nil {
				err = newlineErr
			}
		}
		if err != nil {
			return err
		}
		return nil
	}
	if prompt != "" {
		return ask(prompt)
	}
	fmt.Fprintln(out, "DeepSeek: /reset — очистить память, /exit — выйти. Один запрос на строку.")
	// Read in a goroutine so Ctrl+C also exits while waiting for terminal input.
	lines := make(chan string)
	done := make(chan error, 1)
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-readCtx.Done():
				return
			}
		}
		done <- scanner.Err()
	}()
	for {
		fmt.Fprint(out, "Вы> ")
		var line string
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			return err
		case line = <-lines:
		}
		line = strings.TrimSpace(line)
		switch line {
		case "":
			continue
		case "/exit", "/quit":
			return nil
		case "/reset":
			agent.Reset()
			fmt.Fprintln(out, "Память очищена.")
		default:
			if err := ask(line); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				fmt.Fprintf(out, "Ошибка: %v\n", err)
			}
		}
	}
}
