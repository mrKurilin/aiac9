package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

func strategyLabel(mode string) string {
	if mode == "facts" {
		return "Sticky Facts / Key-Value Memory"
	}
	return "Sliding Window"
}

// Facts belong to the Sticky Facts strategy only: a permanent "facts 0" next to
// Sliding Window describes a store that strategy never fills.
func strategyState(s ConversationState) string {
	state := fmt.Sprintf("%s · N=%d · сообщений %d", strategyLabel(s.Mode), s.KeepLast, len(s.History))
	if s.Mode == "facts" {
		state += fmt.Sprintf(" · facts %d", len(s.Facts))
	}
	return state
}
func (a *Agent) branchNames() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	names := []string{a.active}
	for name := range a.branches {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func memoryCommand(a *Agent, line string, out io.Writer) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	var err error
	switch fields[0] {
	case "/contextManagementStrategy":
		if len(fields) == 1 {
			fmt.Fprintln(out, "Выберите: Sliding Window — /contextManagementStrategy window; Sticky Facts / Key-Value Memory — /contextManagementStrategy facts. Без числа размер окна — 5 сообщений. Можно указать другое N.")
			return true
		}
		keep := defaultKeepLast
		if len(fields) == 3 {
			keep, err = strconv.Atoi(fields[2])
		}
		if len(fields) > 3 || err != nil || !validStrategy(fields[1]) || keep < 0 {
			fmt.Fprintln(out, "Использование: /contextManagementStrategy window|facts [N], N >= 0")
			return true
		}
		err = a.Configure(fields[1], keep)
		if err == nil {
			fmt.Fprintf(out, "Управление контекстом: %s · N=%d. Продолжайте диалог.\n", strategyLabel(fields[1]), keep)
		}
	case "/checkpoint", "/addBranch":
		command := "checkpoint"
		if fields[0] == "/addBranch" {
			command = "branch"
		}
		err = a.BranchCommand(command, fields[1:]...)
		if err == nil {
			fmt.Fprintln(out, "Готово:", line)
			memoryCommand(a, "/memory", out)
		}
	case "/branch":
		if len(fields) == 1 {
			a.mu.Lock()
			active := a.active
			a.mu.Unlock()
			for _, name := range a.branchNames() {
				suffix := ""
				if name == active {
					suffix = " (активная)"
				}
				fmt.Fprintf(out, "  %s%s — /branch %s\n", name, suffix, name)
			}
			return true
		}
		err = a.BranchCommand("switch", fields[1:]...)
		if err == nil {
			memoryCommand(a, "/memory", out)
		}
	case "/memory", "/facts":
		if len(fields) != 1 {
			fmt.Fprintln(out, "Команда без аргументов")
			return true
		}
		a.mu.Lock()
		s := a.snapshot()
		a.mu.Unlock()
		fmt.Fprintf(out, "%s · ветка %s\n", strategyState(s), s.Active)
		if fields[0] == "/facts" {
			data, _ := json.MarshalIndent(s.Facts, "", "  ")
			fmt.Fprintln(out, string(data))
		}
		if fields[0] == "/memory" && len(s.Checkpoints) > 0 {
			names := []string{}
			for name := range s.Checkpoints {
				names = append(names, name)
			}
			sort.Strings(names)
			fmt.Fprintln(out, "Checkpoints:", strings.Join(names, ", "))
		}
	default:
		return false
	}
	if err != nil {
		fmt.Fprintln(out, "Ошибка:", err)
	}
	return true
}
