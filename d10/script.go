package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var messagesPath string

var defaultScenarioBlocks = []string{
	`Собираем ТЗ для проекта «Маяк»: это приложение для записи на групповые занятия по математике. Пользоваться им будет администратор учебного центра. Я буду постепенно сообщать требования. Пока я не попрошу итоговое ТЗ, отвечай только «Принято», без пересказа требований.`,
	`Бюджет разработки — не больше 180000 рублей. Это жёсткий верхний предел, превышать его нельзя.`,
	`Первую рабочую версию нужно подготовить к 20 ноября 2026 года. Пока считаем эту дату обязательной.`,
	`Приложение пишем на Go. Интерфейс только терминальный: без веб-сайта, браузера и мобильного приложения.`,
	`Данные храним локально в JSON-файле, без базы данных и облачных сервисов. Нужен экспорт расписания в CSV.`,
	`Интерфейс и документация должны быть на русском языке. Итоговое ТЗ оформляй короткими пунктами без таблиц.`,
	`Занятия разрешены только по вторникам и четвергам с 18:00 до 21:00 по московскому времени. В группе максимум 8 учеников. По субботам занятия запрещены, даже если есть свободные преподаватели.`,
	`Для ученика храним только псевдоним и выбранную группу. Телефоны, адреса электронной почты и даты рождения не собираем.`,
	`Изменение бюджета: теперь верхний предел — 120000 рублей вместо 180000. Предыдущее значение больше не действует. Остальные требования пока не меняются.`,
	`Отменяем обязательный срок 20 ноября 2026 года. Новая дата пока не согласована. В дальнейшем не указывай старую дату как действующий дедлайн и не придумывай замену.`,
	`Ненадолго отвлечёмся от проекта. Объясни разницу между стеком и очередью в двух предложениях. Это отдельный учебный вопрос, новых требований к проекту здесь нет.`,
	`Ещё один отдельный учебный вопрос: чем сортировка вставками отличается от быстрой сортировки? Ответь в двух предложениях, без привязки к нашему проекту.`,
	`И последний отдельный вопрос: зачем нужен мьютекс? Ответь в двух предложениях. Не пересказывай требования проекта и не добавляй новых.`,
	`Теперь вернёмся к проекту. Составь итоговое ТЗ по актуальным договорённостям из нашего разговора: цель, пользователь, бюджет, срок, технологии, интерфейс, хранение, экспорт, язык, правила занятий и допустимые данные ученика. Можно ответить подробно, но короткими пунктами без таблицы. Не выдумывай потерянные сведения: если чего-то не помнишь, прямо отметь это. Не подменяй действующие требования отменёнными.`,
	`Проверь себя по нашей переписке: какой сейчас предельный бюджет, есть ли согласованная дата готовности, можно ли назначить занятие на субботу, сколько учеников допускается в группе и можно ли хранить телефон ученика? Для каждого ответа укажи, опираешься ли ты на сохранённую договорённость или точных данных у тебя уже нет.`,
}

// script walks the prepared scenario blocks so a run can be replayed without
// copying every message by hand.
type script struct {
	path   string
	blocks []string
	cursor int
	loaded bool
}

// The scenario is the dialogue of the first "##" section: the messages a user
// would type, in order. Commands (blocks starting with "/") are setup and
// inspection steps that the operator runs deliberately, ```sh blocks are shell
// instructions, the checklist bullets are notes, and later sections cover
// branching — none of them belong in an automatic send.
func parseScriptBlocks(source string) []string {
	var blocks []string
	var current []string
	inside, section := false, 0
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inside && strings.HasPrefix(trimmed, "## ") {
			section++
			if section > 1 {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			if inside {
				block := strings.TrimSpace(strings.Join(current, "\n"))
				if block != "" && !strings.HasPrefix(block, "/") {
					blocks = append(blocks, block)
				}
				current, inside = nil, false
				continue
			}
			inside = section == 1 && strings.TrimSpace(strings.TrimPrefix(trimmed, "```")) == "text"
			continue
		}
		if inside {
			current = append(current, line)
		}
	}
	return blocks
}

// Look beside the working directory and beside the binary, so the installed
// mrkai finds the scenario without an absolute path.
func (s *script) load() error {
	if s.loaded {
		return nil
	}
	if strings.TrimSpace(s.path) == "" {
		s.blocks = append([]string(nil), defaultScenarioBlocks...)
		s.loaded = true
		return nil
	}
	candidates := []string{s.path}
	if !filepath.IsAbs(s.path) {
		if executable, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(executable), s.path))
		}
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		blocks := parseScriptBlocks(string(data))
		if len(blocks) == 0 {
			return fmt.Errorf("в файле %s нет блоков ```text", candidate)
		}
		s.blocks, s.loaded = blocks, true
		return nil
	}
	return fmt.Errorf("файл сообщений не найден: %s (задайте MESSAGES_FILE)", s.path)
}

// next reports the line to run next, plus a status note for the terminal.
// An empty line means there is nothing to send and only the note applies.
func (s *script) next(arguments []string) (string, string) {
	if err := s.load(); err != nil {
		return "", "Ошибка: " + err.Error()
	}
	switch {
	case len(arguments) == 0:
	case arguments[0] == "reset":
		s.cursor = 0
		return "", fmt.Sprintf("Сценарий сброшен: следующее сообщение 1/%d.", len(s.blocks))
	case arguments[0] == "list":
		var list strings.Builder
		fmt.Fprintf(&list, "Сценарий %s — %d сообщений:", s.path, len(s.blocks))
		for i, block := range s.blocks {
			marker := " "
			if i == s.cursor {
				marker = "›"
			}
			list.WriteString("\n" + marker + fmt.Sprintf(" %2d. %s", i+1, scriptPreview(block)))
		}
		return "", list.String()
	default:
		number, err := strconv.Atoi(arguments[0])
		if err != nil || number < 1 || number > len(s.blocks) {
			return "", fmt.Sprintf("Укажите номер сообщения 1..%d, list или reset.", len(s.blocks))
		}
		s.cursor = number - 1
	}
	if s.cursor >= len(s.blocks) {
		return "", fmt.Sprintf("Сценарий пройден: %d из %d. /sendNext reset — начать заново.", len(s.blocks), len(s.blocks))
	}
	block := s.blocks[s.cursor]
	s.cursor++
	return block, fmt.Sprintf("Сообщение %d/%d сценария.", s.cursor, len(s.blocks))
}

func scriptPreview(block string) string {
	preview := strings.Join(strings.Fields(block), " ")
	runes := []rune(preview)
	if len(runes) > 60 {
		preview = string(runes[:57]) + "…"
	}
	return preview
}
