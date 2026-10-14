package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type supportData struct {
	Users []struct {
		ID     string `json:"id"`
		Plan   string `json:"plan"`
		Status string `json:"status"`
	} `json:"users"`
	Tickets []struct {
		ID      string `json:"id"`
		UserID  string `json:"user_id"`
		Subject string `json:"subject"`
		Details string `json:"details"`
		Status  string `json:"status"`
	} `json:"tickets"`
}

func supportTicket(root, id string) (string, error) {
	if id == "" || len(id) > 64 || strings.ContainsAny(id, "/\\\n\r") {
		return "", fmt.Errorf("неверный ID тикета")
	}
	file, err := os.Open(filepath.Join(root, "support", "tickets.json"))
	if err != nil {
		return "", fmt.Errorf("база тикетов недоступна или некорректна")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return "", fmt.Errorf("не удалось прочитать базу тикетов")
	}
	if len(data) > 1<<20 {
		return "", fmt.Errorf("база тикетов слишком велика")
	}
	var db supportData
	if err := json.Unmarshal(data, &db); err != nil {
		return "", fmt.Errorf("база тикетов недоступна или некорректна")
	}
	for _, ticket := range db.Tickets {
		if ticket.ID != id {
			continue
		}
		for _, user := range db.Users {
			if user.ID == ticket.UserID {
				return fmt.Sprintf("Тикет %s: %s\nОписание: %s\nСтатус: %s\nПользователь: %s\nТариф: %s\nСостояние учётной записи: %s", ticket.ID, ticket.Subject, ticket.Details, ticket.Status, user.ID, user.Plan, user.Status), nil
			}
		}
		return fmt.Sprintf("Тикет %s: %s\nОписание: %s\nСтатус: %s", ticket.ID, ticket.Subject, ticket.Details, ticket.Status), nil
	}
	return "", fmt.Errorf("тикет не найден")
}
