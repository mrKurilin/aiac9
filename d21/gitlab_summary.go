package main

import (
	"fmt"
	"strings"
)

const mergeRequestDescriptionLimit = 160

type MergeRequestSummary struct {
	ProjectID int    `json:"project_id"`
	IID       int    `json:"iid"`
	Reference string `json:"reference"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	URL       string `json:"url"`
	Summary   string `json:"summary"`
}

var mergeStatusText = map[string]string{
	"mergeable":                "можно вливать",
	"conflict":                 "есть конфликты",
	"need_rebase":              "нужен rebase",
	"ci_must_pass":             "ждёт успешного CI",
	"ci_still_running":         "CI ещё выполняется",
	"not_approved":             "нужны аппрувы",
	"requested_changes":        "запрошены изменения",
	"discussions_not_resolved": "есть нерешённые обсуждения",
	"blocked_status":           "заблокирован другим MR",
	"external_status_checks":   "ждёт внешних проверок",
	"checking":                 "статус слияния проверяется",
	"unchecked":                "статус слияния проверяется",
	"preparing":                "статус слияния проверяется",
}

func summarizeMergeRequests(mrs []MergeRequest) []MergeRequestSummary {
	summaries := make([]MergeRequestSummary, 0, len(mrs))
	for _, mr := range mrs {
		summaries = append(summaries, MergeRequestSummary{
			ProjectID: mr.ProjectID,
			IID:       mr.IID,
			Reference: mr.References.Full,
			Title:     mr.Title,
			Author:    mr.Author.Username,
			URL:       mr.WebURL,
			Summary:   mergeRequestSummary(mr),
		})
	}
	return summaries
}

func mergeRequestSummary(mr MergeRequest) string {
	parts := make([]string, 0, 6)
	if mr.Draft {
		parts = append(parts, "черновик")
	} else {
		parts = append(parts, "готов к ревью")
	}
	if mr.SourceBranch != "" && mr.TargetBranch != "" {
		parts = append(parts, mr.SourceBranch+" → "+mr.TargetBranch)
	}
	if mr.HasConflicts {
		parts = append(parts, mergeStatusText["conflict"])
	} else if status := mergeStatusText[mr.DetailedMergeStatus]; status != "" {
		parts = append(parts, status)
	}
	if mr.UserNotesCount > 0 {
		parts = append(parts, fmt.Sprintf("комментариев: %d", mr.UserNotesCount))
	}
	if date, _, _ := strings.Cut(mr.UpdatedAt, "T"); date != "" {
		parts = append(parts, "обновлён "+date)
	}
	summary := strings.Join(parts, "; ")
	if description := firstDescriptionLine(mr.Description); description != "" {
		summary += ". " + description
	}
	return summary
}

func firstDescriptionLine(description string) string {
	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#>*- "))
		if line == "" || strings.HasPrefix(line, "<!--") {
			continue
		}
		runes := []rune(line)
		if len(runes) > mergeRequestDescriptionLimit {
			return string(runes[:mergeRequestDescriptionLimit]) + "…"
		}
		return line
	}
	return ""
}
