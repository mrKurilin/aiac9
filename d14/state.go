package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Stage string

const (
	StagePlanning   Stage = "planning"
	StageExecution  Stage = "execution"
	StageValidation Stage = "validation"
	StageDone       Stage = "done"
)

var allowedTransitions = map[Stage]map[Stage]bool{
	StagePlanning:   {StageExecution: true},
	StageExecution:  {StageValidation: true, StagePlanning: true},
	StageValidation: {StageDone: true, StageExecution: true},
	StageDone:       {},
}

type TaskState struct {
	Goal           string    `json:"goal"`
	Stage          Stage     `json:"stage"`
	CurrentStep    string    `json:"current_step"`
	ExpectedAction string    `json:"expected_action"`
	CompletedSteps []string  `json:"completed_steps,omitempty"`
	Paused         bool      `json:"paused"`
	PauseReason    string    `json:"pause_reason,omitempty"`
	History        []Message `json:"history,omitempty"`
}

func NewTaskState(goal string) (TaskState, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return TaskState{}, fmt.Errorf("цель задачи не может быть пустой")
	}
	if len([]rune(goal)) > 2000 {
		return TaskState{}, fmt.Errorf("цель задачи длиннее 2000 символов")
	}
	step, expected := defaultsFor(StagePlanning)
	return TaskState{Goal: goal, Stage: StagePlanning, CurrentStep: step, ExpectedAction: expected}, nil
}

func ParseStage(value string) (Stage, error) {
	stage := Stage(strings.ToLower(strings.TrimSpace(value)))
	if _, ok := allowedTransitions[stage]; !ok {
		return "", fmt.Errorf("этап: planning, execution, validation или done")
	}
	return stage, nil
}

func defaultsFor(stage Stage) (string, string) {
	switch stage {
	case StagePlanning:
		return "Собрать требования и согласовать план", "Опишите ограничения или подтвердите предложенный план"
	case StageExecution:
		return "Выполнить согласованный план", "Дайте рабочую вводную для текущего шага"
	case StageValidation:
		return "Проверить результат и соответствие плану", "Сообщите результаты проверки или найденные замечания"
	case StageDone:
		return "Зафиксировать итог", "Задача завершена; выполните /reset и опишите новую задачу"
	default:
		return "", ""
	}
}

func validateText(label, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s не может быть пустым", label)
	}
	if len([]rune(value)) > 2000 {
		return fmt.Errorf("%s длиннее 2000 символов", label)
	}
	return nil
}

func (s TaskState) Validate() error {
	if err := validateText("цель", s.Goal); err != nil {
		return err
	}
	if _, ok := allowedTransitions[s.Stage]; !ok {
		return fmt.Errorf("неизвестный этап %q", s.Stage)
	}
	if err := validateText("текущий шаг", s.CurrentStep); err != nil {
		return err
	}
	if err := validateText("ожидаемое действие", s.ExpectedAction); err != nil {
		return err
	}
	if len(s.History) > 12 {
		return fmt.Errorf("история содержит больше 12 сообщений")
	}
	return nil
}

func (s *TaskState) Transition(target Stage, currentStep, expectedAction string) error {
	if !allowedTransitions[s.Stage][target] {
		return fmt.Errorf("переход %s → %s запрещён", s.Stage, target)
	}
	currentStep = strings.TrimSpace(currentStep)
	expectedAction = strings.TrimSpace(expectedAction)
	if currentStep == "" && expectedAction == "" {
		currentStep, expectedAction = defaultsFor(target)
	} else if currentStep == "" || expectedAction == "" {
		return fmt.Errorf("для перехода укажите и текущий шаг, и ожидаемое действие")
	}
	if err := validateText("текущий шаг", currentStep); err != nil {
		return err
	}
	if err := validateText("ожидаемое действие", expectedAction); err != nil {
		return err
	}
	if s.CurrentStep != "" {
		s.CompletedSteps = append(s.CompletedSteps, s.CurrentStep)
	}
	s.Stage = target
	s.CurrentStep = currentStep
	s.ExpectedAction = expectedAction
	return nil
}

func (s *TaskState) SetStep(currentStep, expectedAction string) error {
	currentStep, expectedAction = strings.TrimSpace(currentStep), strings.TrimSpace(expectedAction)
	if err := validateText("текущий шаг", currentStep); err != nil {
		return err
	}
	if err := validateText("ожидаемое действие", expectedAction); err != nil {
		return err
	}
	s.CurrentStep = currentStep
	s.ExpectedAction = expectedAction
	return nil
}

func (s *TaskState) Pause(reason string) error {
	if s.Paused {
		return fmt.Errorf("задача уже на паузе")
	}
	s.Paused = true
	s.PauseReason = strings.TrimSpace(reason)
	return nil
}

func (s *TaskState) Resume() error {
	if !s.Paused {
		return fmt.Errorf("задача не находится на паузе")
	}
	s.Paused = false
	s.PauseReason = ""
	return nil
}

type StateStore interface {
	Load() (*TaskState, error)
	Save(TaskState) error
	Delete() error
}

type JSONStateStore struct{ path string }

func NewJSONStateStore(path string) *JSONStateStore {
	return &JSONStateStore{path: path}
}

func (s *JSONStateStore) Load() (*TaskState, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("прочитать состояние: %w", err)
	}
	var state TaskState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("разобрать состояние: %w", err)
	}
	if err := state.Validate(); err != nil {
		return nil, fmt.Errorf("некорректное состояние: %w", err)
	}
	return &state, nil
}

func (s *JSONStateStore) Save(state TaskState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("создать каталог состояния: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".task-state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

func (s *JSONStateStore) Delete() error {
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
