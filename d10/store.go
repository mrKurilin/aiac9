package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Store persists a conversation between runs. The agent depends on this small
// interface instead of a concrete backend, so JSON files can be swapped for
// SQLite or a vector store without touching the agent logic.
type Store interface {
	Load(id string) (ConversationState, error)
	Save(id string, state ConversationState) error
}

type Memory struct {
	Mode     string            `json:"strategy"`
	KeepLast int               `json:"keep_last"`
	History  []Message         `json:"history"`
	Facts    map[string]string `json:"facts"`
}
type ConversationState struct {
	Version     int               `json:"version"`
	Mode        string            `json:"strategy"`
	KeepLast    int               `json:"keep_last"`
	History     []Message         `json:"history"`
	Facts       map[string]string `json:"facts"`
	Active      string            `json:"active_branch"`
	Branches    map[string]Memory `json:"branches"`
	Checkpoints map[string]Memory `json:"checkpoints"`
}

func cloneFacts(f map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range f {
		out[k] = v
	}
	return out
}
func cloneMemory(m Memory) Memory {
	m.History = append([]Message(nil), m.History...)
	m.Facts = cloneFacts(m.Facts)
	return m
}
func cloneState(s ConversationState) ConversationState {
	s.History = append([]Message(nil), s.History...)
	s.Facts = cloneFacts(s.Facts)
	branches := map[string]Memory{}
	for k, v := range s.Branches {
		branches[k] = cloneMemory(v)
	}
	s.Branches = branches
	checkpoints := map[string]Memory{}
	for k, v := range s.Checkpoints {
		checkpoints[k] = cloneMemory(v)
	}
	s.Checkpoints = checkpoints
	return s
}

// JSONFileStore keeps one JSON file per conversation: <dir>/<id>.json.
// Files are human-readable, need no extra dependencies and are easy to back up.
type JSONFileStore struct {
	dir string
}

func NewJSONFileStore(dir string) (*JSONFileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("создать каталог данных %s: %w", dir, err)
	}
	return &JSONFileStore{dir: dir}, nil
}

func (s *JSONFileStore) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// Load returns nil when no history exists yet for this conversation.
func (s *JSONFileStore) Load(id string) (ConversationState, error) {
	if !validID(id) {
		return ConversationState{}, fmt.Errorf("неверное имя сессии")
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return ConversationState{}, nil
		}
		return ConversationState{}, err
	}
	var state ConversationState
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("повреждён файл памяти: %w", err)
	}
	if state.Version == 1 {
		state = migrateState(state)
	}
	if state.Version != 2 || !validStrategy(state.Mode) || state.KeepLast < 0 || !validID(state.Active) {
		return state, fmt.Errorf("неподдерживаемый формат памяти d10")
	}
	memories := []Memory{{History: state.History, Facts: state.Facts, Mode: state.Mode, KeepLast: state.KeepLast}}
	for name, memory := range state.Branches {
		if !validID(name) || name == state.Active {
			return state, fmt.Errorf("неверная ветка")
		}
		memories = append(memories, memory)
	}
	for name, memory := range state.Checkpoints {
		if !validID(name) {
			return state, fmt.Errorf("неверный checkpoint")
		}
		memories = append(memories, memory)
	}
	for _, memory := range memories {
		if !validStrategy(memory.Mode) || memory.KeepLast < 0 || len(memory.History) > memory.KeepLast {
			return state, fmt.Errorf("память не соответствует стратегии")
		}
		if err := validateFacts(memory.Facts); err != nil {
			return state, err
		}
		for _, message := range memory.History {
			if message.Role != "user" && message.Role != "assistant" {
				return state, fmt.Errorf("неверная роль в истории")
			}
		}
	}

	return state, nil
}

// Save writes the history atomically: first to a temporary file in the same
// directory, then renames it over the target. A crash mid-write therefore
// never leaves a half-written conversation behind.
func (s *JSONFileStore) Save(id string, state ConversationState) error {
	if !validID(id) {
		return fmt.Errorf("неверное имя сессии")
	}
	if state.History == nil {
		state.History = []Message{}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(s.dir, id+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

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
	return os.Rename(tmpName, s.path(id))
}

// Version 1 treated branching as a strategy. Preserve its history on migration
// by choosing a window large enough for each existing branch/checkpoint.
func migrateState(s ConversationState) ConversationState {
	mode, keep := s.Mode, s.KeepLast
	migrate := func(m Memory) Memory {
		m.Mode = mode
		m.KeepLast = keep
		if mode == "branching" {
			m.Mode = "window"
			if len(m.History) > m.KeepLast {
				m.KeepLast = len(m.History)
			}
		}
		return m
	}
	active := migrate(Memory{History: s.History, Facts: s.Facts})
	s.Mode = active.Mode
	s.KeepLast = active.KeepLast
	for name, m := range s.Branches {
		s.Branches[name] = migrate(m)
	}
	for name, m := range s.Checkpoints {
		s.Checkpoints[name] = migrate(m)
	}
	s.Version = 2
	return s
}
