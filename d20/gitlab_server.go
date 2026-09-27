package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultGitLabAPIURL = "https://gitlab.com/api/v4"

type MergeRequest struct {
	ProjectID int    `json:"project_id"`
	IID       int    `json:"iid"`
	Title     string `json:"title"`
	Author    struct {
		Username string `json:"username"`
	} `json:"author"`
	WebURL              string `json:"web_url"`
	Description         string `json:"description"`
	Draft               bool   `json:"draft"`
	SourceBranch        string `json:"source_branch"`
	TargetBranch        string `json:"target_branch"`
	HasConflicts        bool   `json:"has_conflicts"`
	DetailedMergeStatus string `json:"detailed_merge_status"`
	UserNotesCount      int    `json:"user_notes_count"`
	UpdatedAt           string `json:"updated_at"`
	References          struct {
		Full string `json:"full"`
	} `json:"references"`
}

type GitLabAPI struct {
	baseURL    string
	authHeader string
	http       *http.Client
}

func NewGitLabAPI(baseURL, token string, client *http.Client) (*GitLabAPI, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("неверный адрес GitLab API")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")) {
		return nil, fmt.Errorf("GitLab API требует HTTPS")
	}
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	authHeader := ""
	if token != "" {
		authHeader = "Bearer " + token
	}
	return &GitLabAPI{baseURL: strings.TrimSuffix(baseURL, "/"), authHeader: authHeader, http: &copyClient}, nil
}

func (api *GitLabAPI) OpenMergeRequests(ctx context.Context, projectID string) ([]MergeRequest, error) {
	return api.openMergeRequests(ctx, projectID, nil)
}

func (api *GitLabAPI) openMergeRequests(ctx context.Context, projectID string, progress func(string)) ([]MergeRequest, error) {
	log := func(message string, args ...any) {
		if progress != nil {
			progress(fmt.Sprintf(message, args...))
		}
	}
	projectID = strings.TrimSpace(projectID)
	log("Проверяю параметры и авторизацию")
	if strings.ContainsAny(projectID, "?#") {
		return nil, fmt.Errorf("project_id должен быть числом или путём group/project")
	}
	if api.authHeader == "" {
		return nil, fmt.Errorf("для GitLab API задайте GITLAB_API_TOKEN")
	}
	started := time.Now()
	mrQuery := url.Values{"state": {"opened"}, "scope": {"all"}}
	if projectID != "" {
		items, err := fetchPages[MergeRequest](ctx, api, "/projects/"+url.PathEscape(projectID)+"/merge_requests", mrQuery, "открытых MR проекта", "MR", log)
		if err == nil {
			log("Готово за %s", time.Since(started).Round(time.Millisecond))
		}
		return items, err
	}
	projects, err := fetchPages[gitLabProject](ctx, api, "/projects", url.Values{"membership": {"true"}, "archived": {"false"}, "simple": {"true"}}, "моих проектов", "проектов", log)
	if err != nil {
		return nil, err
	}
	items := make([]MergeRequest, 0)
	for index, project := range projects {
		log("Проект %d/%d: запрашиваю открытые MR", index+1, len(projects))
		batch, err := fetchPages[MergeRequest](ctx, api, "/projects/"+strconv.Itoa(project.ID)+"/merge_requests", mrQuery, "открытых MR проекта", "MR", log)
		if err != nil {
			return nil, err
		}
		items = append(items, batch...)
	}
	log("Готово за %s: проектов — %d, открытых MR — %d", time.Since(started).Round(time.Millisecond), len(projects), len(items))
	return items, nil
}

type gitLabProject struct {
	ID                int    `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
}

func fetchPages[T any](ctx context.Context, api *GitLabAPI, path string, filters url.Values, label, unit string, log func(string, ...any)) ([]T, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	items := make([]T, 0)
	page := "1"
	started := time.Now()
	for count := 0; count < 100; count++ {
		endpoint := api.baseURL + path
		query := url.Values{"per_page": {"100"}, "page": {page}}
		for key, values := range filters {
			query[key] = values
		}
		log("Запрашиваю страницу %s %s", page, label)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("создать запрос GitLab API: %w", err)
		}
		req.Header.Set("Authorization", api.authHeader)
		resp, err := api.http.Do(req)
		if err != nil {
			log("Сетевой запрос страницы %s завершился ошибкой через %s", page, time.Since(started).Round(time.Millisecond))
			if errors.Is(err, context.Canceled) {
				return nil, fmt.Errorf("запрос GitLab API отменён")
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, fmt.Errorf("GitLab API: истёк таймаут запроса")
			}
			return nil, fmt.Errorf("запрос GitLab API не выполнен")
		}
		log("Страница %s: HTTP %d", page, resp.StatusCode)
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, fmt.Errorf("GitLab API: HTTP 401 — токен недействителен, истёк или выпущен для другого GitLab")
			}
			return nil, fmt.Errorf("GitLab API: HTTP %d", resp.StatusCode)
		}
		var batch []T
		err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&batch)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("неверный ответ GitLab API: %w", err)
		}
		items = append(items, batch...)
		log("Страница %s: получено %s — %d, всего — %d", page, unit, len(batch), len(items))
		next := resp.Header.Get("X-Next-Page")
		if next == "" {
			return items, nil
		}
		nextNumber, err := strconv.Atoi(next)
		if err != nil || nextNumber < 1 || next == page {
			return nil, fmt.Errorf("неверная пагинация GitLab API")
		}
		page = next
	}
	return nil, fmt.Errorf("список GitLab %s превышает 100 страниц", unit)
}

func gitLabMRSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"project_id":{"type":"string","description":"Необязательно: числовой ID или путь проекта group/project. Без параметра — открытые MR во всех проектах, где я участник"}},"additionalProperties":false}`)
}

func serveGitLabMCP(in io.Reader, out io.Writer, api *GitLabAPI) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(out)
	var writeMu sync.Mutex
	write := func(message mcpMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return encoder.Encode(message)
	}
	log := func(message string) {
		params, _ := json.Marshal(map[string]string{"level": "info", "logger": "gitlab", "data": message})
		_ = write(mcpMessage{JSONRPC: "2.0", Method: "notifications/message", Params: params})
	}
	initialized, initializeRequested := false, false
	var callsMu sync.Mutex
	calls := map[string]context.CancelFunc{}
	var callsWG sync.WaitGroup
	for scanner.Scan() {
		var request mcpMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return err
		}
		if request.JSONRPC != "2.0" {
			return fmt.Errorf("неподдерживаемый JSON-RPC")
		}
		if request.ID == nil {
			if request.Method == "notifications/initialized" && initializeRequested {
				initialized = true
			} else if request.Method == "notifications/cancelled" {
				var params struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				if json.Unmarshal(request.Params, &params) == nil {
					callsMu.Lock()
					cancel := calls[string(params.RequestID)]
					callsMu.Unlock()
					if cancel != nil {
						log("Получена отмена текущего вызова")
						cancel()
					}
				}
			}
			continue
		}
		response := mcpMessage{JSONRPC: "2.0", ID: request.ID}
		var result any
		switch request.Method {
		case "initialize":
			initializeRequested = true
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "mrkGitlab", "version": "d20"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			if !initialized {
				response.Error = &mcpError{Code: -32000, Message: "клиент ещё не инициализирован"}
			} else {
				result = map[string]any{"tools": []MCPTool{{Name: "list_open_merge_requests", Description: "Сводка по моим открытым MR GitLab со ссылками, с фильтром по проекту", InputSchema: gitLabMRSchema()}}}
			}
		case "tools/call":
			if !initialized {
				response.Error = &mcpError{Code: -32000, Message: "клиент ещё не инициализирован"}
				break
			}
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.Name != "list_open_merge_requests" {
				response.Error = &mcpError{Code: -32602, Message: "неизвестный инструмент"}
				break
			}
			var args struct {
				ProjectID string `json:"project_id"`
			}
			if json.Unmarshal(params.Arguments, &args) != nil {
				response.Error = &mcpError{Code: -32602, Message: "project_id должен быть строкой"}
				break
			}
			ctx, cancel := context.WithCancel(context.Background())
			key := string(request.ID)
			callsMu.Lock()
			calls[key] = cancel
			callsMu.Unlock()
			callsWG.Add(1)
			go func(response mcpMessage, projectID, key string) {
				defer callsWG.Done()
				defer cancel()
				defer func() {
					callsMu.Lock()
					delete(calls, key)
					callsMu.Unlock()
				}()
				log("Вызов list_open_merge_requests принят сервером")
				mrs, err := api.openMergeRequests(ctx, projectID, log)
				if err != nil {
					result := map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": err.Error()}}}
					response.Result, _ = json.Marshal(result)
					_ = write(response)
					return
				}
				data, _ := json.Marshal(map[string]any{"project_id": projectID, "merge_requests": summarizeMergeRequests(mrs)})
				response.Result, _ = json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": string(data)}}})
				_ = write(response)
			}(response, args.ProjectID, key)
			continue
		default:
			response.Error = &mcpError{Code: -32601, Message: "метод не найден"}
		}
		if response.Error == nil {
			response.Result, _ = json.Marshal(result)
		}
		if err := write(response); err != nil {
			return err
		}
	}
	callsMu.Lock()
	for _, cancel := range calls {
		cancel()
	}
	callsMu.Unlock()
	callsWG.Wait()
	return scanner.Err()
}
