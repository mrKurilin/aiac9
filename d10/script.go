package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var messagesPath = "../../lectures/неделя_2/TODOs/10-messages.md"

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
