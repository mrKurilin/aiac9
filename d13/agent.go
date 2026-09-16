package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

type TurnResult struct {
	Answer       string
	Notice       string
	TaskCreated  bool
	StageChanged bool
	Stage        Stage
}

type modelDecision struct {
	Answer         string `json:"answer"`
	NextStage      string `json:"next_stage"`
	CurrentStep    string `json:"current_step"`
	ExpectedAction string `json:"expected_action"`
}

type taskDiscovery struct {
	Answer         string `json:"answer"`
	TaskReady      bool   `json:"task_ready"`
	Goal           string `json:"goal"`
	CurrentStep    string `json:"current_step"`
	ExpectedAction string `json:"expected_action"`
}

var explicitPlanApprovalPattern = regexp.MustCompile(`(?i)(утверждаю|подтверждаю(\s+план)?|согласен\s+с\s+планом|план\s+(утвержд[её]н|согласован|подходит)|approve(d)?\s+(the\s+)?plan|plan\s+approved)`)
var shortPlanApprovalPattern = regexp.MustCompile(`(?i)^\s*(вс[её]\s+ок|согласовано|утверждено|приступай(\s+к\s+реализации)?)\s*[!.]?\s*$`)
var implementationRequestPattern = regexp.MustCompile(`(?i)(давай\s+(сразу\s+)?(писать\s+код|к\s+коду)|(пиши|напиши|реализуй)\s+(код|функц\w*|сервис\w*|прилож\w*|модул\w*)|(пропусти|skip).{0,24}(план|plan)|start\s+coding|write\s+(the\s+)?code)`)
var stageRequestPattern = regexp.MustCompile(`(?i)(переходим|перейди|переходи|вернись|давай)\s+(на|в|к)\s+(этап\s+)?(planning|планирован\p{L}*|execution|исполнен\p{L}*|реализац\p{L}*|validation|валидац\p{L}*|проверк\p{L}*|done|завершен\p{L}*)`)

func requestedStage(prompt string) (Stage, bool) {
	match := stageRequestPattern.FindStringSubmatch(prompt)
	if len(match) < 5 {
		return "", false
	}
	value := strings.ToLower(match[4])
	switch {
	case value == "planning" || strings.HasPrefix(value, "планирован"):
		return StagePlanning, true
	case value == "execution" || strings.HasPrefix(value, "исполнен") || strings.HasPrefix(value, "реализац"):
		return StageExecution, true
	case value == "validation" || strings.HasPrefix(value, "валидац") || strings.HasPrefix(value, "проверк"):
		return StageValidation, true
	case value == "done" || strings.HasPrefix(value, "завершен"):
		return StageDone, true
	default:
		return "", false
	}
}

func approvesPlan(prompt string) bool {
	if target, ok := requestedStage(prompt); ok && target == StageExecution {
		return true
	}
	return explicitPlanApprovalPattern.MatchString(prompt) || shortPlanApprovalPattern.MatchString(prompt)
}

type Agent struct {
	mu      sync.Mutex
	client  ChatClient
	system  string
	store   StateStore
	state   *TaskState
	draft   []Message
	loadErr error
}

func NewAgent(client ChatClient, system string, store StateStore) *Agent {
	a := &Agent{client: client, system: strings.TrimSpace(system), store: store}
	if store != nil {
		a.state, a.loadErr = store.Load()
	}
	return a
}

func cloneState(state *TaskState) *TaskState {
	if state == nil {
		return nil
	}
	copy := *state
	copy.CompletedSteps = append([]string(nil), state.CompletedSteps...)
	copy.History = append([]Message(nil), state.History...)
	return &copy
}

func (a *Agent) Snapshot() *TaskState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneState(a.state)
}

func (a *Agent) save(next TaskState) error {
	if a.store != nil {
		if err := a.store.Save(next); err != nil {
			return fmt.Errorf("сохранить состояние: %w", err)
		}
	}
	a.state = cloneState(&next)
	return nil
}

func (a *Agent) StartTask(goal string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.loadErr != nil {
		return a.loadErr
	}
	if a.state != nil && a.state.Stage != StageDone {
		return fmt.Errorf("сначала завершите текущую задачу или выполните /reset")
	}
	next, err := NewTaskState(goal)
	if err != nil {
		return err
	}
	a.draft = nil
	return a.save(next)
}

func (a *Agent) Pause(reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == nil {
		return fmt.Errorf("сначала опишите задачу обычным сообщением")
	}
	next := *cloneState(a.state)
	if err := next.Pause(reason); err != nil {
		return err
	}
	return a.save(next)
}

func (a *Agent) Resume() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == nil {
		return fmt.Errorf("нет сохранённой задачи")
	}
	next := *cloneState(a.state)
	if err := next.Resume(); err != nil {
		return err
	}
	return a.save(next)
}

func (a *Agent) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store != nil {
		if err := a.store.Delete(); err != nil {
			return fmt.Errorf("удалить состояние: %w", err)
		}
	}
	a.state = nil
	a.draft = nil
	a.loadErr = nil
	return nil
}

func buildMessages(system string, state TaskState, prompt string) []Message {
	stateView := struct {
		Goal           string   `json:"goal"`
		Stage          Stage    `json:"stage"`
		CurrentStep    string   `json:"current_step"`
		ExpectedAction string   `json:"expected_action"`
		Completed      []string `json:"completed_steps,omitempty"`
	}{state.Goal, state.Stage, state.CurrentStep, state.ExpectedAction, state.CompletedSteps}
	encoded, _ := json.Marshal(stateView)
	messages := make([]Message, 0, len(state.History)+3)
	if system != "" {
		messages = append(messages, Message{Role: "system", Content: system})
	}
	messages = append(messages, Message{Role: "system", Content: "TASK_STATE: " + string(encoded) +
		"\nРаботай только в рамках current_step. Учитывай completed_steps и не проси повторять уже сохранённые сведения. " +
		`Верни только JSON: {"answer":"ответ пользователю","next_stage":"","current_step":"текущий шаг","expected_action":"что ожидается дальше"}. ` +
		"next_stage оставь пустым, пока этап не завершён. После явного утверждения плана предложи execution; " +
		"в execution самостоятельно выполни весь утверждённый план без подтверждения каждого шага и предложи validation после завершения реализации; " +
		"после завершения реализации — validation; после успешной проверки — done; при замечаниях проверки — execution. " +
		"Код проверит переход: не обещай перейти на запрещённый этап."})
	messages = append(messages, state.History...)
	messages = append(messages, Message{Role: "user", Content: prompt})
	return messages
}

func buildDiscoveryMessages(system string, draft []Message, prompt string) []Message {
	messages := make([]Message, 0, len(draft)+3)
	if system != "" {
		messages = append(messages, Message{Role: "system", Content: system})
	}
	messages = append(messages, Message{Role: "system", Content: `Определи, достаточно ли ясно сформулирована пользовательская задача. Верни только JSON и сохрани указанный порядок полей: {"task_ready":false,"answer":"краткое подтверждение или один уточняющий вопрос","goal":"краткая цель задачи","current_step":"первый шаг планирования","expected_action":"что ожидается дальше"}. Если задача ясна, поставь task_ready=true, заполни goal, current_step="Составить план" и expected_action="Агент автоматически составляет план". На этом техническом шаге не составляй сам план: интерфейс сразу после создания задачи запустит для него отдельный потоковый запрос. Если задача не ясна, не выдумывай цель: task_ready=false, goal оставь пустым и задай один самый важный уточняющий вопрос.`})
	messages = append(messages, draft...)
	messages = append(messages, Message{Role: "user", Content: prompt})
	return messages
}

const automaticPlanningPrompt = "Составь конкретный пошаговый план выполнения текущей задачи. Не приступай к реализации. После плана попроси пользователя утвердить его или перечислить правки."
const automaticExecutionPrompt = "Приступай к реализации по утверждённому плану прямо сейчас. Выполни все шаги этапа execution последовательно без промежуточных подтверждений пользователя и не останавливайся после каждого шага. Запрашивай пользователя только если без отсутствующих данных продолжение объективно невозможно. Когда реализация полностью закончена, предложи переход в validation."

func jsonObject(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return "", false
	}
	return raw[start : end+1], true
}

func decodeDecision(raw string) modelDecision {
	raw = strings.TrimSpace(raw)
	object, ok := jsonObject(raw)
	if !ok {
		return modelDecision{Answer: raw}
	}
	var decision modelDecision
	if err := json.Unmarshal([]byte(object), &decision); err != nil {
		return modelDecision{Answer: raw}
	}
	decision.Answer = strings.TrimSpace(decision.Answer)
	decision.NextStage = strings.TrimSpace(decision.NextStage)
	decision.CurrentStep = strings.TrimSpace(decision.CurrentStep)
	decision.ExpectedAction = strings.TrimSpace(decision.ExpectedAction)
	return decision
}

func decodeDiscovery(raw string) taskDiscovery {
	raw = strings.TrimSpace(raw)
	object, ok := jsonObject(raw)
	if !ok {
		return taskDiscovery{Answer: raw}
	}
	var discovery taskDiscovery
	if err := json.Unmarshal([]byte(object), &discovery); err != nil {
		return taskDiscovery{Answer: raw}
	}
	discovery.Answer = strings.TrimSpace(discovery.Answer)
	discovery.Goal = strings.TrimSpace(discovery.Goal)
	discovery.CurrentStep = strings.TrimSpace(discovery.CurrentStep)
	discovery.ExpectedAction = strings.TrimSpace(discovery.ExpectedAction)
	return discovery
}

type answerStreamDecoder struct {
	header  string
	pending []byte
	started bool
	done    bool
}

func answerValueStart(raw string) (int, bool) {
	key := strings.Index(raw, `"answer"`)
	if key < 0 {
		return 0, false
	}
	position := key + len(`"answer"`)
	for position < len(raw) && (raw[position] == ' ' || raw[position] == '\n' || raw[position] == '\r' || raw[position] == '\t') {
		position++
	}
	if position >= len(raw) || raw[position] != ':' {
		return 0, false
	}
	position++
	for position < len(raw) && (raw[position] == ' ' || raw[position] == '\n' || raw[position] == '\r' || raw[position] == '\t') {
		position++
	}
	if position >= len(raw) || raw[position] != '"' {
		return 0, false
	}
	return position + 1, true
}

func (d *answerStreamDecoder) feed(chunk string, emit func(string) error) error {
	if d.done || chunk == "" {
		return nil
	}
	if !d.started {
		d.header += chunk
		start, ok := answerValueStart(d.header)
		if !ok {
			return nil
		}
		d.pending = append(d.pending, d.header[start:]...)
		d.header = ""
		d.started = true
	} else {
		d.pending = append(d.pending, chunk...)
	}
	var result strings.Builder
	position := 0
	for position < len(d.pending) {
		if d.pending[position] == '"' {
			d.done = true
			position++
			break
		}
		if d.pending[position] != '\\' {
			if !utf8.FullRune(d.pending[position:]) {
				break
			}
			r, size := utf8.DecodeRune(d.pending[position:])
			result.WriteRune(r)
			position += size
			continue
		}
		if position+1 >= len(d.pending) {
			break
		}
		escape := d.pending[position+1]
		switch escape {
		case '"', '\\', '/':
			result.WriteByte(escape)
			position += 2
		case 'b':
			result.WriteByte('\b')
			position += 2
		case 'f':
			result.WriteByte('\f')
			position += 2
		case 'n':
			result.WriteByte('\n')
			position += 2
		case 'r':
			result.WriteByte('\r')
			position += 2
		case 't':
			result.WriteByte('\t')
			position += 2
		case 'u':
			if position+6 > len(d.pending) {
				goto decodeDone
			}
			value, err := strconv.ParseUint(string(d.pending[position+2:position+6]), 16, 16)
			if err != nil {
				return fmt.Errorf("разобрать unicode escape в answer: %w", err)
			}
			r := rune(value)
			if utf16.IsSurrogate(r) {
				if position+12 > len(d.pending) {
					goto decodeDone
				}
				if string(d.pending[position+6:position+8]) != `\u` {
					return fmt.Errorf("некорректная surrogate pair в answer")
				}
				low, err := strconv.ParseUint(string(d.pending[position+8:position+12]), 16, 16)
				if err != nil {
					return fmt.Errorf("разобрать unicode escape в answer: %w", err)
				}
				r = utf16.DecodeRune(r, rune(low))
				position += 12
			} else {
				position += 6
			}
			result.WriteRune(r)
		default:
			return fmt.Errorf("неизвестная escape-последовательность в answer")
		}
	}
decodeDone:
	if d.done {
		d.pending = nil
	} else if position > 0 {
		d.pending = append([]byte(nil), d.pending[position:]...)
	}
	if result.Len() > 0 {
		return emit(result.String())
	}
	return nil
}

type discoveryStreamDecoder struct {
	header     string
	ready      bool
	determined bool
	answer     answerStreamDecoder
}

func taskReadyValue(raw string) (bool, bool) {
	key := strings.Index(raw, `"task_ready"`)
	if key < 0 {
		return false, false
	}
	rest := strings.TrimSpace(raw[key+len(`"task_ready"`):])
	if !strings.HasPrefix(rest, ":") {
		return false, false
	}
	rest = strings.TrimSpace(rest[1:])
	if strings.HasPrefix(rest, "true") {
		return true, true
	}
	if strings.HasPrefix(rest, "false") {
		return false, true
	}
	return false, false
}

func (d *discoveryStreamDecoder) feed(chunk string, emit func(string) error) error {
	if d.determined {
		if d.ready {
			return nil
		}
		return d.answer.feed(chunk, emit)
	}
	d.header += chunk
	ready, ok := taskReadyValue(d.header)
	if !ok {
		return nil
	}
	d.ready, d.determined = ready, true
	if ready {
		d.header = ""
		return nil
	}
	header := d.header
	d.header = ""
	return d.answer.feed(header, emit)
}

func (a *Agent) complete(ctx context.Context, messages []Message, emit func(string) error) (string, error) {
	streaming, ok := a.client.(StreamingChatClient)
	if !ok || emit == nil {
		return a.client.Complete(ctx, messages)
	}
	decoder := &answerStreamDecoder{}
	return streaming.CompleteStream(ctx, messages, func(chunk string) error {
		return decoder.feed(chunk, emit)
	})
}

func (a *Agent) completeDiscovery(ctx context.Context, messages []Message, emit func(string) error) (string, error) {
	streaming, ok := a.client.(StreamingChatClient)
	if !ok || emit == nil {
		return a.client.Complete(ctx, messages)
	}
	decoder := &discoveryStreamDecoder{}
	return streaming.CompleteStream(ctx, messages, func(chunk string) error {
		return decoder.feed(chunk, emit)
	})
}

func appendHistory(state *TaskState, prompt, answer string) {
	state.History = append(state.History,
		Message{Role: "user", Content: prompt},
		Message{Role: "assistant", Content: answer},
	)
	if len(state.History) > 12 {
		state.History = append([]Message(nil), state.History[len(state.History)-12:]...)
	}
}

func (a *Agent) applyDecision(next *TaskState, prompt string, decision modelDecision) TurnResult {
	result := TurnResult{Answer: decision.Answer, Stage: next.Stage}
	if decision.NextStage == "" {
		if decision.CurrentStep != "" && decision.ExpectedAction != "" {
			if err := next.SetStep(decision.CurrentStep, decision.ExpectedAction); err != nil {
				result.Notice = "Состояние шага не обновлено: " + err.Error()
			}
		}
		return result
	}
	target, err := ParseStage(decision.NextStage)
	if err != nil {
		result.Notice = "Переход отклонён: " + err.Error()
		return result
	}
	if target == next.Stage {
		if decision.CurrentStep != "" && decision.ExpectedAction != "" {
			if err := next.SetStep(decision.CurrentStep, decision.ExpectedAction); err != nil {
				result.Notice = "Состояние шага не обновлено: " + err.Error()
			}
		}
		return result
	}
	if next.Stage == StagePlanning && target == StageExecution && !approvesPlan(prompt) {
		result.Notice = "переход planning → execution запрещён: сначала согласуйте и явно утвердите план"
		return result
	}
	if err := next.Transition(target, decision.CurrentStep, decision.ExpectedAction); err != nil {
		result.Notice = err.Error()
		return result
	}
	result.StageChanged = true
	result.Stage = target
	return result
}

func (a *Agent) discoverTask(ctx context.Context, prompt string, emit func(string) error) (TurnResult, error) {
	raw, err := a.completeDiscovery(ctx, buildDiscoveryMessages(a.system, a.draft, prompt), emit)
	if err != nil {
		return TurnResult{}, err
	}
	discovery := decodeDiscovery(raw)
	if !discovery.TaskReady || discovery.Goal == "" {
		answer := discovery.Answer
		if answer == "" {
			answer = "Уточните, пожалуйста, какой результат нужно получить."
		}
		a.draft = append(a.draft,
			Message{Role: "user", Content: prompt},
			Message{Role: "assistant", Content: answer},
		)
		if len(a.draft) > 8 {
			a.draft = append([]Message(nil), a.draft[len(a.draft)-8:]...)
		}
		return TurnResult{Answer: answer}, nil
	}
	next, err := NewTaskState(discovery.Goal)
	if err != nil {
		return TurnResult{}, err
	}
	if discovery.CurrentStep != "" && discovery.ExpectedAction != "" {
		if err := next.SetStep(discovery.CurrentStep, discovery.ExpectedAction); err != nil {
			return TurnResult{}, err
		}
	}
	answer := discovery.Answer
	if answer == "" {
		answer = "Задача понятна. Начинаю планирование."
	}
	for _, message := range a.draft {
		next.History = append(next.History, message)
	}
	appendHistory(&next, prompt, answer)
	if len(next.History) > 12 {
		next.History = append([]Message(nil), next.History[len(next.History)-12:]...)
	}
	if err := a.save(next); err != nil {
		return TurnResult{}, err
	}
	a.draft = nil
	return TurnResult{Answer: answer, TaskCreated: true, Stage: StagePlanning}, nil
}

func (a *Agent) Ask(ctx context.Context, prompt string) (TurnResult, error) {
	return a.AskStream(ctx, prompt, nil)
}

func (a *Agent) AskStream(ctx context.Context, prompt string, emit func(string) error) (TurnResult, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return TurnResult{}, fmt.Errorf("пустой запрос")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.loadErr != nil {
		return TurnResult{}, a.loadErr
	}
	if a.state == nil {
		return a.discoverTask(ctx, prompt, emit)
	}
	if a.state.Paused {
		return TurnResult{}, fmt.Errorf("задача на паузе; выполните /resume")
	}
	if a.state.Stage == StageDone {
		return TurnResult{}, fmt.Errorf("задача завершена; выполните /reset и опишите новую")
	}
	if a.state.Stage == StagePlanning && implementationRequestPattern.MatchString(prompt) && !approvesPlan(prompt) {
		next := *cloneState(a.state)
		result := TurnResult{
			Notice: "переход planning → execution запрещён: сначала согласуйте и явно утвердите план",
			Stage:  next.Stage,
		}
		appendHistory(&next, prompt, "Уведомление: "+result.Notice)
		if err := a.save(next); err != nil {
			return TurnResult{}, err
		}
		return result, nil
	}
	raw, err := a.complete(ctx, buildMessages(a.system, *a.state, prompt), emit)
	if err != nil {
		return TurnResult{}, err
	}
	decision := decodeDecision(raw)
	if target, ok := requestedStage(prompt); ok {
		decision.NextStage = string(target)
		decision.CurrentStep = ""
		decision.ExpectedAction = ""
	}
	next := *cloneState(a.state)
	result := a.applyDecision(&next, prompt, decision)
	storedAnswer := result.Answer
	if result.Notice != "" {
		if storedAnswer != "" {
			storedAnswer += "\n"
		}
		storedAnswer += "Уведомление: " + result.Notice
	}
	appendHistory(&next, prompt, storedAnswer)
	if err := a.save(next); err != nil {
		return TurnResult{}, err
	}
	return result, nil
}
