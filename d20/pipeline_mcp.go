package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type searchHit struct {
	Source string `json:"source"`
	Text   string `json:"text"`
	Score  int    `json:"score"`
}

type pipelineService struct {
	knowledgeDir string
	reportsDir   string
}

func pipelineTools() []MCPTool {
	return []MCPTool{
		{Name: "search", Description: "Найти подходящие документы в локальном каталоге knowledge; вернёт исходный текст и имя файла", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)},
		{Name: "summarize", Description: "Составить краткую выдержку из текста, полученного инструментом search", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`)},
		{Name: "save_to_file", Description: "Сохранить текст в data/reports под указанным именем .md", InputSchema: json.RawMessage(`{"type":"object","properties":{"filename":{"type":"string"},"text":{"type":"string"}},"required":["filename","text"],"additionalProperties":false}`)},
		{Name: "run_pipeline", Description: "Автоматически выполнить search → summarize → save_to_file для запроса", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"filename":{"type":"string","description":"Имя отчёта .md в data/reports"}},"required":["query","filename"],"additionalProperties":false}`)},
	}
}

func queryWords(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func (s pipelineService) search(query string) ([]searchHit, error) {
	words := queryWords(query)
	if len(words) == 0 {
		return nil, fmt.Errorf("запрос поиска пуст")
	}
	entries, err := os.ReadDir(s.knowledgeDir)
	if err != nil {
		return nil, fmt.Errorf("каталог knowledge недоступен: %w", err)
	}
	hits := []searchHit{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || (filepath.Ext(name) != ".md" && filepath.Ext(name) != ".txt") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 64*1024 || info.Size() == 0 || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.knowledgeDir, name))
		if err != nil {
			return nil, fmt.Errorf("не удалось прочитать документ %s: %w", name, err)
		}
		body := strings.TrimSpace(string(data))
		lower := strings.ToLower(body)
		score := 0
		for _, word := range words {
			if strings.Contains(lower, word) || strings.Contains(strings.ToLower(name), word) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, searchHit{Source: name, Text: body, Score: score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Source < hits[j].Source
	})
	if len(hits) > 5 {
		hits = hits[:5]
	}
	return hits, nil
}

func summarizeText(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("текст для обработки пуст")
	}
	lines := strings.Split(text, "\n")
	selected := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "*- "))
		if line == "" {
			continue
		}
		selected = append(selected, line)
		if len(selected) == 3 {
			break
		}
	}
	return strings.Join(selected, " "), nil
}

func (s pipelineService) save(filename, text string) (string, error) {
	if filename == "" || filename != filepath.Base(filename) || strings.HasPrefix(filename, ".") || filepath.Ext(filename) != ".md" || strings.ContainsAny(filename, "/\\:") {
		return "", fmt.Errorf("имя отчёта должно быть простым именем файла .md")
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("нечего сохранять")
	}
	if err := os.MkdirAll(s.reportsDir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(s.reportsDir, filename)
	tmp, err := os.CreateTemp(s.reportsDir, ".report-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.WriteString(tmp, text+"\n"); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func (s pipelineService) run(query, filename string, progress func(string)) (map[string]any, error) {
	progress("search: читаю локальные документы")
	hits, err := s.search(query)
	if err != nil {
		return nil, err
	}
	progress(fmt.Sprintf("search: найдено документов: %d", len(hits)))
	if len(hits) == 0 {
		return nil, fmt.Errorf("по запросу ничего не найдено")
	}
	parts := make([]string, 0, len(hits))
	sources := make([]string, 0, len(hits))
	for _, hit := range hits {
		parts = append(parts, hit.Text)
		sources = append(sources, hit.Source)
	}
	progress("summarize: обрабатываю найденный текст")
	summary, err := summarizeText(strings.Join(parts, "\n"))
	if err != nil {
		return nil, err
	}
	report := fmt.Sprintf("# %s\n\n%s\n\nИсточники: %s", query, summary, strings.Join(sources, ", "))
	progress("save_to_file: сохраняю отчёт")
	path, err := s.save(filename, report)
	if err != nil {
		return nil, err
	}
	progress("run_pipeline: завершено")
	return map[string]any{"query": query, "sources": sources, "found": len(hits), "summary": summary, "path": path}, nil
}

func servePipelineMCP(in io.Reader, out io.Writer, knowledgeDir, reportsDir string) error {
	s := pipelineService{knowledgeDir: knowledgeDir, reportsDir: reportsDir}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(out)
	initialized := false
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
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "mrkPipeline", "version": "d20"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			if !initialized {
				response.Error = &mcpError{Code: -32000, Message: "клиент не инициализирован"}
			} else {
				result = map[string]any{"tools": pipelineTools()}
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
				Query    string `json:"query"`
				Filename string `json:"filename"`
				Text     string `json:"text"`
			}
			if json.Unmarshal(params.Arguments, &args) != nil {
				response.Error = &mcpError{Code: -32602, Message: "неверные аргументы"}
				break
			}
			var value any
			var err error
			switch params.Name {
			case "search":
				value, err = s.search(args.Query)
			case "summarize":
				value, err = summarizeText(args.Text)
			case "save_to_file":
				value, err = s.save(args.Filename, args.Text)
			case "run_pipeline":
				value, err = s.run(args.Query, args.Filename, func(message string) {
					params, _ := json.Marshal(map[string]string{"data": message})
					_ = encoder.Encode(mcpMessage{JSONRPC: "2.0", Method: "notifications/message", Params: params})
				})
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
