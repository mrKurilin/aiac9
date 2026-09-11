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
	if fixed, ok := out.(*bottomTerminal); ok {
		return fixed.clear()
	}
	// Clear the visible screen and scrollback, then move the cursor home.
	_, err := io.WriteString(out, "\x1b[2J\x1b[3J\x1b[H")
	return err
}

func runTerminal(ctx context.Context, agent *Agent, in io.Reader, out io.Writer, prompt string) error {
	return runTerminalMode(ctx, agent, in, out, prompt, nil)
}

func runComparisonTerminal(ctx context.Context, left, right *Agent, in io.Reader, out io.Writer) error {
	view := newComparisonView(left, right, out)
	return runTerminalMode(ctx, left, in, out, "", view)
}

func runTerminalMode(ctx context.Context, agent *Agent, in io.Reader, out io.Writer, prompt string, comparison *comparisonView) error {

	info := infoWriter{out: out}
	scenario := &script{path: messagesPath}
	// Notices belong inside the panels while they own the screen; writing them
	// to the transcript would scroll the painted frame.
	notify := func(text string) {
		if comparison == nil {
			fmt.Fprintln(info, text)
			return
		}
		for _, pane := range comparison.panes {
			fmt.Fprintln(pane, text)
		}
		comparison.render()
	}
	ask := func(requestCtx context.Context, prompt string) error {
		if comparison != nil {
			return comparison.ask(requestCtx, prompt)
		}
		// Leave two empty terminal rows between conversation blocks.
		fmt.Fprint(out, "\n\n")
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
				fmt.Fprint(out, "\n\n")
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
		if warning := agent.DrainFactsWarning(); warning != "" {
			fmt.Fprintf(&report, "  Facts не обновлены, ответ по прежним фактам: %s\n", warning)
		}
		if _, reportErr := io.Copy(info, &report); err == nil {
			err = reportErr
		}
		if err != nil {
			return err
		}
		return nil
	}
	command := func(line string) bool {
		if comparison != nil {
			return comparison.command(line)
		}
		if line == "/" {
			for _, entry := range slashCommands {
				fmt.Fprintln(info, entry.Label)
			}
			return true
		}
		return memoryCommand(agent, line, info) || contextCommand(agent, line, info)
	}
	if prompt != "" {
		prompt = strings.TrimSpace(prompt)
		if command(prompt) {
			return nil
		}
		switch prompt {
		case "/exit", "/quit":
			return nil
		case "/reset":
			return agent.Reset()
		case "/stats":
			if meter, ok := agent.client.(*Meter); ok {
				meter.Summary(info)
			}
			return nil
		}
		if strings.HasPrefix(prompt, "/") {
			return fmt.Errorf("неизвестная команда; /info — справка")
		}
		return ask(ctx, prompt)
	}
	edited, restore, err := prepareInput(in, out)
	if err != nil {
		return err
	}
	defer restore()
	if edited {
		fixed, err := newBottomTerminal(out)
		if err != nil {
			return err
		}
		defer fixed.Close()
		out = fixed
		info.out = out
		if comparison != nil {
			comparison.out = fixed
			comparison.interactive = true
			// The completion menu grows the footer over the panel and erases
			// those rows; repaint the panel whenever the footer moves.
			fixed.onFooterMove = func() {
				comparison.invalidate()
				comparison.render()
			}
		}
	}
	if comparison == nil {
		fmt.Fprintln(info, "DeepSeek: /info — команды и клавиши · /exit — выйти. Один запрос на строку.")
		fmt.Fprintln(info, "Память: /contextManagementStrategy · Ветки: /addBranch имя · /branch. Контекст: /context · /fill N.")
		memoryCommand(agent, "/memory", info)
		agent.ShowContext(info)
	} else {
		comparison.render()
	}
	// In comparison mode the panel owns every row above the footer: printing a
	// hint there would scroll the painted frame out of step with it.
	if edited && comparison == nil {
		fmt.Fprintln(info, "/ — команды · ↑/↓ — выбор · Tab — дополнить · Enter — подтвердить · Esc — закрыть меню.")
	}
	// Read in a goroutine so Ctrl+C also exits while waiting for terminal input.
	// Buffered: the editor keeps running while a request is in flight, so a
	// line typed during the answer waits here instead of blocking the typist.
	lines := make(chan string, 8)
	// Closing lines ends the loop: buffered input is processed first, and the
	// close publishes readErr to the reader of the channel.
	var readErr error
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	interrupts := make(chan struct{})
	go func() {
		defer close(lines)
		if edited {
			reader := bufio.NewReader(in)
			history := []string{}
			for _, message := range agent.History() {
				if message.Role == "user" && !strings.HasPrefix(message.Content, contextFillerPrefix) {
					history = append(history, message.Content)
				}
			}
			// Esc cancels the running request; with nothing running it only
			// closes the menu, so the signal is dropped rather than queued.
			escape := func() {
				select {
				case interrupts <- struct{}{}:
				default:
				}
			}
			// PageUp/PageDown move the comparison panel; with a single agent the
			// terminal's own scrollback still holds the conversation.
			scroll := func(pages int) {}
			if comparison != nil {
				scroll = comparison.scrollPages
			}
			for {
				line, err := readEditedLineSession(readCtx, reader, out, true, true, agentCompleter(agent), editSession{onEscape: escape, onScroll: scroll}, history...)
				if err != nil {
					if err != io.EOF && readCtx.Err() == nil {
						readErr = err
					}
					return
				}
				// Commands join the history too: /sendNext is worth repeating
				// with the up arrow more than anything else typed here.
				if strings.TrimSpace(line) != "" {
					if len(history) == 0 || history[len(history)-1] != line {
						history = append(history, line)
					}
				}
				select {
				case lines <- line:
				case <-readCtx.Done():
					return
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
		readErr = scanner.Err()
	}()
	for {
		var line string
		select {
		case <-ctx.Done():
			return nil
		case next, ok := <-lines:
			if !ok {
				return readErr
			}
			line = next
		}
		line = strings.TrimSpace(line)
		// Replace /sendNext with the next scenario message, then let it take the
		// ordinary path as if it had been typed by hand.
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "/sendNext" {
			next, note := scenario.next(fields[1:])
			notify(note)
			if next == "" {
				continue
			}
			line = next
		}
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
			if err := agent.Reset(); err != nil {
				fmt.Fprintln(info, "Ошибка:", err)
				continue
			}
			if err := clearConversationView(out, edited); err != nil {
				return err
			}
			fmt.Fprintln(info, "Память очищена.")
			agent.ShowContext(info)
		default:
			if strings.HasPrefix(line, "/") {
				fmt.Fprintln(info, "Неизвестная команда: /info — справка")
				continue
			}
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
				notify("Запрос прерван клавишей Esc.")
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
				notify(fmt.Sprintf("Ошибка: %v", requestErr))
			}
		}
	}
}
