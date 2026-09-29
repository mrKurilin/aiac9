package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type TaskState struct {
	Goal        string    `json:"goal"`
	Paused      bool      `json:"paused"`
	PauseReason string    `json:"pause_reason,omitempty"`
	History     []Message `json:"history,omitempty"`
}

func NewTaskState(goal string) (TaskState, error) {
	goal = strings.TrimSpace(goal)
	if err := validateText("цель задачи", goal); err != nil {
		return TaskState{}, err
	}
	return TaskState{Goal: goal}, nil
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
	if err := validateText("цель задачи", s.Goal); err != nil {
		return err
	}
	if len(s.History) > 12 {
		return fmt.Errorf("история содержит больше 12 сообщений")
	}
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
