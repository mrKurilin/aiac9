package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskPauseResumeAndLegacyStageMigration(t *testing.T) {
	state, err := NewTaskState("Задача")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Pause("ожидание"); err != nil {
		t.Fatal(err)
	}
	if err := state.Resume(); err != nil || state.Paused {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	path := filepath.Join(t.TempDir(), "task-state.json")
	legacy := `{"goal":"Старая задача","stage":"planning","current_step":"План","plan_approved":false,"history":[{"role":"user","content":"Привет"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewJSONStateStore(path)
	loaded, err := store.Load()
	if err != nil || loaded.Goal != "Старая задача" || len(loaded.History) != 1 {
		t.Fatalf("state=%+v err=%v", loaded, err)
	}
	if err := store.Save(*loaded); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"stage"`) || strings.Contains(string(data), `"current_step"`) {
		t.Fatalf("legacy fields saved: %s", data)
	}
}
