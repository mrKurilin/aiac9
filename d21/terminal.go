package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const terminalHelp = `Команды:
  /index github ТЕМА — скачать README публичных GitHub-репозиториев по теме и построить два индекса
  /index build [КАТАЛОГ] — построить два локальных индекса (по умолчанию текущий каталог)
  /index status — показать размеры и пути индексов
  /index search fixed|structure ЗАПРОС — поиск по выбранной стратегии
  /index compare ЗАПРОС — сравнить размеры и результаты поиска
  /mcp            — показать MCP-серверы
  /mcp mrkgitlab   — показать инструменты mrkGitlab
  /mcp mrkgitlab mrs [ПРОЕКТ] — сводка по моим открытым MR
  /mcp mrkgitlab call NAME JSON — вызвать инструмент mrkGitlab
  /mcp mrkscheduler — показать инструменты планировщика
  /mcp mrkscheduler add СЕКУНДЫ ПЕРИОД ТЕКСТ — создать напоминание (период 0 — один раз)
  /mcp mrkscheduler mrs СЕКУНДЫ [ПРОЕКТ] — регулярно выводить открытые MR
  /mcp mrkscheduler list — список напоминаний
  /mcp mrkscheduler summary — агрегированная сводка
  /mcp mrkscheduler cancel ID — остановить напоминание
  /mcp mrkpipeline — показать инструменты композиции
  /mcp mrkpipeline run ЗАПРОС | ФАЙЛ.md — найти, обработать и сохранить
  /mcp mrkpipeline call NAME JSON — вызвать отдельный инструмент
  /plan-mode enable — включить цели и уточнение задачи
  /plan-mode disable — вернуться к обычному чату
  /state           — показать формальное состояние
  /invariants      — показать обязательные инварианты
  /invariant add КАТЕГОРИЯ | ПРАВИЛО
                   — добавить и сохранить инвариант
  /invariant remove ID
                   — удалить инвариант по идентификатору
  /invariant clear — удалить все инварианты
  /pause [ПРИЧИНА] — сохранить задачу и поставить на паузу
  /resume          — продолжить с того же места
  /reset           — удалить задачу, контекст и остановить расписание
  /info            — справка
  /exit            — выход

Ctrl+C или Esc останавливает текущее выполнение; двойное нажатие завершает mrkai.

По умолчанию работает обычный чат. Для ведения задачи с целью и уточняющими
вопросами включите /plan-mode enable. Чтобы создать отчёт обычным сообщением,
задайте DEEPSEEK_API_KEY и напишите, что найти и в какой файл .md сохранить.`

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
	for _, server := range a.mcpServers() {
		if server.client == nil {
			fmt.Fprintf(out, "  %s — не настроен\n", server.name)
			continue
		}
		fmt.Fprintf(out, "  %s — обнаружение инструментов\n", server.name)
		tools, err := server.client.ListTools(ctx)
		if err != nil {
			fmt.Fprintf(out, "  %s — ошибка: %v\n", server.name, err)
		} else {
			fmt.Fprintf(out, "  %s — подключён, инструментов: %d\n", server.name, len(tools))
		}
	}
}

func printPipelineResult(out io.Writer, result string) error {
	var report struct {
		Path    string   `json:"path"`
		Found   int      `json:"found"`
		Sources []string `json:"sources"`
		Summary string   `json:"summary"`
	}
	if err := json.Unmarshal([]byte(result), &report); err != nil {
		return fmt.Errorf("не удалось прочитать результат пайплайна: %w", err)
	}
	if report.Path == "" {
		return fmt.Errorf("пайплайн не вернул путь к отчёту")
	}
	fmt.Fprintf(out, "\nОтчёт сохранён: %s\n", report.Path)
	fmt.Fprintf(out, "Найдено документов: %d\n", report.Found)
	fmt.Fprintf(out, "Источники: %s\n", strings.Join(report.Sources, ", "))
	fmt.Fprintf(out, "\nВыдержка:\n%s\n", report.Summary)
	return nil
}

func runPipelineMCPCommand(ctx context.Context, a *Agent, rest string, out io.Writer) error {
	if a.pipeline == nil {
		return fmt.Errorf("mrkPipeline недоступен")
	}
	action, arguments, _ := strings.Cut(strings.TrimSpace(rest), " ")
	switch action {
	case "", "tools":
		if strings.TrimSpace(arguments) != "" {
			return fmt.Errorf("использование: /mcp mrkpipeline")
		}
		tools, err := a.pipeline.ListTools(ctx)
		if err == nil {
			printMCPTools(out, tools)
		}
		return err
	case "run":
		query, filename, ok := strings.Cut(strings.TrimSpace(arguments), "|")
		if !ok || strings.TrimSpace(query) == "" || strings.TrimSpace(filename) == "" {
			return fmt.Errorf("использование: /mcp mrkpipeline run ЗАПРОС | ФАЙЛ.md")
		}
		encoded, _ := json.Marshal(map[string]string{"query": strings.TrimSpace(query), "filename": strings.TrimSpace(filename)})
		result, err := a.pipeline.CallJSONToolWithProgress(ctx, "run_pipeline", encoded, func(message string) { fmt.Fprintln(out, message) })
		if err == nil {
			return printPipelineResult(out, result)
		}
		return err
	case "call":
		name, raw, ok := strings.Cut(strings.TrimSpace(arguments), " ")
		if name == "" || !ok || strings.TrimSpace(raw) == "" {
			return fmt.Errorf("использование: /mcp mrkpipeline call NAME JSON")
		}
		result, err := a.pipeline.CallJSONToolWithProgress(ctx, name, json.RawMessage(raw), func(message string) { fmt.Fprintln(out, message) })
		if err == nil {
			if name == "run_pipeline" {
				return printPipelineResult(out, result)
			}
			fmt.Fprintln(out, result)
		}
		return err
	default:
		return fmt.Errorf("использование: /mcp mrkpipeline [run ЗАПРОС | ФАЙЛ.md|call NAME JSON]")
	}
}

func runSchedulerMCPCommand(ctx context.Context, a *Agent, rest string, out io.Writer) error {
	if a.scheduler == nil {
		return fmt.Errorf("mrkScheduler недоступен")
	}
	action, arguments, _ := strings.Cut(strings.TrimSpace(rest), " ")
	name := ""
	args := map[string]any{}
	switch action {
	case "", "tools":
		if strings.TrimSpace(arguments) != "" {
			return fmt.Errorf("использование: /mcp mrkscheduler")
		}
		tools, err := a.scheduler.ListTools(ctx)
		if err == nil {
			printMCPTools(out, tools)
		}
		return err
	case "add":
		parts := strings.SplitN(strings.TrimSpace(arguments), " ", 3)
		if len(parts) != 3 {
			return fmt.Errorf("использование: /mcp mrkscheduler add СЕКУНДЫ ПЕРИОД ТЕКСТ")
		}
		delay, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return fmt.Errorf("секунды должны быть целым числом")
		}
		every, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return fmt.Errorf("период должен быть целым числом")
		}
		name = "schedule_reminder"
		args = map[string]any{"delay_seconds": delay, "every_seconds": every, "text": parts[2]}
	case "mrs":
		parts := strings.Fields(arguments)
		if len(parts) < 1 || len(parts) > 2 {
			return fmt.Errorf("использование: /mcp mrkscheduler mrs СЕКУНДЫ [ПРОЕКТ]")
		}
		every, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return fmt.Errorf("период должен быть целым числом секунд")
		}
		name = "schedule_open_merge_requests"
		args = map[string]any{"every_seconds": every}
		if len(parts) == 2 {
			args["project_id"] = parts[1]
		}
	case "list":
		name = "list_reminders"
	case "summary":
		name = "reminder_summary"
	case "cancel":
		name = "cancel_reminder"
		args = map[string]any{"id": strings.TrimSpace(arguments)}
	default:
		return fmt.Errorf("неизвестная команда планировщика")
	}
	if (action == "list" || action == "summary") && strings.TrimSpace(arguments) != "" {
		return fmt.Errorf("лишние аргументы")
	}
	encoded, _ := json.Marshal(args)
	result, err := a.scheduler.CallJSONTool(ctx, name, encoded)
	if err == nil {
		fmt.Fprintln(out, result)
	}
	return err
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
		result, err := a.CallGitLabTool(ctx, name, json.RawMessage(raw))
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
	case "/index":
		err = runIndexCommand(ctx, a, rest, out)
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
		case "mrkscheduler":
			err = runSchedulerMCPCommand(ctx, a, arguments, out)
		case "mrkpipeline":
			err = runPipelineMCPCommand(ctx, a, arguments, out)
		default:
			err = fmt.Errorf("использование: /mcp или /mcp mrkgitlab [mrs [ПРОЕКТ]|call NAME JSON]")
		}
	case "/state":
		if strings.TrimSpace(rest) != "" {
			err = fmt.Errorf("использование: /state")
		} else {
			printState(out, a.Snapshot())
		}
	case "/plan-mode":
		switch strings.TrimSpace(rest) {
		case "enable":
			a.SetPlanMode(true)
			fmt.Fprintln(out, "Режим планирования включён.")
		case "disable":
			a.SetPlanMode(false)
			fmt.Fprintln(out, "Обычный чат включён.")
		default:
			err = fmt.Errorf("использование: /plan-mode enable или /plan-mode disable")
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
		if !a.PlanMode() {
			err = fmt.Errorf("сначала включите /plan-mode enable")
		} else {
			err = a.Pause(rest)
		}
		if err == nil {
			fmt.Fprintln(out, "Задача сохранена и поставлена на паузу.")
		}
	case "/resume":
		if strings.TrimSpace(rest) != "" {
			err = fmt.Errorf("использование: /resume")
		} else if !a.PlanMode() {
			err = fmt.Errorf("сначала включите /plan-mode enable")
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
			err = a.ResetContext(ctx)
		}
		if err == nil {
			if clearer, ok := out.(interface{ clearChat() error }); ok {
				err = clearer.clearChat()
			}
		}
		if err == nil {
			fmt.Fprintln(out, "Чат, задача и сохранённый контекст удалены. Расписание остановлено.")
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

type schedulerMessage struct {
	text      string
	mrSummary bool
}

func schedulerMessages(update SchedulerUpdate) []schedulerMessage {
	if update.Error != "" {
		return []schedulerMessage{{text: fmt.Sprintf("Ошибка планировщика: %s\n", update.Error)}}
	}
	messages := []schedulerMessage{}
	for _, event := range update.Events {
		isMR := false
		for _, result := range update.MRResults {
			if result.JobID == event.JobID {
				isMR = true
				break
			}
		}
		if !isMR {
			messages = append(messages, schedulerMessage{text: fmt.Sprintf("\nНапоминание: %s\n", event.Text)})
		}
	}
	for _, result := range update.MRResults {
		if result.Error != "" {
			messages = append(messages, schedulerMessage{text: fmt.Sprintf("\nНе удалось получить открытые MR: %s\n", result.Error)})
			continue
		}
		var summary strings.Builder
		fmt.Fprintf(&summary, "\nОткрытые MR: %d\n", len(result.MergeRequests))
		for _, mr := range result.MergeRequests {
			fmt.Fprintf(&summary, "\n• %s — %s (@%s)\n", mr.Reference, mr.Title, mr.Author)
			if mr.URL != "" {
				fmt.Fprintln(&summary, " ", mr.URL)
			}
			if mr.Summary != "" {
				fmt.Fprintln(&summary, " ", mr.Summary)
			}
		}
		messages = append(messages, schedulerMessage{text: summary.String(), mrSummary: true})
	}
	return messages
}

func printSchedulerUpdate(out io.Writer, update SchedulerUpdate) {
	for _, message := range schedulerMessages(update) {
		fmt.Fprint(out, message.text)
	}
}

type summaryGate struct {
	lastSummary string
	lastWasMR   bool
}

func (g *summaryGate) otherMessage() {
	g.lastWasMR = false
}

func (g *summaryGate) printUpdate(out io.Writer, update SchedulerUpdate) {
	for _, message := range schedulerMessages(update) {
		if message.mrSummary && g.lastWasMR && g.lastSummary == message.text {
			continue
		}
		fmt.Fprint(out, message.text)
		g.lastWasMR = message.mrSummary
		if message.mrSummary {
			g.lastSummary = message.text
		}
	}
}

func shouldShowSchedulerUpdate(a *Agent, update SchedulerUpdate) bool {
	return update.Generation >= a.SchedulerGeneration()
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
	if parts[1] == "mrkscheduler" {
		return "Планировщик MCP: выполняю команду"
	}
	if parts[1] == "mrkpipeline" && len(parts) > 2 && (parts[2] == "run" || parts[2] == "call") {
		return "Pipeline MCP: выполняю команду"
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

	fmt.Fprintln(display, "mrkai · индексация документов, MCP и диалог · /info — команды")
	if state := a.Snapshot(); state != nil && a.PlanMode() {
		fmt.Fprintln(display, "Загружена сохранённая задача:")
		printState(display, state)
	}
	if footer != nil {
		var statusErr error
		if a.PlanMode() {
			statusErr = footer.setState(a.DisplayState())
		} else {
			statusErr = footer.setPlainChat(false)
		}
		if err := statusErr; err != nil {
			return err
		}
	} else {
		if a.PlanMode() {
			fmt.Fprintln(display, taskStatus(a.DisplayState()))
		} else {
			fmt.Fprintln(display, "Обычный чат")
		}
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
	var summaries summaryGate
	var updates <-chan SchedulerUpdate
	if a.scheduler != nil {
		updates = a.scheduler.updates
	}
	for {
		var line string
		var ok bool
		select {
		case <-readCtx.Done():
			return nil
		case update, open := <-updates:
			if !open {
				updates = nil
				continue
			}
			if !shouldShowSchedulerUpdate(a, update) {
				continue
			}
			summaries.printUpdate(display, update)
			continue
		case interrupt := <-interrupts:
			now := time.Now()
			if isDoubleInterrupt(lastInterruptKey, lastCtrlC, interrupt, now) {
				return nil
			}
			lastCtrlC = now
			lastInterruptKey = interrupt.key
			fmt.Fprintf(display, "Нет активного выполнения. Нажмите %s ещё раз для выхода.\n", interrupt.key)
			summaries.otherMessage()
			continue
		case line, ok = <-lines:
			if !ok {
				return <-readErr
			}
		}
		if strings.TrimSpace(line) != "" {
			summaries.otherMessage()
		}
		thinking := strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "/")
		if footer != nil {
			operation := mcpOperation(line)
			var activityErr error
			if operation != "" {
				activityErr = footer.setOperation(operation)
			} else if thinking {
				if a.PlanMode() {
					activityErr = footer.setActivity(a.DisplayState(), true)
				} else {
					activityErr = footer.setPlainChat(true)
				}
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
		var pendingUpdates []SchedulerUpdate
	waitExecution:
		for {
			select {
			case execution = <-finished:
				break waitExecution
			case update, open := <-updates:
				if open {
					pendingUpdates = append(pendingUpdates, update)
				} else {
					updates = nil
				}
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
	drainUpdates:
		for {
			select {
			case update, open := <-updates:
				if open {
					pendingUpdates = append(pendingUpdates, update)
				} else {
					updates = nil
				}
			default:
				break drainUpdates
			}
		}
		stopExecution()
		pending.Add(-1)
		if footer != nil {
			var stateErr error
			if a.PlanMode() {
				stateErr = footer.setState(a.DisplayState())
			} else {
				stateErr = footer.setPlainChat(false)
			}
			if execution.err == nil {
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
		for _, update := range pendingUpdates {
			if shouldShowSchedulerUpdate(a, update) {
				summaries.printUpdate(display, update)
			}
		}
	}
}
