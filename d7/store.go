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
	Load(id string) ([]Message, error)
	Save(id string, messages []Message) error
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
func (s *JSONFileStore) Load(id string) ([]Message, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var messages []Message
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, fmt.Errorf("повреждён файл истории %s: %w", s.path(id), err)
	}
	return messages, nil
}

// Save writes the history atomically: first to a temporary file in the same
// directory, then renames it over the target. A crash mid-write therefore
// never leaves a half-written conversation behind.
func (s *JSONFileStore) Save(id string, messages []Message) error {
	if messages == nil {
		messages = []Message{}
	}
	data, err := json.MarshalIndent(messages, "", "  ")
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
