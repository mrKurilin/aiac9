package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

type MemoryLayers struct {
	ShortTerm map[string]string `json:"short"`
	Working   map[string]string `json:"working"`
	LongTerm  map[string]string `json:"long"`
}

type LayerStore interface {
	Load(string) (MemoryLayers, error)
	SaveShortTerm(string, map[string]string) error
	SaveWorking(string, map[string]string) error
	SaveLongTerm(string, map[string]string) error
}

type JSONLayerStore struct{ dir string }

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func NewJSONLayerStore(dir string) (*JSONLayerStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("создать каталог памяти: %w", err)
	}
	return &JSONLayerStore{dir: dir}, nil
}

func (s *JSONLayerStore) layerPath(id, layer string) string {
	return filepath.Join(s.dir, id, layer+".json")
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("повреждён слой памяти %s: %w", filepath.Base(path), err)
	}
	return nil
}

func (s *JSONLayerStore) Load(id string) (MemoryLayers, error) {
	if !idPattern.MatchString(id) {
		return MemoryLayers{}, fmt.Errorf("неверное имя сессии")
	}
	layers := MemoryLayers{ShortTerm: map[string]string{}, Working: map[string]string{}, LongTerm: map[string]string{}}
	// d11 originally stored the full transcript in short-term.json. Preserve
	// manually saved notes while deliberately dropping those legacy messages:
	// persisted memory now contains facts only.
	var legacyShort struct {
		Notes map[string]string `json:"notes"`
	}
	shortPath := s.layerPath(id, "short-term")
	legacyFormat := false
	data, err := os.ReadFile(shortPath)
	if err == nil {
		if json.Unmarshal(data, &layers.ShortTerm) != nil {
			legacyFormat = true
			if err := json.Unmarshal(data, &legacyShort); err != nil {
				return layers, fmt.Errorf("повреждён слой памяти %s: %w", filepath.Base(shortPath), err)
			}
			layers.ShortTerm = legacyShort.Notes
		}
	} else if !os.IsNotExist(err) {
		return layers, err
	}
	for _, item := range []struct {
		name   string
		target any
	}{{"working", &layers.Working}, {"long-term", &layers.LongTerm}} {
		if err := readJSON(s.layerPath(id, item.name), item.target); err != nil {
			return layers, err
		}
	}
	if layers.ShortTerm == nil {
		layers.ShortTerm = map[string]string{}
	}
	if layers.Working == nil {
		layers.Working = map[string]string{}
	}
	if layers.LongTerm == nil {
		layers.LongTerm = map[string]string{}
	}
	for _, layer := range []map[string]string{layers.ShortTerm, layers.Working, layers.LongTerm} {
		if len(layer) > 48 {
			return layers, fmt.Errorf("слой памяти содержит больше 48 фактов")
		}
		for key, value := range layer {
			if err := validateEntry(key, value); err != nil {
				return layers, fmt.Errorf("слой памяти содержит недопустимую запись: %w", err)
			}
		}
	}
	if legacyFormat {
		if err := writeJSONAtomic(shortPath, layers.ShortTerm); err != nil {
			return layers, fmt.Errorf("мигрировать краткосрочную память: %w", err)
		}
	}
	return layers, nil
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memory-*")
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
	return os.Rename(name, path)
}

func (s *JSONLayerStore) SaveShortTerm(id string, memory map[string]string) error {
	return writeJSONAtomic(s.layerPath(id, "short-term"), memory)
}
func (s *JSONLayerStore) SaveWorking(id string, memory map[string]string) error {
	return writeJSONAtomic(s.layerPath(id, "working"), memory)
}
func (s *JSONLayerStore) SaveLongTerm(id string, memory map[string]string) error {
	return writeJSONAtomic(s.layerPath(id, "long-term"), memory)
}
