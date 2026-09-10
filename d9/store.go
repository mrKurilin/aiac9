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

// ConversationState keeps the compressed memory separate from verbatim
// messages on disk as well as in the Agent.
type ConversationState struct {
	Summary string    `json:"summary,omitempty"`
	History []Message `json:"history"`
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
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return ConversationState{}, nil
		}
		return ConversationState{}, err
	}
	var state ConversationState
	if err := json.Unmarshal(data, &state); err != nil {
		// Accept the day 8 array format so an intentionally copied session can
		// be opened without data loss.
		var legacy []Message
		if legacyErr := json.Unmarshal(data, &legacy); legacyErr != nil {
			return ConversationState{}, fmt.Errorf("повреждён файл истории %s: %w", s.path(id), err)
		}
		state.History = legacy
	}
	return state, nil
}

// Save writes the history atomically: first to a temporary file in the same
// directory, then renames it over the target. A crash mid-write therefore
// never leaves a half-written conversation behind.
func (s *JSONFileStore) Save(id string, state ConversationState) error {
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
