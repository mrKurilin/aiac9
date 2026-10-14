package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type chunk struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}

type index struct {
	Root   string  `json:"root"`
	Chunks []chunk `json:"chunks"`
}

type hit struct {
	chunk
	Score int
}

type searchIndex struct {
	root     string
	path     string
	docsOnly bool
}

func safeDocName(name string) bool {
	if strings.HasPrefix(name, ".") || strings.ToLower(filepath.Ext(name)) != ".md" {
		return false
	}
	lower := strings.ToLower(name)
	for _, word := range []string{"secret", "credential", "password", "token", "private", "apikey", "api_key"} {
		if strings.Contains(lower, word) {
			return false
		}
	}
	return true
}

func splitChunks(source, content string) []chunk {
	var result []chunk
	var heading string
	var lines []string
	flush := func() {
		prefix := heading
		if len([]rune(prefix)) > 160 {
			prefix = string([]rune(prefix)[:160])
		}
		body := strings.TrimSpace(strings.Join(lines, "\n"))
		if body == "" {
			if prefix != "" {
				result = append(result, chunk{source, prefix})
			}
		} else {
			for _, item := range splitParagraphChunks(source, body, 1000-len([]rune(prefix))-2) {
				if prefix != "" {
					item.Text = prefix + "\n\n" + item.Text
				}
				result = append(result, item)
			}
		}
		lines = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "#") && strings.HasPrefix(strings.TrimLeft(line, "#"), " ") {
			flush()
			heading = line
		} else {
			lines = append(lines, line)
		}
	}
	flush()
	return result
}

func splitParagraphChunks(source, content string, limit int) []chunk {
	var result []chunk
	var buf []rune
	flush := func() {
		if text := strings.TrimSpace(string(buf)); text != "" {
			result = append(result, chunk{source, text})
		}
		buf = nil
	}
	for _, paragraph := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n\n") {
		p := []rune(strings.TrimSpace(paragraph))
		for len(p) > 0 {
			if len(buf)+len(p)+2 <= limit {
				if len(buf) > 0 {
					buf = append(buf, '\n', '\n')
				}
				buf = append(buf, p...)
				break
			}
			if len(buf) > 0 {
				flush()
				continue
			}
			n := limit
			if len(p) < n {
				n = len(p)
			}
			buf = append(buf, p[:n]...)
			p = p[n:]
			flush()
		}
	}
	flush()
	return result
}

func (s searchIndex) build(ctx context.Context, progress func(string)) (int, error) {
	root, err := filepath.Abs(s.root)
	if err != nil {
		return 0, err
	}
	var files []string
	if !s.docsOnly {
		readme := filepath.Join(root, "README.md")
		if info, err := os.Lstat(readme); err == nil && info.Mode().IsRegular() {
			files = append(files, readme)
		} else {
			return 0, fmt.Errorf("README.md не найден в %s", root)
		}
	}
	docs := filepath.Join(root, "docs")
	if info, err := os.Lstat(docs); err != nil || !info.IsDir() {
		return 0, fmt.Errorf("папка docs не найдена в %s", root)
	}
	err = filepath.WalkDir(docs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == docs {
			return nil
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") || !safeDocName(entry.Name()+".md") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() && safeDocName(entry.Name()) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(files) == 0 || (!s.docsOnly && len(files) < 2) {
		return 0, fmt.Errorf("в docs нет Markdown документов")
	}
	result := index{Root: root}
	for i, path := range files {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() > 1<<20 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		rel, _ := filepath.Rel(root, path)
		result.Chunks = append(result.Chunks, splitChunks(filepath.ToSlash(rel), string(data))...)
		if progress != nil {
			progress(fmt.Sprintf("RAG: документы %d/%d, фрагменты %d", i+1, len(files), len(result.Chunks)))
		}
	}
	if len(result.Chunks) == 0 {
		return 0, fmt.Errorf("документы пусты")
	}
	data, err := json.Marshal(result)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return 0, err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return 0, err
	}
	return len(result.Chunks), nil
}

func (s searchIndex) load() (index, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return index{}, err
	}
	var result index
	if err := json.Unmarshal(data, &result); err != nil {
		return index{}, err
	}
	root, err := filepath.Abs(s.root)
	if err != nil || result.Root != root || len(result.Chunks) == 0 {
		return index{}, fmt.Errorf("индекс другого проекта или пуст: выполните /index build")
	}
	return result, nil
}

func terms(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

func (s searchIndex) search(question string, limit int) ([]hit, error) {
	idx, err := s.load()
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, term := range terms(question) {
		if len([]rune(term)) >= 3 {
			wanted[term] = true
		}
	}
	var hits []hit
	for _, item := range idx.Chunks {
		seen := map[string]bool{}
		score := 0
		for _, term := range terms(item.Text) {
			if wanted[term] && !seen[term] {
				score++
				seen[term] = true
			}
		}
		if score > 0 {
			hits = append(hits, hit{item, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit < len(hits) {
		hits = hits[:limit]
	}
	return hits, nil
}
