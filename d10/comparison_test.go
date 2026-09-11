package main

import (
	"context"
	"encoding/json"
	"strings"
)

var comparisonScenario = []string{
	"Цель: сервис записи на занятия",
	"Бюджет: 100000 рублей",
	"Срок: 1 декабря",
	"Платформа: терминал на Go",
	"Ограничение: без персональных данных",
	"Язык: русский",
	"Бюджет: 80000 рублей",
	"Срок: 15 декабря",
	"Обсудим понятные сообщения об ошибках.",
	"Обсудим доступность интерфейса.",
	"Обсудим документацию для оператора.",
	"Собери итоговое ТЗ: перечисли сохранённые требования.",
}

// rehearsalClient is a deterministic context probe, NOT a simulated quality
// score for DeepSeek. It only recognises explicit key:value user statements.
type rehearsalClient struct{}

func readExplicitFacts(facts map[string]string, text string) {
	key, value, ok := strings.Cut(text, ": ")
	if !ok {
		return
	}
	switch key {
	case "Цель", "Бюджет", "Срок", "Платформа", "Ограничение", "Язык":
		facts[key] = value
	}
}
func (rehearsalClient) Complete(_ context.Context, messages []Message) (string, error) {
	facts := map[string]string{}
	if strings.HasPrefix(messages[0].Content, "Обнови facts") {
		var payload struct {
			Facts  map[string]string
			Recent []Message
			User   string
		}
		if err := json.Unmarshal([]byte(messages[len(messages)-1].Content), &payload); err != nil {
			return "", err
		}
		facts = cloneFacts(payload.Facts)
		readExplicitFacts(facts, payload.User)
	} else {
		for _, message := range messages {
			if strings.HasPrefix(message.Content, factsContextPrefix) {
				if err := json.Unmarshal([]byte(strings.TrimPrefix(message.Content, factsContextPrefix)), &facts); err != nil {
					return "", err
				}
			}
			if message.Role == "user" {
				readExplicitFacts(facts, message.Content)
			}
		}
		if !strings.HasPrefix(messages[len(messages)-1].Content, "Собери итоговое ТЗ") {
			return "Принято.", nil
		}
	}
	data, _ := json.Marshal(facts)
	return string(data), nil
}

type comparisonResult struct {
	Mode, Answer                   string
	Retained, Calls, Input, Output int
}

func compareScenario(ctx context.Context) ([]comparisonResult, error) {
	results := []comparisonResult{}
	expected := map[string]string{}
	for _, prompt := range comparisonScenario {
		readExplicitFacts(expected, prompt)
	}
	for _, mode := range []string{"window", "facts", "branch (window N=24)"} {
		meter := &Meter{Client: rehearsalClient{}}
		a := NewAgent(meter, "Составляй ТЗ по требованиям пользователя.", nil, "comparison")
		a.extractor = LLMFacts{Client: meter}
		contextMode, keep := mode, 4
		if mode == "branch (window N=24)" {
			contextMode, keep = "window", 24
		}
		if err := a.Configure(contextMode, keep); err != nil {
			return nil, err
		}
		if mode == "branch (window N=24)" {
			if err := a.BranchCommand("branch", "scenario"); err != nil {
				return nil, err
			}
		}
		answer := ""
		for _, prompt := range comparisonScenario {
			var err error
			answer, err = a.Ask(ctx, prompt)
			if err != nil {
				return nil, err
			}
		}
		actual := map[string]string{}
		if err := json.Unmarshal([]byte(answer), &actual); err != nil {
			return nil, err
		}
		retained := 0
		for key, value := range expected {
			if actual[key] == value {
				retained++
			}
		}
		results = append(results, comparisonResult{mode, answer, retained, meter.Turns, meter.Input, meter.Output})
	}
	return results, nil
}
