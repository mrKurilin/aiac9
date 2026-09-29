package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const githubCorpusTargetWords = 10000
const githubMaxRepositories = 40

var errUnsupportedREADME = errors.New("README в неподдерживаемом формате")
var errGitHubResponseTooLarge = errors.New("ответ GitHub API превышает лимит")

var githubNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
var githubCredentialPattern = regexp.MustCompile(`(?i)(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9]{32,}|-----BEGIN [A-Z ]*PRIVATE KEY-----)`)
var githubInternalHostPattern = regexp.MustCompile(`(?i)\b[\w-]+(?:\.[\w-]+)*\.(?:local|internal|intranet|corp|lan|svc|priv|vpn)\b`)

type githubRepository struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	Archived bool   `json:"archived"`
	Stars    int    `json:"stargazers_count"`
}

type githubIndexedProject struct {
	Source string `json:"source"`
	Stars  int    `json:"stars"`
}

type githubReadmeService struct {
	baseURL string
	client  *http.Client
	bearer  string
}

type githubIndexResult struct {
	Topic                 string                 `json:"topic"`
	RequestedRepositories int                    `json:"requested_repositories,omitempty"`
	Repositories          int                    `json:"repositories"`
	Words                 int                    `json:"words"`
	TargetMet             bool                   `json:"target_met"`
	FixedChunks           int                    `json:"fixed_chunks"`
	StructureChunks       int                    `json:"structure_chunks"`
	Sources               []string               `json:"sources"`
	Projects              []githubIndexedProject `json:"projects"`
}

func newGitHubReadmeService(token string) *githubReadmeService {
	return &githubReadmeService{
		baseURL: "https://api.github.com",
		bearer:  strings.TrimSpace(token),
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("перенаправление GitHub API отклонено")
		}},
	}
}

func validGitHubRepo(fullName string) bool {
	parts := strings.Split(fullName, "/")
	return len(parts) == 2 && githubNamePattern.MatchString(parts[0]) && githubNamePattern.MatchString(parts[1]) && parts[0] != "." && parts[1] != "."
}

func validateGitHubTopic(topic string) error {
	if len([]rune(topic)) < 2 || len([]rune(topic)) > 150 || strings.ContainsAny(topic, "\r\n") || strings.Contains(topic, "://") || strings.Contains(topic, "@") {
		return fmt.Errorf("тема должна быть короткой поисковой фразой без URL и адресов")
	}
	if githubCredentialPattern.MatchString(topic) || githubInternalHostPattern.MatchString(topic) {
		return fmt.Errorf("поисковая фраза содержит закрытые данные")
	}
	for _, field := range strings.Fields(topic) {
		if address := net.ParseIP(strings.Trim(field, ",;()[]")); address != nil && (address.IsPrivate() || address.IsLinkLocalUnicast()) {
			return fmt.Errorf("поисковая фраза содержит закрытый адрес")
		}
	}
	return nil
}

func (s *githubReadmeService) get(ctx context.Context, path, accept string, maxBytes int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "mrkai-d22")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if s.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+s.bearer)
	}
	response, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, fmt.Errorf("GitHub API: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if int64(len(data)) > maxBytes {
		return nil, response.StatusCode, errGitHubResponseTooLarge
	}
	return data, response.StatusCode, nil
}

func (s *githubReadmeService) search(ctx context.Context, topic string) ([]githubRepository, error) {
	query := url.Values{"q": {topic + " in:name,description,readme"}, "per_page": {"50"}, "sort": {"stars"}, "order": {"desc"}}
	data, _, err := s.get(ctx, "/search/repositories?"+query.Encode(), "application/vnd.github+json", 2<<20)
	if err != nil {
		return nil, err
	}
	var response struct {
		Items []githubRepository `json:"items"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("неверный ответ поиска GitHub: %w", err)
	}
	sort.SliceStable(response.Items, func(i, j int) bool {
		if response.Items[i].Stars == response.Items[j].Stars {
			return response.Items[i].FullName < response.Items[j].FullName
		}
		return response.Items[i].Stars > response.Items[j].Stars
	})
	return response.Items, nil
}

func (s *githubReadmeService) readme(ctx context.Context, fullName string) (string, int, error) {
	if !validGitHubRepo(fullName) {
		return "", 0, fmt.Errorf("неверное имя репозитория")
	}
	data, status, err := s.get(ctx, "/repos/"+fullName+"/readme", "application/vnd.github.raw+json", 1<<20)
	if err != nil {
		return "", status, err
	}
	body, err := decodeGitHubReadme(data)
	return body, status, err
}

func decodeGitHubReadme(data []byte) (string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return "", errUnsupportedREADME
	}
	if !json.Valid(trimmed) {
		if len(data) > maxDocumentBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return "", errUnsupportedREADME
		}
		return string(data), nil
	}
	var file struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(data, &file); err != nil || file.Encoding != "base64" || file.Content == "" {
		return "", errUnsupportedREADME
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil || len(decoded) > maxDocumentBytes || !utf8.Valid(decoded) || bytes.IndexByte(decoded, 0) >= 0 {
		return "", errUnsupportedREADME
	}
	return string(decoded), nil
}

func (a *Agent) IndexGitHubReadmes(ctx context.Context, topic string, progress func(string), requested ...int) (githubIndexResult, error) {
	topic = strings.TrimSpace(topic)
	if err := validateGitHubTopic(topic); err != nil {
		return githubIndexResult{}, err
	}
	limit := githubMaxRepositories
	exactCount := false
	if len(requested) > 0 && requested[0] != 0 {
		if len(requested) != 1 || requested[0] < 1 || requested[0] > githubMaxRepositories {
			return githubIndexResult{}, fmt.Errorf("число репозиториев должно быть от 1 до %d", githubMaxRepositories)
		}
		limit = requested[0]
		exactCount = true
	}
	if a.githubReadmes == nil {
		return githubIndexResult{}, fmt.Errorf("GitHub README не настроен")
	}
	if progress != nil {
		progress("GitHub: поиск публичных репозиториев")
	}
	repos, err := a.githubReadmes.search(ctx, topic)
	if err != nil {
		return githubIndexResult{}, err
	}
	if progress != nil {
		progress(fmt.Sprintf("GitHub: поиск завершён, кандидатов: %d", len(repos)))
	}
	docs := make([]sourceDocument, 0, limit)
	sources := make([]string, 0, limit)
	projects := make([]githubIndexedProject, 0, limit)
	words := 0
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			return githubIndexResult{}, err
		}
		if len(docs) >= limit || (!exactCount && words >= githubCorpusTargetWords) {
			break
		}
		if repo.Private || repo.Archived || !validGitHubRepo(repo.FullName) {
			continue
		}
		if progress != nil {
			progress(fmt.Sprintf("GitHub: README %d/%d — %s", len(docs)+1, limit, repo.FullName))
		}
		body, status, err := a.githubReadmes.readme(ctx, repo.FullName)
		if err != nil {
			if status == http.StatusNotFound {
				if progress != nil {
					progress("GitHub: HTTP 404, README отсутствует")
				}
				continue
			}
			if errors.Is(err, errUnsupportedREADME) || errors.Is(err, errGitHubResponseTooLarge) {
				if progress != nil {
					progress("GitHub: HTTP 200, README пропущен (неподдерживаемый формат или размер)")
				}
				continue
			}
			return githubIndexResult{}, err
		}
		if progress != nil {
			progress("GitHub: HTTP 200, README получен")
		}
		if strings.TrimSpace(body) == "" || strings.ContainsRune(body, 0) || githubCredentialPattern.MatchString(body) {
			if progress != nil {
				progress("GitHub: README пропущен (пустой, бинарный или содержит закрытые данные)")
			}
			continue
		}
		ownerRepo := strings.Split(repo.FullName, "/")
		path := filepath.Join(a.readmesDir, ownerRepo[0], ownerRepo[1], "README.md")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return githubIndexResult{}, err
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			return githubIndexResult{}, err
		}
		source := "https://github.com/" + repo.FullName
		docs = append(docs, sourceDocument{path: repo.FullName + "/README.md", source: source, text: body})
		sources = append(sources, source)
		projects = append(projects, githubIndexedProject{Source: source, Stars: repo.Stars})
		words += len(splitWords(body))
		if progress != nil {
			progress(fmt.Sprintf("GitHub: сохранено README: %d, слов: %d", len(docs), words))
		}
	}
	if len(docs) == 0 {
		return githubIndexResult{}, fmt.Errorf("по теме не удалось скачать ни одного README")
	}
	indexes, err := a.buildIndexesFromDocuments(ctx, docs, progress)
	if err != nil {
		return githubIndexResult{}, err
	}
	result := githubIndexResult{Topic: topic, Repositories: len(docs), Words: words,
		TargetMet: words >= githubCorpusTargetWords, FixedChunks: len(indexes[0].Chunks),
		StructureChunks: len(indexes[1].Chunks), Sources: sources, Projects: projects}
	if exactCount {
		result.RequestedRepositories = limit
	}
	return result, nil
}
