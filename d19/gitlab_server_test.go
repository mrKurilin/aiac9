package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func connectedGitLabMCP(t *testing.T, api *GitLabAPI) *MCPClient {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	done := make(chan error, 1)
	go func() {
		defer serverOut.Close()
		done <- serveGitLabMCP(serverIn, serverOut, api)
	}()
	client := &MCPClient{stdin: clientOut, stdout: bufio.NewReader(clientIn)}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := client.call("initialize", map[string]any{}, &initialized); err != nil {
		t.Fatal(err)
	}
	if err := client.notify("notifications/initialized"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clientOut.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return client
}

func TestMCPGitLabMRSShowsOnlyResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/projects" {
			fmt.Fprint(w, `[{"id":42,"path_with_namespace":"group/project"}]`)
			return
		}
		fmt.Fprint(w, `[{"project_id":42,"iid":3,"title":"Open","author":{"username":"alice"},"web_url":"https://gitlab.example/group/project/-/merge_requests/3","references":{"full":"group/project!3"},"source_branch":"feature","target_branch":"main","detailed_merge_status":"not_approved","user_notes_count":2,"updated_at":"2026-09-20T10:00:00Z","description":"## Adds login\nmore"}]`)
	}))
	defer server.Close()
	api, err := NewGitLabAPI(server.URL+"/api/v4", "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(&captureClient{}, "", nil)
	agent.gitlab = connectedGitLabMCP(t, api)
	var out bytes.Buffer
	if handled, exit := runCommand(agent, "/mcp mrkgitlab mrs", &out); !handled || exit {
		t.Fatal("command not handled")
	}
	shown := out.String()
	if strings.Contains(shown, "ответ получен") || strings.Contains(shown, "↳") || strings.Contains(shown, "Запрашиваю страницу") {
		t.Fatalf("diagnostic logs leaked into output: %s", shown)
	}
	if !strings.Contains(shown, "готов к ревью; feature → main; нужны аппрувы; комментариев: 2; обновлён 2026-09-20. Adds login") {
		t.Fatalf("summary missing: %s", shown)
	}
	out.Reset()
	if handled, exit := runCommand(agent, "/mcp mrs", &out); !handled || exit || !strings.Contains(out.String(), "использование") || strings.Contains(out.String(), "merge_requests/3") {
		t.Fatalf("short command must be rejected: %s", out.String())
	}
}

func TestGitLabOpenMergeRequestsAndMCP(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("PRIVATE-TOKEN") != "" || r.URL.EscapedPath() != "/api/v4/projects/group%2Fproject/merge_requests" || r.URL.Query().Get("state") != "opened" || r.URL.Query().Get("scope") != "all" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected request: path=%q query=%q", r.URL.EscapedPath(), r.URL.RawQuery)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"iid":1,"title":"First","author":{"username":"alice"}}]`)
		case "2":
			fmt.Fprint(w, `[{"iid":2,"title":"Second","author":{"username":"bob"}}]`)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer server.Close()
	api, err := NewGitLabAPI(server.URL+"/api/v4", "test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client := connectedGitLabMCP(t, api)
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "list_open_merge_requests" {
		t.Fatalf("tools=%+v err=%v", tools, err)
	}
	var logs []string
	result, err := client.CallJSONToolWithProgress(context.Background(), "list_open_merge_requests", json.RawMessage(`{"project_id":"group/project"}`), func(message string) {
		logs = append(logs, message)
	})
	if err != nil || !strings.Contains(result, `"iid":2`) || requests != 2 {
		t.Fatalf("result=%s requests=%d err=%v", result, requests, err)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"Вызов list_open_merge_requests принят", "Запрашиваю страницу 1", "HTTP 200", "получено MR — 1", "Запрашиваю страницу 2", "Готово"} {
		if !strings.Contains(joined, want) {
			t.Errorf("logs miss %q: %s", want, joined)
		}
	}
}

func TestGitLabRequiresTokenBeforeNetwork(t *testing.T) {
	api, err := NewGitLabAPI(defaultGitLabAPIURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.OpenMergeRequests(context.Background(), "group/project")
	if err == nil || !strings.Contains(err.Error(), "GITLAB_API_TOKEN") {
		t.Fatalf("error=%v", err)
	}
}

func TestGitLabListsCurrentUsersOpenMergeRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v4/projects" && r.URL.Query().Get("membership") == "true" && r.URL.Query().Get("archived") == "false":
			fmt.Fprint(w, `[{"id":42,"path_with_namespace":"group/project"}]`)
		case r.URL.Path == "/api/v4/projects/42/merge_requests" && r.URL.Query().Get("state") == "opened":
			fmt.Fprint(w, `[{"project_id":42,"iid":3,"title":"Open","author":{"username":"alice"}}]`)
		default:
			t.Errorf("unexpected request: %s", r.URL.String())
		}
	}))
	defer server.Close()
	api, err := NewGitLabAPI(server.URL+"/api/v4", "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	mrs, err := api.OpenMergeRequests(context.Background(), "")
	if err != nil || len(mrs) != 1 || mrs[0].ProjectID != 42 {
		t.Fatalf("mrs=%v err=%v", mrs, err)
	}
}

func TestGitLabRejectsInsecureURLAndHidesErrorBody(t *testing.T) {
	if _, err := NewGitLabAPI("http://gitlab.com/api/v4", "test", nil); err == nil {
		t.Fatal("insecure URL accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "sensitive upstream error")
	}))
	defer server.Close()
	api, err := NewGitLabAPI(server.URL, "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.OpenMergeRequests(context.Background(), "project")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("error=%v", err)
	}
}

func TestGitLabExplainsUnauthorizedToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "token details must stay hidden")
	}))
	defer server.Close()
	api, err := NewGitLabAPI(server.URL, "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.OpenMergeRequests(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "недействителен") || !strings.Contains(err.Error(), "другого GitLab") || strings.Contains(err.Error(), "token details") {
		t.Fatalf("error=%v", err)
	}
}

func TestGitLabMCPCallCanBeCancelledAndUsedAgain(t *testing.T) {
	var requests atomic.Int32
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `[{"project_id":42,"iid":7,"title":"After cancel","author":{"username":"alice"}}]`)
	}))
	defer server.Close()
	api, err := NewGitLabAPI(server.URL+"/api/v4", "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client := connectedGitLabMCP(t, api)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.CallJSONToolWithProgress(ctx, "list_open_merge_requests", json.RawMessage(`{"project_id":""}`), nil)
		done <- err
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP cancellation timed out")
	}
	result, err := client.CallJSONTool(context.Background(), "list_open_merge_requests", json.RawMessage(`{"project_id":""}`))
	if err != nil || !strings.Contains(result, `"iid":7`) {
		t.Fatalf("client unusable after cancellation: result=%s err=%v", result, err)
	}
}
