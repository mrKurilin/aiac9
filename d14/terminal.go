package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

const terminalHelp = `Команды:
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

Просто опишите, что нужно сделать. Когда задача станет ясна, агент сам создаст
её в planning и сразу предложит план; если данных не хватает, он задаст
уточняющий вопрос.
Этапы переключаются автоматически после подтверждения результата.
Разрешённые переходы:
  planning → execution
  execution → validation | planning
  validation → done | execution`

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
		Goal           string   `json:"goal"`
		Stage          Stage    `json:"stage"`
		CurrentStep    string   `json:"current_step"`
		ExpectedAction string   `json:"expected_action"`
		Completed      []string `json:"completed_steps,omitempty"`
		Paused         bool     `json:"paused"`
		PauseReason    string   `json:"pause_reason,omitempty"`
	}{state.Goal, state.Stage, state.CurrentStep, state.ExpectedAction, state.CompletedSteps, state.Paused, state.PauseReason}
	data, _ := json.MarshalIndent(view, "", "  ")
	fmt.Fprintln(out, string(data))
}

func runCommand(a *Agent, line string, out io.Writer) (handled, exit bool) {
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
	if err != nil {
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
	result, err := a.AskStream(ctx, prompt, emit)
	if streamStarted {
		fmt.Fprint(out, "\n\n")
	}
	if err != nil {
		return TurnResult{}, err
	}
	if !streamStarted && result.Answer != "" && !result.TaskCreated {
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

func keepThinking(out io.Writer, a *Agent) error {
	if activity, ok := out.(interface {
		setActivity(*TaskState, bool) error
	}); ok {
		return activity.setActivity(a.Snapshot(), true)
	}
	return nil
}

func printStageChange(out io.Writer, a *Agent, result TurnResult) {
	if !result.StageChanged {
		return
	}
	fmt.Fprintln(out, "Этап изменён автоматически:", result.Stage)
	printState(out, a.Snapshot())
}

func continueExecution(ctx context.Context, a *Agent, out io.Writer) error {
	if err := keepThinking(out, a); err != nil {
		return err
	}
	result, err := printAgentTurn(ctx, a, automaticExecutionPrompt, out)
	if err != nil {
		return err
	}
	printStageChange(out, a, result)
	return nil
}

func handleTerminalLine(ctx context.Context, a *Agent, line string, out io.Writer) (bool, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false, nil
	}
	if handled, exit := runCommand(a, line, out); handled {
		return exit, nil
	}
	if a.Snapshot() == nil {
		result, err := printAgentTurn(ctx, a, line, out)
		if err != nil {
			return false, err
		}
		if !result.TaskCreated {
			return false, nil
		}
		fmt.Fprintln(out, "Задача создана автоматически · этап:", result.Stage)
		if err := keepThinking(out, a); err != nil {
			return false, err
		}
		plan, err := printAgentTurn(ctx, a, automaticPlanningPrompt, out)
		if err != nil {
			return false, err
		}
		printStageChange(out, a, plan)
		if plan.StageChanged && plan.Stage == StageExecution {
			if err := continueExecution(ctx, a, out); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	result, err := printAgentTurn(ctx, a, line, out)
	if err != nil {
		return false, err
	}
	printStageChange(out, a, result)
	if result.StageChanged && result.Stage == StageExecution {
		if err := continueExecution(ctx, a, out); err != nil {
			return false, err
		}
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

	fmt.Fprintln(display, "mrkai · invariants + task state machine · /info — команды")
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
					if err == errInputInterrupted {
						cancel()
						readErr <- nil
						return
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
	for line := range lines {
		thinking := strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "/")
		if footer != nil && thinking {
			if err := footer.setActivity(a.Snapshot(), true); err != nil {
				pending.Add(-1)
				return err
			}
		}
		exit, err := handleTerminalLine(readCtx, a, line, display)
		pending.Add(-1)
		if footer != nil {
			if stateErr := footer.setState(a.Snapshot()); err == nil {
				err = stateErr
			}
		}
		if exit {
			return nil
		}
		if readCtx.Err() != nil {
			return nil
		}
		if err != nil {
			fmt.Fprintln(display, "Ошибка:", err)
		}
	}
	return <-readErr
}
