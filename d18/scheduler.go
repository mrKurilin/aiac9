package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type ScheduledJob struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind,omitempty"`
	Text         string    `json:"text"`
	ProjectID    string    `json:"project_id,omitempty"`
	NextAt       time.Time `json:"next_at"`
	EverySeconds int64     `json:"every_seconds,omitempty"`
	Runs         int       `json:"runs"`
	Active       bool      `json:"active"`
}

const mergeRequestsJobKind = "open_merge_requests"

var projectIDPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

type ScheduledEvent struct {
	JobID string    `json:"job_id"`
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
}

type SchedulerData struct {
	Jobs       []ScheduledJob   `json:"jobs"`
	Events     []ScheduledEvent `json:"events"`
	TotalRuns  int              `json:"total_runs"`
	Generation int64            `json:"generation,omitempty"`
}

type Scheduler struct {
	mu            sync.Mutex
	path          string
	data          SchedulerData
	runningCancel context.CancelFunc
}

func NewScheduler(path string) (*Scheduler, error) {
	s := &Scheduler{path: path}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("прочитать расписание: %w", err)
	}
	if len(raw) > 0 && json.Unmarshal(raw, &s.data) != nil {
		return nil, fmt.Errorf("повреждён файл расписания")
	}
	return s, nil
}

func (s *Scheduler) save(next SchedulerData) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".schedule-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err = temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, s.path); err != nil {
		return err
	}
	s.data = next
	return nil
}

func (s *Scheduler) Add(text string, delaySeconds, everySeconds int64, now time.Time) (ScheduledJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > 500 {
		return ScheduledJob{}, fmt.Errorf("текст должен содержать от 1 до 500 символов")
	}
	if delaySeconds < 1 || delaySeconds > 365*24*3600 {
		return ScheduledJob{}, fmt.Errorf("delay_seconds должен быть от 1 до 31536000")
	}
	if everySeconds < 0 || (everySeconds > 0 && (everySeconds < 10 || everySeconds > 365*24*3600)) {
		return ScheduledJob{}, fmt.Errorf("every_seconds должен быть 0 или от 10 до 31536000")
	}
	next := s.data
	job := ScheduledJob{ID: fmt.Sprintf("R%d", len(next.Jobs)+1), Text: text, NextAt: now.Add(time.Duration(delaySeconds) * time.Second), EverySeconds: everySeconds, Active: true}
	next.Jobs = append(append([]ScheduledJob(nil), next.Jobs...), job)
	if err := s.save(next); err != nil {
		return ScheduledJob{}, err
	}
	return job, nil
}

func (s *Scheduler) AddMergeRequests(everySeconds int64, projectID string, now time.Time) (ScheduledJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if everySeconds < 10 || everySeconds > 365*24*3600 {
		return ScheduledJob{}, fmt.Errorf("every_seconds должен быть от 10 до 31536000")
	}
	projectID = strings.TrimSpace(projectID)
	if len(projectID) > 200 || (projectID != "" && !projectIDPattern.MatchString(projectID)) {
		return ScheduledJob{}, fmt.Errorf("project_id должен быть числом или путём group/project")
	}
	for _, job := range s.data.Jobs {
		if job.Active && job.Kind == mergeRequestsJobKind && job.EverySeconds == everySeconds && job.ProjectID == projectID {
			return job, nil
		}
	}
	next := s.data
	job := ScheduledJob{
		ID: fmt.Sprintf("R%d", len(next.Jobs)+1), Kind: mergeRequestsJobKind,
		Text: "Открытые MR", ProjectID: projectID,
		NextAt: now.Add(time.Duration(everySeconds) * time.Second), EverySeconds: everySeconds, Active: true,
	}
	next.Jobs = append(append([]ScheduledJob(nil), next.Jobs...), job)
	if err := s.save(next); err != nil {
		return ScheduledJob{}, err
	}
	return job, nil
}

func (s *Scheduler) Cancel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.data
	next.Jobs = append([]ScheduledJob(nil), next.Jobs...)
	for i := range next.Jobs {
		if next.Jobs[i].ID == id && next.Jobs[i].Active {
			next.Jobs[i].Active = false
			return s.save(next)
		}
	}
	return fmt.Errorf("активное напоминание не найдено")
}

func (s *Scheduler) CancelAll() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningCancel != nil {
		s.runningCancel()
	}
	next := s.data
	next.Jobs = append([]ScheduledJob(nil), next.Jobs...)
	for index := range next.Jobs {
		next.Jobs[index].Active = false
	}
	next.Generation++
	if err := s.save(next); err != nil {
		return 0, err
	}
	return next.Generation, nil
}

func (s *Scheduler) setRunningCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runningCancel = cancel
}

func (s *Scheduler) clearRunningCancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runningCancel = nil
}

func (s *Scheduler) Tick(now time.Time) ([]ScheduledEvent, error) {
	events, _, err := s.tickWithSnapshot(now)
	return events, err
}

// tickWithSnapshot returns the events and their generation under the same lock.
func (s *Scheduler) tickWithSnapshot(now time.Time) ([]ScheduledEvent, SchedulerData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.data
	next.Jobs = append([]ScheduledJob(nil), next.Jobs...)
	next.Events = append([]ScheduledEvent(nil), next.Events...)
	var fired []ScheduledEvent
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if !job.Active || now.Before(job.NextAt) {
			continue
		}
		event := ScheduledEvent{JobID: job.ID, Text: job.Text, At: now}
		fired = append(fired, event)
		next.Events = append(next.Events, event)
		next.TotalRuns++
		job.Runs++
		if job.EverySeconds == 0 {
			job.Active = false
		} else {
			job.NextAt = now.Add(time.Duration(job.EverySeconds) * time.Second)
		}
	}
	if len(fired) == 0 {
		return nil, SchedulerData{}, nil
	}
	if len(next.Events) > 100 {
		next.Events = next.Events[len(next.Events)-100:]
	}
	if err := s.save(next); err != nil {
		return nil, SchedulerData{}, err
	}
	return fired, s.snapshotLocked(), nil
}

func (s *Scheduler) Summary() SchedulerData {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Scheduler) snapshotLocked() SchedulerData {
	result := s.data
	result.Jobs = append([]ScheduledJob(nil), result.Jobs...)
	result.Events = append([]ScheduledEvent(nil), result.Events...)
	return result
}
