package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const terminalHelp = `Команды:
  /mcp            — показать MCP-сервер mrkGitlab и его состояние
  /mcp mrkgitlab   — показать инструменты mrkGitlab
  /mcp mrkgitlab mrs [ПРОЕКТ] — сводка по моим открытым MR
  /mcp mrkgitlab call NAME JSON — вызвать инструмент mrkGitlab
  /state           — показать формальное состояние
  /invariants      — показать обязательные инварианты
  /invariant add КАТЕГОРИЯ | ПРАВИЛО
                   — добавить и сохранить инвариант
  /invariant remove ID
                   — удалить инвариант по идентификатору
  /invariant clear — удалить все инварианты
  /pause [ПРИЧИНА] — сохранить задачу и поставить на паузу
  /resume          — продолжить с того же места
  /reset           — удалить текущую задачу и её контекст
  /info            — справка
  /exit            — выход

Ctrl+C или Esc останавливает текущее выполнение; двойное нажатие завершает mrkai.

Просто опишите, что нужно сделать. Агент сохранит контекст задачи и продолжит
диалог; если данных не хватает, он задаст уточняющий вопрос.`

func printInvariants(out io.Writer, items []Invariant) {
	if len(items) == 0 {
		fmt.Fprintln(out, "Инварианты не заданы. Добавьте первый командой /invariant add КАТЕГОРИЯ | ПРАВИЛО")
		return
	}
	for _, item := range items {
		fmt.Fprintf(out, "%s [%s] %s\n", item.ID, item.Category, item.Rule)
	}
}

func printState(out io.Writer, state *TaskState) {
	if state == nil {
		fmt.Fprintln(out, "Активной задачи нет. Опишите её обычным сообщением.")
		return
	}
	view := struct {
		Goal        string `json:"goal"`
		Paused      bool   `json:"paused"`
		PauseReason string `json:"pause_reason,omitempty"`
	}{state.Goal, state.Paused, state.PauseReason}
	data, _ := json.MarshalIndent(view, "", "  ")
	fmt.Fprintln(out, string(data))
}

func printMCPServers(ctx context.Context, a *Agent, out io.Writer) {
	fmt.Fprintln(out, "MCP-серверы:")
	if a.gitlab == nil {
		fmt.Fprintln(out, "  mrkGitlab — не настроен")
	} else if tools, err := a.gitlab.ListTools(ctx); err != nil {
		fmt.Fprintln(out, "  mrkGitlab — ошибка:", err)
	} else {
		fmt.Fprintf(out, "  mrkGitlab — подключён, инструментов: %d\n", len(tools))
	}
}

func runGitLabMCPCommand(ctx context.Context, a *Agent, rest string, out io.Writer) error {
	action, arguments, _ := strings.Cut(strings.TrimSpace(rest), " ")
	switch action {
	case "", "tools":
		if strings.TrimSpace(arguments) != "" {
			return fmt.Errorf("использование: /mcp mrkgitlab")
		}
		tools, err := a.ListGitLabTools(ctx)
		if err == nil {
			printMCPTools(out, tools)
		}
		return err
	case "mrs":
		project := strings.TrimSpace(arguments)
		encoded, _ := json.Marshal(map[string]string{"project_id": project})
		result, err := a.CallGitLabTool(ctx, "list_open_merge_requests", encoded)
		if err == nil {
			fmt.Fprintln(out, "GitLab MCP: ответ получен")
			printMergeRequests(out, result)
		}
		return err
	case "call":
		name, raw, ok := strings.Cut(strings.TrimSpace(arguments), " ")
		if name == "" {
			return fmt.Errorf("использование: /mcp mrkgitlab call NAME JSON")
		}
		if !ok || strings.TrimSpace(raw) == "" {
			raw = "{}"
		}
		fmt.Fprintf(out, "GitLab MCP: вызываю %s…\n", name)
		result, err := a.CallGitLabToolWithProgress(ctx, name, json.RawMessage(raw), func(message string) {
			fmt.Fprintln(out, "  ↳", message)
		})
		if err == nil {
			fmt.Fprintf(out, "GitLab MCP %s: %s\n", name, result)
		}
		return err
	default:
		return fmt.Errorf("использование: /mcp mrkgitlab [mrs [group/project]|call NAME JSON]")
	}
}

func printMergeRequests(out io.Writer, result string) {
	var payload struct {
		MergeRequests []MergeRequestSummary `json:"merge_requests"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		fmt.Fprintln(out, result)
		return
	}
	if len(payload.MergeRequests) == 0 {
		fmt.Fprintln(out, "Открытых MR нет.")
		return
	}
	fmt.Fprintf(out, "Открытых MR: %d\n", len(payload.MergeRequests))
	for _, mr := range payload.MergeRequests {
		reference := mr.Reference
		if reference == "" {
			reference = fmt.Sprintf("!%d", mr.IID)
		}
		fmt.Fprintf(out, "\n• %s — %s (@%s)\n", reference, mr.Title, mr.Author)
		if mr.URL != "" {
			fmt.Fprintln(out, "  ", mr.URL)
		}
		fmt.Fprintln(out, "  ", mr.Summary)
	}
}

func runCommand(a *Agent, line string, out io.Writer) (handled, exit bool) {
	return runCommandContext(context.Background(), a, line, out)
}

func runCommandContext(ctx context.Context, a *Agent, line string, out io.Writer) (handled, exit bool) {
	command, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	if !strings.HasPrefix(command, "/") {
		return false, false
	}
	var err error
	switch command {
	case "/exit", "/quit":
		return true, true
	case "/info", "/help":
		fmt.Fprintln(out, terminalHelp)
	case "/mcp":
		action, arguments, _ := strings.Cut(strings.TrimSpace(rest), " ")
		switch action {
		case "", "list":
			if strings.TrimSpace(arguments) != "" {
				err = fmt.Errorf("использование: /mcp list")
				break
			}
			printMCPServers(ctx, a, out)
		case "mrkgitlab":
			err = runGitLabMCPCommand(ctx, a, arguments, out)
		default:
			err = fmt.Errorf("использование: /mcp или /mcp mrkgitlab [mrs [ПРОЕКТ]|call NAME JSON]")
		}
	case "/state":
		if strings.TrimSpace(rest) != "" {
			err = fmt.Errorf("использование: /state")
		} else {
			printState(out, a.Snapshot())
		}
	case "/invariants":
		if strings.TrimSpace(rest) != "" {
			err = fmt.Errorf("использование: /invariants")
		} else {
			printInvariants(out, a.Invariants())
		}
	case "/invariant":
		action, arguments, _ := strings.Cut(strings.TrimSpace(rest), " ")
		switch action {
		case "add":
			category, rule, ok := strings.Cut(strings.TrimSpace(arguments), "|")
			if !ok {
				err = fmt.Errorf("использование: /invariant add КАТЕГОРИЯ | ПРАВИЛО")
				break
			}
			var item Invariant
			item, err = a.AddInvariant(category, rule)
			if err == nil {
				fmt.Fprintf(out, "Инвариант %s сохранён.\n", item.ID)
			}
		case "remove":
			id := strings.TrimSpace(arguments)
			if id == "" || strings.Contains(id, " ") {
				err = fmt.Errorf("использование: /invariant remove ID")
				break
			}
			err = a.RemoveInvariant(id)
			if err == nil {
				fmt.Fprintf(out, "Инвариант %s удалён.\n", strings.ToUpper(id))
			}
		case "clear":
			if strings.TrimSpace(arguments) != "" {
				err = fmt.Errorf("использование: /invariant clear")
				break
			}
			var count int
			count, err = a.ClearInvariants()
			if err == nil && count == 0 {
				fmt.Fprintln(out, "Инварианты уже отсутствуют.")
			} else if err == nil {
				fmt.Fprintf(out, "Удалены все инварианты: %d.\n", count)
			}
		default:
			err = fmt.Errorf("использование: /invariant add КАТЕГОРИЯ | ПРАВИЛО, /invariant remove ID или /invariant clear")
		}
	case "/pause":
		err = a.Pause(rest)
		if err == nil {
			fmt.Fprintln(out, "Задача сохранена и поставлена на паузу.")
		}
	case "/resume":
		if strings.TrimSpace(rest) != "" {
			err = fmt.Errorf("использование: /resume")
		} else {
			err = a.Resume()
		}
		if err == nil {
			fmt.Fprintln(out, "Продолжаю без повторного сбора контекста:")
			printState(out, a.Snapshot())
		}
	case "/reset":
		if strings.TrimSpace(rest) != "" {
			err = fmt.Errorf("использование: /reset")
		} else {
			err = a.Reset()
		}
		if err == nil {
			if clearer, ok := out.(interface{ clearChat() error }); ok {
				err = clearer.clearChat()
			}
		}
		if err == nil {
			fmt.Fprintln(out, "Чат, задача и сохранённый контекст удалены.")
		}
	default:
		err = fmt.Errorf("неизвестная команда; /info — справка")
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(out, "Ошибка:", err)
	}
	return true, false
}

func printAgentTurn(ctx context.Context, a *Agent, prompt string, out io.Writer) (TurnResult, error) {
	streamStarted := false
	emit := func(chunk string) error {
		if chunk == "" {
			return nil
		}
		if !streamStarted {
			if _, err := fmt.Fprint(out, "\n◆ "); err != nil {
				return err
			}
			streamStarted = true
		}
		_, err := io.WriteString(out, chunk)
		return err
	}
	result, err := a.AskStreamWithTools(ctx, prompt, emit, func(message string) {
		fmt.Fprintln(out, message)
	})
	if streamStarted {
		fmt.Fprint(out, "\n\n")
	}
	if err != nil {
		return TurnResult{}, err
	}
	if !streamStarted && result.Answer != "" {
		fmt.Fprintf(out, "\n◆ %s\n\n", result.Answer)
	}
	if result.InvariantSummary != "" {
		fmt.Fprintln(out, "Проверка инвариантов:", result.InvariantSummary)
		fmt.Fprintln(out)
	}
	if result.Notice != "" {
		fmt.Fprintln(out, "Уведомление:", result.Notice)
		fmt.Fprintln(out)
	}
	return result, nil
}

func handleTerminalLine(ctx context.Context, a *Agent, line string, out io.Writer) (bool, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false, nil
	}
	if handled, exit := runCommandContext(ctx, a, line, out); handled {
		return exit, nil
	}
	if _, err := printAgentTurn(ctx, a, line, out); err != nil {
		return false, err
	}
	return false, nil
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func userTranscript(line string) string {
	return "\n› " + strings.ReplaceAll(line, "\n", "\n  ")
}

const doubleInterruptWindow = time.Second

type terminalInterrupt struct {
	key string
}

func isDoubleInterrupt(previousKey string, previous time.Time, current terminalInterrupt, now time.Time) bool {
	return previousKey == current.key && !previous.IsZero() && now.Sub(previous) >= 0 && now.Sub(previous) <= doubleInterruptWindow
}

func mcpOperation(line string) string {
	parts := strings.Fields(line)
	if len(parts) < 2 || parts[0] != "/mcp" {
		return ""
	}
	if parts[1] == "mrkgitlab" && len(parts) > 2 && parts[2] == "mrs" {
		return "GitLab MCP: получаю открытые MR"
	}
	if parts[1] == "mrkgitlab" && len(parts) > 2 && parts[2] == "call" {
		return "GitLab MCP: вызываю инструмент"
	}
	return ""
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(data)
}

func runTerminal(ctx context.Context, a *Agent, in io.Reader, out io.Writer) error {
	edited, restore, err := prepareInput(in, out)
	if err != nil {
		return err
	}
	defer restore()

	var display io.Writer = &lockedWriter{w: out}
	var footer *bottomTerminal
	if edited {
		footer, err = newBottomTerminal(out)
		if err != nil {
			return err
		}
		defer footer.Close()
		display = footer
	}

	fmt.Fprintln(display, "mrkai · MCP и диалог · /info — команды")
	if a.gitlab != nil {
		tools, err := a.gitlab.ListTools(ctx)
		if err != nil {
			return fmt.Errorf("получить инструменты mrkGitlab: %w", err)
		}
		fmt.Fprintln(display, "[MCP] mrkGitlab запущен: локальный дочерний процесс, транспорт stdio.")
		printMCPTools(display, tools)
		fmt.Fprintln(display, "Сводка по моим MR: /mcp mrkgitlab mrs")
	}
	if state := a.Snapshot(); state != nil {
		fmt.Fprintln(display, "Загружена сохранённая задача:")
		printState(display, state)
	}
	if footer != nil {
		if err := footer.setState(a.Snapshot()); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(display, taskStatus(a.Snapshot()))
	}

	lines := make(chan string, 64)
	interrupts := make(chan terminalInterrupt, 4)
	readErr := make(chan error, 1)
	var pending atomic.Int64
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	enqueue := func(line string) bool {
		queued := pending.Add(1) > 1
		select {
		case lines <- line:
			if queued && edited {
				fmt.Fprintln(display, "Сообщение принято и поставлено в очередь.")
			}
			return true
		case <-readCtx.Done():
			pending.Add(-1)
			return false
		}
	}
	go func() {
		defer close(lines)
		if edited {
			reader := bufio.NewReader(in)
			history := []string{}
			for {
				line, err := readEditedLine(readCtx, reader, footer, history)
				if err != nil {
					if err == errInputInterrupted || err == errEscapeInterrupted {
						interrupt := terminalInterrupt{key: "Ctrl+C"}
						if err == errEscapeInterrupted {
							interrupt.key = "Esc"
						}
						select {
						case interrupts <- interrupt:
						case <-readCtx.Done():
							readErr <- nil
							return
						}
						continue
					}
					if err == io.EOF || readCtx.Err() != nil {
						readErr <- nil
					} else {
						readErr <- err
					}
					return
				}
				if strings.TrimSpace(line) != "" && (len(history) == 0 || history[len(history)-1] != line) {
					history = append(history, line)
				}
				if strings.TrimSpace(line) != "" {
					fmt.Fprintln(display, userTranscript(line))
				}
				if !enqueue(line) {
					readErr <- readCtx.Err()
					return
				}
			}
		}
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 64*1024), 2<<20)
		for {
			fmt.Fprint(display, "→ ")
			if !scanner.Scan() {
				readErr <- scanner.Err()
				return
			}
			line := scanner.Text()
			fmt.Fprintln(display, line)
			if !enqueue(line) {
				readErr <- readCtx.Err()
				return
			}
		}
	}()
	var lastCtrlC time.Time
	var lastInterruptKey string
	for {
		var line string
		var ok bool
		select {
		case <-readCtx.Done():
			return nil
		case interrupt := <-interrupts:
			now := time.Now()
			if isDoubleInterrupt(lastInterruptKey, lastCtrlC, interrupt, now) {
				return nil
			}
			lastCtrlC = now
			lastInterruptKey = interrupt.key
			fmt.Fprintf(display, "Нет активного выполнения. Нажмите %s ещё раз для выхода.\n", interrupt.key)
			continue
		case line, ok = <-lines:
			if !ok {
				return <-readErr
			}
		}
		thinking := strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "/")
		if footer != nil {
			operation := mcpOperation(line)
			var activityErr error
			if operation != "" {
				activityErr = footer.setOperation(operation)
			} else if thinking {
				activityErr = footer.setActivity(a.Snapshot(), true)
			}
			if activityErr != nil {
				pending.Add(-1)
				return activityErr
			}
		}
		executionCtx, stopExecution := context.WithCancel(readCtx)
		type executionResult struct {
			exit bool
			err  error
		}
		finished := make(chan executionResult, 1)
		go func() {
			exit, err := handleTerminalLine(executionCtx, a, line, display)
			finished <- executionResult{exit: exit, err: err}
		}()
		cancelled, exitAfterCancel := false, false
		var execution executionResult
	waitExecution:
		for {
			select {
			case execution = <-finished:
				break waitExecution
			case interrupt := <-interrupts:
				now := time.Now()
				double := isDoubleInterrupt(lastInterruptKey, lastCtrlC, interrupt, now)
				lastCtrlC = now
				lastInterruptKey = interrupt.key
				if !cancelled {
					cancelled = true
					stopExecution()
					fmt.Fprintf(display, "%s: останавливаю текущее выполнение…\n", interrupt.key)
				}
				if double {
					exitAfterCancel = true
				}
			case <-readCtx.Done():
				cancelled, exitAfterCancel = true, true
				stopExecution()
			}
		}
		stopExecution()
		pending.Add(-1)
		if footer != nil {
			if stateErr := footer.setState(a.Snapshot()); execution.err == nil {
				execution.err = stateErr
			}
		}
		if cancelled {
			fmt.Fprintln(display, "Текущее выполнение остановлено.")
			execution.err = nil
		}
		if execution.exit || exitAfterCancel {
			return nil
		}
		if readCtx.Err() != nil {
			return nil
		}
		if execution.err != nil {
			fmt.Fprintln(display, "Ошибка:", execution.err)
		}
	}
}
