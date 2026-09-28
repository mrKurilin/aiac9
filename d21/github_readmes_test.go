package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func githubFixture(t *testing.T) (*githubReadmeService, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/repositories":
			if !strings.Contains(r.URL.Query().Get("q"), "vector databases") {
				t.Errorf("unexpected search query: %q", r.URL.Query().Get("q"))
			}
			if r.URL.Query().Get("sort") != "stars" || r.URL.Query().Get("order") != "desc" {
				t.Errorf("search is not sorted by stars: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"items":[{"full_name":"bob/search","stargazers_count":4},{"full_name":"alice/vector-db","stargazers_count":100},{"full_name":"alice/unsupported","stargazers_count":50},{"full_name":"bob/missing","stargazers_count":2},{"full_name":"bad/../name","stargazers_count":1}]}`)
		case "/repos/alice/vector-db/readme", "/repos/bob/search/readme":
			body := "# Vector databases\n" + strings.Repeat("vector index search storage. ", 80)
			if strings.Contains(r.URL.Path, "bob") {
				body = "# Search\n" + strings.Repeat("embedding similarity retrieval. ", 80)
			}
			if strings.Contains(r.URL.Path, "bob") {
				w.Write([]byte(body)) // GitHub can return raw README text.
			} else {
				data, _ := json.Marshal(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(body))})
				w.Write(data)
			}
		case "/repos/alice/unsupported/readme":
			fmt.Fprint(w, `{"encoding":"none","content":""}`)
		case "/repos/bob/missing/readme":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return &githubReadmeService{baseURL: server.URL, client: server.Client()}, server
}

func TestGitHubReadmesDownloadAndIndex(t *testing.T) {
	service, server := githubFixture(t)
	defer server.Close()
	a := NewAgent(nil, "", nil)
	a.githubReadmes = service
	a.indexDir = filepath.Join(t.TempDir(), "index")
	a.readmesDir = filepath.Join(t.TempDir(), "readmes")
	var logs []string
	result, err := a.IndexGitHubReadmes(context.Background(), "vector databases", func(message string) { logs = append(logs, message) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Repositories != 2 || result.Words < 500 || result.TargetMet || result.FixedChunks == 0 || result.StructureChunks == 0 {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Projects) != 2 || result.Projects[0].Source != "https://github.com/alice/vector-db" || result.Projects[0].Stars != 100 || result.Projects[1].Stars != 4 {
		t.Fatalf("projects are not sorted: %+v", result.Projects)
	}
	if _, err := os.Stat(filepath.Join(a.readmesDir, "alice", "vector-db", "README.md")); err != nil {
		t.Fatal(err)
	}
	index, err := a.LoadIndex("structure")
	if err != nil || len(index.Chunks) == 0 || index.Chunks[0].Source != "https://github.com/alice/vector-db" || index.Chunks[0].Section != "Vector databases" {
		t.Fatalf("index=%+v err=%v", index.Chunks[0], err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "HTTP 404") || !strings.Contains(strings.Join(logs, "\n"), "неподдерживаемый формат") || !strings.Contains(strings.Join(logs, "\n"), "сохранён") {
		t.Fatalf("progress=%v", logs)
	}
	partial, err := a.IndexGitHubReadmes(context.Background(), "vector databases", nil, 35)
	if err != nil || partial.RequestedRepositories != 35 || partial.Repositories != 2 {
		t.Fatalf("partial corpus=%+v err=%v", partial, err)
	}
}

func TestModelCanChooseGitHubIndexTool(t *testing.T) {
	service, server := githubFixture(t)
	defer server.Close()
	model := &scriptedToolModel{replies: []Message{
		{ToolCalls: []ToolCall{{ID: "github-1", Type: "function", Function: ToolCallFunction{Name: "index_github_readmes", Arguments: `{"topic":"vector databases","repositories":2}`}}}},
		{Content: `{"answer":"README проиндексированы"}`},
	}}
	a := NewAgent(model, "", nil)
	a.githubReadmes = service
	a.indexDir = filepath.Join(t.TempDir(), "index")
	a.readmesDir = filepath.Join(t.TempDir(), "readmes")
	answer, err := a.completeTurn(context.Background(), []Message{{Role: "user", Content: "Найди GitHub README по теме vector databases и проиндексируй"}}, nil, nil, a.complete)
	if err != nil || !strings.Contains(answer, "проиндексированы") {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if len(model.tools) != 2 || len(model.tools[0]) != 1 || model.tools[0][0].Function.Name != "index_github_readmes" || !strings.Contains(string(model.tools[0][0].Function.Parameters), `"repositories"`) || len(model.seen[1]) != 3 || !strings.Contains(model.seen[1][2].Content, `"requested_repositories":2`) {
		t.Fatalf("tool exchange=%+v", model.seen)
	}
}

func TestRequestedRepositoryCountOverridesWordTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/repositories":
			fmt.Fprint(w, `{"items":[{"full_name":"alice/large","stargazers_count":10},{"full_name":"bob/second","stargazers_count":5}]}`)
		case "/repos/alice/large/readme", "/repos/bob/second/readme":
			body := "# README\n" + strings.Repeat("vector ", githubCorpusTargetWords+1)
			data, _ := json.Marshal(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(body))})
			w.Write(data)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	a := NewAgent(nil, "", nil)
	a.githubReadmes = &githubReadmeService{baseURL: server.URL, client: server.Client()}
	a.indexDir = filepath.Join(t.TempDir(), "index")
	a.readmesDir = filepath.Join(t.TempDir(), "readmes")
	result, err := a.IndexGitHubReadmes(context.Background(), "vector databases", nil, 2)
	if err != nil || result.Repositories != 2 || result.RequestedRepositories != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := a.IndexGitHubReadmes(context.Background(), "vector databases", nil, 41); err == nil {
		t.Fatal("accepted repository count above limit")
	}
}

func TestGitHubIndexTerminalCommand(t *testing.T) {
	service, server := githubFixture(t)
	defer server.Close()
	a := NewAgent(nil, "", nil)
	a.githubReadmes = service
	a.indexDir = filepath.Join(t.TempDir(), "index")
	a.readmesDir = filepath.Join(t.TempDir(), "readmes")
	var out bytes.Buffer
	handled, exit := runCommandContext(context.Background(), a, "/index github vector databases", &out)
	if !handled || exit || !strings.Contains(out.String(), "★ 100") || !strings.Contains(out.String(), "★ 4") || !strings.Contains(out.String(), "Корпус меньше") {
		t.Fatalf("terminal output=%q", out.String())
	}
}

func TestGitHubTopicAndRepositoryValidation(t *testing.T) {
	for _, topic := range []string{"", "https://example.com/path", "test@example.com"} {
		if validateGitHubTopic(topic) == nil {
			t.Fatalf("accepted topic %q", topic)
		}
	}
	for _, name := range []string{"alice/../repo", "alice/repo/extra", "bad?name/repo"} {
		if validGitHubRepo(name) {
			t.Fatalf("accepted repository %q", name)
		}
	}
}

func TestGitHubTokenIsSentOnlyWhenConfigured(t *testing.T) {
	for _, test := range []struct {
		name   string
		bearer string
		header string
	}{
		{name: "anonymous"},
		{name: "authenticated", bearer: "placeholder", header: "Bearer placeholder"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != test.header {
					t.Errorf("authorization header differs: present=%v", got != "")
				}
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			service := &githubReadmeService{baseURL: server.URL, client: server.Client(), bearer: test.bearer}
			_, status, err := service.get(context.Background(), "/check", "application/vnd.github+json", 1024)
			if err != nil || status != http.StatusOK {
				t.Fatalf("status=%d err=%v", status, err)
			}
		})
	}
}

func TestGitHubClientRejectsRedirectWithToken(t *testing.T) {
	service := newGitHubReadmeService("placeholder-github-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://example.com/")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	service.baseURL = server.URL
	_, _, err := service.get(context.Background(), "/redirect", "application/vnd.github+json", 1024)
	if err == nil || !strings.Contains(err.Error(), "перенаправление GitHub API отклонено") || strings.Contains(err.Error(), service.bearer) {
		t.Fatalf("unexpected redirect error: %v", err)
	}
}
