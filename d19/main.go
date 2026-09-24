package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func validateLaunchArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("запускайте mrkai без параметров")
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--pipeline-mcp-server" {
		if err := servePipelineMCP(os.Stdin, os.Stdout, envOr("KNOWLEDGE_DIR", "./knowledge"), filepath.Join(envOr("DATA_DIR", "./data"), "reports")); err != nil {
			fmt.Fprintln(os.Stderr, "Pipeline MCP:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--scheduler-mcp-server" {
		if err := runSchedulerServer(filepath.Join(envOr("DATA_DIR", "./data"), "reminders.json")); err != nil {
			fmt.Fprintln(os.Stderr, "Scheduler MCP:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--gitlab-mcp-server" {
		api, err := NewGitLabAPI(envOr("GITLAB_API_URL", defaultGitLabAPIURL), strings.TrimSpace(os.Getenv("GITLAB_API_TOKEN")), &http.Client{Timeout: 15 * time.Second})
		if err == nil {
			err = serveGitLabMCP(os.Stdin, os.Stdout, api)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "GitLab MCP:", err)
			os.Exit(1)
		}
		return
	}
	if err := validateLaunchArgs(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(2)
	}
	apiKey := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	dataDir := envOr("DATA_DIR", "./data")
	store := NewJSONStateStore(filepath.Join(dataDir, "task-state.json"))
	invariantStore := NewJSONInvariantStore(envOr("INVARIANTS_FILE", filepath.Join(dataDir, "invariants.json")))
	client := NewDeepSeekClient(
		apiKey,
		envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com/chat/completions"),
		envOr("DEEPSEEK_MODEL", "deepseek-chat"),
		&http.Client{Timeout: 90 * time.Second},
	)
	agent := NewAgent(client, envOr("AGENT_SYSTEM_PROMPT", "Ты практичный ИИ-ассистент. Отвечай на языке пользователя."), store, invariantStore)
	if agent.loadErr != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", agent.loadErr)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	gitlab, err := StartGitLabMCP(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка GitLab MCP:", err)
		os.Exit(1)
	}
	defer gitlab.Close()
	agent.gitlab = gitlab
	scheduler, err := StartSchedulerMCP(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка Scheduler MCP:", err)
		os.Exit(1)
	}
	defer scheduler.Close()
	agent.scheduler = scheduler
	pipeline, err := StartPipelineMCP(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка Pipeline MCP:", err)
		os.Exit(1)
	}
	defer pipeline.Close()
	agent.pipeline = pipeline
	if err := runTerminal(ctx, agent, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
