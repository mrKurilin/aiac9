package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type SchedulerUpdate struct {
	Generation int64               `json:"generation"`
	Events     []ScheduledEvent    `json:"events,omitempty"`
	MRResults  []ScheduledMRResult `json:"mr_results,omitempty"`
	Summary    SchedulerData       `json:"summary"`
	Error      string              `json:"error,omitempty"`
}

type ScheduledMRResult struct {
	JobID         string                `json:"job_id"`
	ProjectID     string                `json:"project_id,omitempty"`
	MergeRequests []MergeRequestSummary `json:"merge_requests"`
	Error         string                `json:"error,omitempty"`
}

func schedulerTools() []MCPTool {
	return []MCPTool{
		{Name: "schedule_reminder", Description: "Создать отложенное или периодическое напоминание", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"},"delay_seconds":{"type":"integer"},"every_seconds":{"type":"integer"}},"required":["text","delay_seconds"],"additionalProperties":false}`)},
		{Name: "schedule_open_merge_requests", Description: "Регулярно получать и выводить открытые MR GitLab. Первый запрос через every_seconds секунд", InputSchema: json.RawMessage(`{"type":"object","properties":{"every_seconds":{"type":"integer","description":"Период от 10 секунд"},"project_id":{"type":"string","description":"Необязательно: проект group/project. Без проекта — мои проекты"}},"required":["every_seconds"],"additionalProperties":false}`)},
		{Name: "list_reminders", Description: "Список напоминаний и следующих запусков", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		{Name: "reminder_summary", Description: "Агрегированная сводка запусков и последние 100 событий", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		{Name: "cancel_reminder", Description: "Остановить напоминание по ID", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)},
		{Name: "cancel_all_reminders", Description: "Остановить все задания планировщика при сбросе агента", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
	}
}

func serveSchedulerMCP(in io.Reader, out io.Writer, s *Scheduler, now func() time.Time, gitlab ...*GitLabAPI) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(out)
	initialized := false
	var api *GitLabAPI
	if len(gitlab) > 0 {
		api = gitlab[0]
	}
	for scanner.Scan() {
		var request mcpMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return err
		}
		if request.JSONRPC != "2.0" {
			return fmt.Errorf("неподдерживаемый JSON-RPC")
		}
		if request.ID == nil {
			if request.Method == "notifications/initialized" {
				initialized = true
			}
			continue
		}
		response := mcpMessage{JSONRPC: "2.0", ID: request.ID}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "mrkScheduler", "version": "d18"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			if !initialized {
				response.Error = &mcpError{Code: -32000, Message: "клиент не инициализирован"}
			} else {
				result = map[string]any{"tools": schedulerTools()}
			}
		case "tools/call":
			if !initialized {
				response.Error = &mcpError{Code: -32000, Message: "клиент не инициализирован"}
				break
			}
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(request.Params, &params) != nil {
				response.Error = &mcpError{Code: -32602, Message: "неверные параметры"}
				break
			}
			var args struct {
				Text         string `json:"text"`
				DelaySeconds int64  `json:"delay_seconds"`
				EverySeconds int64  `json:"every_seconds"`
				ProjectID    string `json:"project_id"`
				ID           string `json:"id"`
			}
			if len(params.Arguments) > 0 && json.Unmarshal(params.Arguments, &args) != nil {
				response.Error = &mcpError{Code: -32602, Message: "неверные аргументы"}
				break
			}
			var value any
			var err error
			switch params.Name {
			case "schedule_reminder":
				value, err = s.Add(args.Text, args.DelaySeconds, args.EverySeconds, now())
			case "schedule_open_merge_requests":
				if api == nil || api.token == "" {
					err = fmt.Errorf("для регулярного сбора MR задайте GITLAB_API_TOKEN")
				} else {
					value, err = s.AddMergeRequests(args.EverySeconds, args.ProjectID, now())
				}
			case "list_reminders":
				value = map[string]any{"jobs": s.Summary().Jobs}
			case "reminder_summary":
				value = s.Summary()
			case "cancel_reminder":
				err = s.Cancel(args.ID)
				value = map[string]string{"cancelled": args.ID}
			case "cancel_all_reminders":
				var generation int64
				generation, err = s.CancelAll()
				value = map[string]int64{"generation": generation}
			default:
				response.Error = &mcpError{Code: -32602, Message: "неизвестный инструмент"}
			}
			if response.Error == nil {
				if err != nil {
					result = map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": err.Error()}}}
				} else {
					data, _ := json.Marshal(value)
					result = map[string]any{"content": []map[string]string{{"type": "text", "text": string(data)}}}
				}
			}
		default:
			response.Error = &mcpError{Code: -32601, Message: "метод не найден"}
		}
		if response.Error == nil {
			response.Result, _ = json.Marshal(result)
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func runSchedulerServer(path string) error {
	s, err := NewScheduler(path)
	if err != nil {
		return err
	}
	api, err := NewGitLabAPI(envOr("GITLAB_API_URL", defaultGitLabAPIURL), os.Getenv("GITLAB_API_TOKEN"), &http.Client{Timeout: 15 * time.Second})
	if err != nil {
		return err
	}
	done := make(chan struct{})
	updates := json.NewEncoder(os.Stderr)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				update, fired, err := runScheduledTick(context.Background(), s, api, time.Now())
				if err != nil {
					_ = updates.Encode(SchedulerUpdate{Generation: s.Summary().Generation, Error: "не удалось сохранить расписание"})
					continue
				}
				if fired {
					_ = updates.Encode(update)
				}
			case <-done:
				return
			}
		}
	}()
	err = serveSchedulerMCP(os.Stdin, os.Stdout, s, time.Now, api)
	close(done)
	return err
}

func runScheduledTick(ctx context.Context, s *Scheduler, api *GitLabAPI, now time.Time) (SchedulerUpdate, bool, error) {
	events, snapshot, err := s.tickWithSnapshot(now)
	if err != nil || len(events) == 0 {
		return SchedulerUpdate{}, false, err
	}
	update := SchedulerUpdate{Generation: snapshot.Generation, Events: events}
	jobs := snapshot.Jobs
	for _, event := range events {
		if s.Summary().Generation != update.Generation {
			break
		}
		for _, job := range jobs {
			if job.ID != event.JobID || job.Kind != mergeRequestsJobKind {
				continue
			}
			result := ScheduledMRResult{JobID: job.ID, ProjectID: job.ProjectID}
			if api == nil || api.token == "" {
				result.Error = "не задан GITLAB_API_TOKEN"
			} else {
				requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				s.setRunningCancel(cancel)
				if s.Summary().Generation != update.Generation {
					cancel()
					s.clearRunningCancel()
					break
				}
				mrs, requestErr := api.openMergeRequests(requestCtx, job.ProjectID, nil)
				cancel()
				s.clearRunningCancel()
				if requestErr != nil {
					result.Error = requestErr.Error()
				} else {
					result.MergeRequests = summarizeMergeRequests(mrs)
				}
			}
			update.MRResults = append(update.MRResults, result)
			break
		}
	}
	update.Summary = s.Summary()
	if update.Summary.Generation != update.Generation {
		return SchedulerUpdate{}, false, nil
	}
	return update, true, nil
}

func activeJobs(jobs []ScheduledJob) int {
	n := 0
	for _, job := range jobs {
		if job.Active {
			n++
		}
	}
	return n
}
