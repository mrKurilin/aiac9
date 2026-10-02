package main

import (
	"aiac9-terminal"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	indexFile  = "structure.json"
	vectorSize = 384
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ChatClient interface {
	Complete(context.Context, []Message) (string, error)
}

type DeepSeekClient struct {
	key, url, model string
	http            *http.Client
}

func (c *DeepSeekClient) Complete(ctx context.Context, messages []Message) (string, error) {
	if c.key == "" {
		return "", fmt.Errorf("задайте DEEPSEEK_API_KEY для ответа модели")
	}
	body, err := json.Marshal(map[string]any{"model": c.model, "messages": messages, "stream": false})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("сетевая ошибка модели")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("модель вернула HTTP %d", resp.StatusCode)
	}
	var data struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&data); err != nil {
		return "", fmt.Errorf("прочитать ответ модели: %w", err)
	}
	if len(data.Choices) == 0 || strings.TrimSpace(data.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("модель вернула пустой ответ")
	}
	return strings.TrimSpace(data.Choices[0].Message.Content), nil
}

type Chunk struct {
	Source, Section, Text string
	Vector                []float64
}
type Index struct {
	Version int     `json:"version"`
	Chunks  []Chunk `json:"chunks"`
}
type Hit struct {
	Chunk Chunk
	Score float64
}
type Agent struct {
	client   ChatClient
	indexDir string
	history  []Message
	task     TaskState
}

func NewAgent(client ChatClient, indexDir string) *Agent {
	return &Agent{client: client, indexDir: indexDir}
}

func tokens(text string) []string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	stopwords := map[string]bool{
		"a": true, "an": true, "and": true, "are": true, "at": true, "be": true,
		"by": true, "do": true, "for": true, "from": true, "how": true,
		"in": true, "is": true, "of": true, "on": true, "the": true,
		"to": true, "what": true, "when": true, "who": true, "with": true,
		"must": true, "our": true, "we": true, "my": true, "me": true,
		"this": true, "that": true, "these": true, "those": true,
		"it": true, "please": true, "about": true, "using": true,
		"draft": true, "write": true, "create": true, "make": true,
		"guide": true, "document": true, "plan": true, "summary": true,
		"summarize": true, "based": true, "can": true, "could": true,
		"would": true, "should": true, "tell": true, "now": true,
	}
	result := make([]string, 0, len(words))
	for _, word := range words {
		if stopwords[word] {
			continue
		}
		switch word {
		case "submitted", "submission", "submissions":
			word = "submit"
		case "expenses":
			word = "expense"
		case "days":
			word = "day"
		case "no":
			word = "not"
		}
		result = append(result, word)
	}
	return result
}
func vector(text string) []float64 {
	v := make([]float64, vectorSize)
	for _, word := range tokens(text) {
		if len([]rune(word)) > 1 {
			h := fnv.New64a()
			_, _ = h.Write([]byte(word))
			v[h.Sum64()%vectorSize]++
		}
	}
	var n float64
	for _, x := range v {
		n += x * x
	}
	if n > 0 {
		for i := range v {
			v[i] /= math.Sqrt(n)
		}
	}
	return v
}
func cosine(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

func allowedDocument(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, ".") || strings.HasSuffix(lower, "_test.go") {
		return false
	}
	for _, bad := range []string{"secret", "credential", "password", "token", "apikey", "api_key", ".env", ".pem", ".key"} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".txt")
}
func splitSections(text, fallback string) []struct{ name, text string } {
	var result []struct{ name, text string }
	section, subsection := fallback, ""
	var body []string
	isSeparator := func(line string) bool {
		line = strings.TrimSpace(line)
		return len(line) >= 8 && strings.Trim(line, "=") == ""
	}
	flush := func() {
		if s := strings.TrimSpace(strings.Join(body, "\n")); s != "" {
			name := section
			if subsection != "" {
				name += " / " + subsection
			}
			result = append(result, struct{ name, text string }{name, s})
		}
		body = nil
	}
	isSubheading := func(line string) bool {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Q: ") {
			return true
		}
		if !strings.HasSuffix(line, ":") || len(line) > 100 || strings.ToUpper(line) != line {
			return false
		}
		return strings.IndexFunc(line, unicode.IsLetter) >= 0
	}
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if i+2 < len(lines) && isSeparator(line) && strings.TrimSpace(lines[i+1]) != "" && isSeparator(lines[i+2]) {
			flush()
			section, subsection = strings.TrimSpace(lines[i+1]), ""
			i += 2
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			flush()
			section, subsection = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#")), ""
			continue
		}
		if isSubheading(line) {
			flush()
			subsection = strings.TrimSpace(line)
			continue
		}
		body = append(body, line)
	}
	flush()
	return result
}
func buildIndex(ctx context.Context, root string, progress func(string)) (Index, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Index{}, err
	}
	for path := root; ; path = filepath.Dir(path) {
		base := strings.ToLower(filepath.Base(path))
		if base == "secret" || base == "secrets" || strings.HasPrefix(base, ".") {
			return Index{}, fmt.Errorf("скрытый каталог или каталог секретов нельзя индексировать")
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	var chunks []Chunk
	files := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules" || name == "secret" || name == "secrets" || strings.Contains(strings.ToLower(name), "credential")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !allowedDocument(name) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 || info.Size() > 512*1024 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files++
		for _, part := range splitSections(string(data), filepath.Base(rel)) {
			chunks = append(chunks, Chunk{Source: filepath.ToSlash(rel), Section: part.name, Text: part.text, Vector: vector(part.name + " " + part.text)})
		}
		if progress != nil && files%10 == 0 {
			progress(fmt.Sprintf("Индекс: прочитано файлов: %d", files))
		}
		return nil
	})
	if err != nil {
		return Index{}, err
	}
	if len(chunks) == 0 {
		return Index{}, fmt.Errorf("в каталоге нет публичных .md или .txt документов")
	}
	return Index{Version: 1, Chunks: chunks}, nil
}
func (a *Agent) indexPath() string { return filepath.Join(a.indexDir, indexFile) }
func (a *Agent) Build(ctx context.Context, root string, progress func(string)) error {
	if progress != nil {
		progress("Индекс: начало чтения документов")
	}
	idx, err := buildIndex(ctx, root, progress)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.indexDir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(a.indexPath(), data, 0600); err != nil {
		return err
	}
	if progress != nil {
		progress(fmt.Sprintf("Индекс: готово, чанков: %d", len(idx.Chunks)))
	}
	return nil
}
func (a *Agent) load() (Index, error) {
	data, err := os.ReadFile(a.indexPath())
	if err != nil {
		return Index{}, fmt.Errorf("индекс недоступен; выполните /index build [КАТАЛОГ]: %w", err)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return Index{}, err
	}
	return idx, nil
}
func search(idx Index, query string, limit int) []Hit {
	q := vector(query)
	hits := make([]Hit, 0, len(idx.Chunks))
	for _, chunk := range idx.Chunks {
		hits = append(hits, Hit{chunk, cosine(q, chunk.Vector)})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit < len(hits) {
		hits = hits[:limit]
	}
	return hits
}

func main() {
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "data"
	}
	key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	url := os.Getenv("DEEPSEEK_BASE_URL")
	if url == "" {
		url = "https://api.deepseek.com/chat/completions"
	}
	model := os.Getenv("DEEPSEEK_MODEL")
	if model == "" {
		model = "deepseek-chat"
	}
	agent := NewAgent(&DeepSeekClient{key, url, model, &http.Client{Timeout: 90 * time.Second}}, filepath.Join(dataDir, "index"))
	handle := func(ctx context.Context, line string, out io.Writer) bool {
		if handled, exit := runCommand(ctx, agent, line, out); handled {
			return exit
		}
		answer, err := agent.Answer(ctx, line, func(s string) { fmt.Fprintln(out, s) })
		if err != nil {
			fmt.Fprintln(out, "Ошибка:", err)
		} else {
			fmt.Fprintln(out, answer)
		}
		return false
	}
	if err := terminal.Run(context.Background(), os.Stdin, os.Stdout, "mrkai · RAG + память задачи · /help — команды", commands, handle); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
