package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var terminalSessionPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validID(id string) bool { return terminalSessionPattern.MatchString(id) }

func clearConversationView(out io.Writer, interactive bool) error {
	if !interactive {
		return nil
	}
	// Clear the visible screen and scrollback, then move the cursor home.
	_, err := io.WriteString(out, "\x1b[2J\x1b[3J\x1b[H")
	return err
}

func runTerminal(ctx context.Context, agent *Agent, in io.Reader, out io.Writer, prompt string) error {
	return runTerminalView(ctx, agent, in, out, prompt, nil)
}

func runTerminalView(ctx context.Context, agent *Agent, in io.Reader, out io.Writer, prompt string, comparison *comparisonView) error {
	info := infoWriter{out: out}
	ask := func(requestCtx context.Context, prompt string) error {
		fieldHeader(out, "СООБЩЕНИЕ", "36")
		fmt.Fprintln(out, terminalControls.ReplaceAllString(prompt, ""))
		loader := newWorkingLoader(out, terminalOutput(out))
		defer loader.Stop()
		var report bytes.Buffer
		if meter, ok := agent.client.(*Meter); ok {
			meter.Out = info
			meter.Report = &report
			meter.RequestStarted = loader.Start
			meter.RequestFinished = loader.Stop
			defer func() {
				meter.Report = nil
				meter.RequestStarted = nil
				meter.RequestFinished = nil
			}()
		}
		markdown := newMarkdownWriter(out)
		wrote := false
		_, err := agent.AskStream(requestCtx, prompt, func(chunk string) error {
			if !wrote && chunk != "" {
				loader.Stop()
				fmt.Fprintln(out)
				fieldHeader(out, "ОТВЕТ", "32")
			}
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
		if warning := agent.DrainCompressionWarning(); warning != "" {
			fmt.Fprintf(&report, "  Предупреждение: summary не обновлено, полная история сохранена: %s\n", warning)
		}
		if _, reportErr := io.Copy(info, &report); err == nil {
			err = reportErr
		}
		if err != nil {
			return err
		}
		return nil
	}
	if comparison != nil {
		ask = comparison.ask
	}
	command := func(line string) bool {
		if comparison != nil {
			return comparison.command(line)
		}
		return memoryCommand(agent, line, info) || contextCommand(agent, line, info)
	}
	if prompt != "" {
		if command(strings.TrimSpace(prompt)) {
			return nil
		}
		return ask(ctx, prompt)
	}
	edited, restore, err := prepareInput(in, out)
	if err != nil {
		return err
	}
	defer restore()
	fmt.Fprintln(info, "DeepSeek: /info — команды и клавиши · /exit — выйти. Один запрос на строку.")
	fmt.Fprintln(info, "Память: /memory · /compress on|off. Контекст: /context · /fill N.")
	agent.ShowContext(info)
	if edited {
		fmt.Fprintln(info, "↑/↓ — история · Enter — отправить · Ctrl+Backspace / Ctrl+U — очистить строку.")
	}
	if comparison != nil {
		comparison.render()
	}
	// Read in a goroutine so Ctrl+C also exits while waiting for terminal input.
	lines := make(chan string)
	done := make(chan error, 1)
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	requests := make(chan struct{})
	interrupts := make(chan struct{})
	go func() {
		if edited {
			reader := bufio.NewReader(in)
			history := []string{}
			for _, message := range agent.History() {
				if message.Role == "user" && !strings.HasPrefix(message.Content, contextFillerPrefix) {
					history = append(history, message.Content)
				}
			}
			for {
				select {
				case <-requests:
				case <-readCtx.Done():
					return
				}
			readLine:
				line, err := readEditedLineLayout(reader, out, true, comparison != nil && comparison.interactive, history...)
				if err != nil {
					if err == io.EOF {
						err = nil
					}
					done <- err
					return
				}
				trimmed := strings.TrimSpace(line)
				if trimmed != "" && !strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "+") {
					if len(history) == 0 || history[len(history)-1] != line {
						history = append(history, line)
					}
				}
				select {
				case lines <- line:
				case <-readCtx.Done():
					return
				}
				// While a request runs, Esc cancels it. Terminal VTIME makes the
				// read return periodically so the next editor can open promptly.
				for {
					select {
					case <-requests:
						goto readLine
					case <-readCtx.Done():
						return
					default:
					}
					r, _, readErr := reader.ReadRune()
					if readErr == io.EOF {
						continue
					}
					if readErr != nil {
						done <- readErr
						return
					}
					if r == 27 {
						select {
						case interrupts <- struct{}{}:
						case <-readCtx.Done():
							return
						}
					}
				}
			}
		}
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
		if edited {
			select {
			case requests <- struct{}{}:
			case <-ctx.Done():
				return nil
			}
		}
		var line string
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			return err
		case line = <-lines:
		}
		line = strings.TrimSpace(line)
		if comparison == nil && (strings.HasPrefix(line, "/") || strings.HasPrefix(line, "+")) {
			fieldHeader(out, "КОМАНДА", "36")
			fmt.Fprintln(out, terminalControls.ReplaceAllString(line, ""))
		}
		if command(line) {
			continue
		}
		switch line {
		case "":
			continue
		case "/exit", "/quit":
			return nil
		case "/stats":
			if meter, ok := agent.client.(*Meter); ok {
				meter.Summary(info)
			}
		case "/reset":
			agent.Reset()
			if err := clearConversationView(out, edited); err != nil {
				return err
			}
			fmt.Fprintln(info, "Память очищена.")
			agent.ShowContext(info)
		default:
			requestCtx, stopRequest := context.WithCancel(ctx)
			result := make(chan error, 1)
			go func() { result <- ask(requestCtx, line) }()
			var requestErr error
			interrupted := false
			select {
			case requestErr = <-result:
			case <-interrupts:
				interrupted = true
				stopRequest()
				requestErr = <-result
				if comparison == nil {
					fmt.Fprintln(info, "Запрос прерван клавишей Esc.")
				}
			case <-ctx.Done():
				stopRequest()
				<-result
				return nil
			}
			stopRequest()
			if requestErr != nil && !interrupted {
				if ctx.Err() != nil {
					return nil
				}
				if comparison == nil {
					fmt.Fprintf(info, "Ошибка: %v\n", requestErr)
				}
			}
		}
	}
}
