package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// UserProfile is an explicit, curated contract for how MrKai adapts an answer.
// It is deliberately separate from automatically extracted long-term memory.
type UserProfile struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Audience string   `json:"audience"`
	Rules    []string `json:"rules"`
	Accent   string   `json:"accent"`
}

func DefaultUserProfiles() []UserProfile {
	return []UserProfile{
		{
			ID:       "developer",
			Name:     "Разработчик",
			Audience: "Практикующий разработчик, который выбирает алгоритмы для реальных систем.",
			Accent:   "#67e8f9",
			Rules: []string{
				"Отвечай технически и компактно, без объяснения общеизвестных для разработчика терминов.",
				"Давай один пример кода или псевдокода, когда он помогает понять реализацию.",
				"Всегда указывай временную и пространственную сложность и важные компромиссы.",
				"Используй профессиональный русский язык; допустим лёгкий сухой технический юмор.",
			},
		},
		{
			ID:       "student",
			Name:     "Студент",
			Audience: "Изучает алгоритмы и пока не обладает большим практическим опытом.",
			Accent:   "#a78bfa",
			Rules: []string{
				"Объясняй пошагово: сначала интуиция, затем формальное описание.",
				"Давай два примера: бытовую аналогию и небольшой разбор входных данных.",
				"Пиши ответ средней длины и кратко раскрывай каждый новый термин.",
				"Используй дружелюбный русский язык, поддерживающий тон и умеренный юмор.",
			},
		},
		{
			ID:       "business",
			Name:     "Предприниматель",
			Audience: "Принимает продуктовые решения и хочет понимать практическую ценность алгоритмов.",
			Accent:   "#fbbf24",
			Rules: []string{
				"Начинай с короткого вывода: какую проблему решает алгоритм и зачем он нужен бизнесу.",
				"Не показывай код и формулы без прямого запроса пользователя.",
				"Давай один бизнес-пример и отмечай влияние на выгоду, затраты или риски.",
				"Используй простой русский язык без технического жаргона и лишнего юмора.",
			},
		},
	}
}

func DemoPrompts() []string {
	return []string{
		"Объясни алгоритм Дейкстры и расскажи, когда его стоит использовать.",
		"Сравни быструю сортировку и сортировку слиянием. Как выбрать между ними?",
		"Как работают рекомендательные алгоритмы и как понять, что они приносят пользу?",
	}
}

func findUserProfile(profiles []UserProfile, id string) (UserProfile, error) {
	id = strings.TrimSpace(id)
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, nil
		}
	}
	return UserProfile{}, fmt.Errorf("неизвестный профиль %q", id)
}

func profileMessage(profile UserProfile) Message {
	data, _ := json.Marshal(struct {
		Name     string   `json:"name"`
		Audience string   `json:"audience"`
		Rules    []string `json:"response_rules"`
	}{profile.Name, profile.Audience, profile.Rules})
	return Message{
		Role: "system",
		Content: "ПРОФИЛЬ ПОЛЬЗОВАТЕЛЯ. Адаптируй глубину, стиль и формат ответа к этому профилю. " +
			"Профиль не отменяет общие системные правила и требования безопасности: " + string(data),
	}
}
