package main

import (
	"context"
	"strings"
	"testing"
)

func TestDefaultProfilesAreDistinctAndComplete(t *testing.T) {
	profiles := DefaultUserProfiles()
	if len(profiles) != 3 {
		t.Fatalf("profiles=%d", len(profiles))
	}
	seen := map[string]bool{}
	for _, profile := range profiles {
		if profile.ID == "" || profile.Name == "" || profile.Audience == "" || len(profile.Rules) != 4 {
			t.Errorf("incomplete profile: %+v", profile)
		}
		if seen[profile.ID] {
			t.Errorf("duplicate profile id %q", profile.ID)
		}
		seen[profile.ID] = true
	}
	if len(DemoPrompts()) != 3 {
		t.Fatalf("demo prompts=%d", len(DemoPrompts()))
	}
}

func TestProfileIsInjectedIntoEveryRequest(t *testing.T) {
	profile := DefaultUserProfiles()[0]
	client := &captureClient{}
	agent := NewAgent(client, "base system", nil, "profile-test", profile)
	for _, prompt := range []string{"Первый вопрос", "Второй вопрос"} {
		if _, err := agent.Ask(context.Background(), prompt, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(client.calls) != 2 {
		t.Fatalf("calls=%d", len(client.calls))
	}
	for index, call := range client.calls {
		joined := ""
		for _, message := range call {
			joined += "\n" + message.Content
		}
		if !strings.Contains(joined, "ПРОФИЛЬ ПОЛЬЗОВАТЕЛЯ") || !strings.Contains(joined, profile.Audience) {
			t.Errorf("call %d misses profile: %s", index, joined)
		}
		for _, rule := range profile.Rules {
			if !strings.Contains(joined, rule) {
				t.Errorf("call %d misses rule %q", index, rule)
			}
		}
	}
}

func TestSameQuestionBuildsDifferentContextForProfiles(t *testing.T) {
	profiles := DefaultUserProfiles()
	contexts := map[string]string{}
	for _, profile := range profiles {
		client := &captureClient{}
		agent := NewAgent(client, "base", nil, "profile-"+profile.ID, profile)
		if _, err := agent.Ask(context.Background(), "Объясни Дейкстру", nil); err != nil {
			t.Fatal(err)
		}
		for _, message := range client.calls[0] {
			contexts[profile.ID] += message.Content
		}
	}
	if contexts["developer"] == contexts["student"] || contexts["student"] == contexts["business"] || contexts["developer"] == contexts["business"] {
		t.Fatalf("profile contexts must differ: %+v", contexts)
	}
}

func TestProfileMemoriesUseSeparatePersistentNamespaces(t *testing.T) {
	store, err := NewJSONLayerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profiles := DefaultUserProfiles()
	developer := NewAgent(&captureClient{}, "", store, "profile-developer", profiles[0])
	student := NewAgent(&captureClient{}, "", store, "profile-student", profiles[1])
	if err := developer.Remember("long", "algorithm", "Dijkstra"); err != nil {
		t.Fatal(err)
	}
	if len(student.Snapshot().LongTerm) != 0 {
		t.Fatal("developer memory leaked into student namespace")
	}
	restored := NewAgent(&captureClient{}, "", store, "profile-developer", profiles[0])
	if restored.Snapshot().LongTerm["algorithm"] != "Dijkstra" {
		t.Fatal("developer memory was not restored")
	}
}
