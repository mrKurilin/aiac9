package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Invariant struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Rule     string `json:"rule"`
}

func (i Invariant) Validate() error {
	if strings.TrimSpace(i.ID) == "" {
		return fmt.Errorf("идентификатор инварианта не может быть пустым")
	}
	if err := validateInvariantText("категория", i.Category, 120); err != nil {
		return err
	}
	return validateInvariantText("правило", i.Rule, 2000)
}

func validateInvariantText(label, value string, limit int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s не может быть пустой", label)
	}
	if len([]rune(value)) > limit {
		return fmt.Errorf("%s длиннее %d символов", label, limit)
	}
	return nil
}

type InvariantStore interface {
	Load() ([]Invariant, error)
	Save([]Invariant) error
}

type JSONInvariantStore struct{ path string }

func NewJSONInvariantStore(path string) *JSONInvariantStore {
	return &JSONInvariantStore{path: path}
}

func validateInvariants(items []Invariant) error {
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("инвариант %q: %w", item.ID, err)
		}
		if seen[item.ID] {
			return fmt.Errorf("идентификатор %q повторяется", item.ID)
		}
		seen[item.ID] = true
	}
	return nil
}

func (s *JSONInvariantStore) Load() ([]Invariant, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("прочитать инварианты: %w", err)
	}
	var items []Invariant
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("разобрать инварианты: %w", err)
	}
	if err := validateInvariants(items); err != nil {
		return nil, fmt.Errorf("некорректные инварианты: %w", err)
	}
	return append([]Invariant(nil), items...), nil
}

func (s *JSONInvariantStore) Save(items []Invariant) error {
	if err := validateInvariants(items); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("создать каталог инвариантов: %w", err)
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".invariants-*")
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

func nextInvariantID(items []Invariant) string {
	used := make(map[int]bool, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.ID, "INV-") {
			if number, err := strconv.Atoi(strings.TrimPrefix(item.ID, "INV-")); err == nil && number > 0 {
				used[number] = true
			}
		}
	}
	for number := 1; ; number++ {
		if !used[number] {
			return fmt.Sprintf("INV-%03d", number)
		}
	}
}
