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
	root      string
	path      string
	preferred map[string]bool
	tracked   map[string]bool
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
			if len(buf)+len(p)+2 <= 1000 {
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
			n := 1000
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
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			if !safeDocName(entry.Name()+".md") || entry.Name() == "data" || entry.Name() == "testdata" || entry.Name() == "vendor" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		code := strings.HasSuffix(entry.Name(), ".go") && safeDocName(strings.TrimSuffix(entry.Name(), ".go")+".md")
		if entry.Type().IsRegular() && (safeDocName(entry.Name()) || code) {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if s.tracked != nil && !s.tracked[filepath.ToSlash(rel)] {
				return nil
			}
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	// Changed files come first; reserve capacity for both documentation and code.
	sort.SliceStable(files, func(i, j int) bool {
		left, _ := filepath.Rel(root, files[i])
		right, _ := filepath.Rel(root, files[j])
		if s.preferred[filepath.ToSlash(left)] != s.preferred[filepath.ToSlash(right)] {
			return s.preferred[filepath.ToSlash(left)]
		}
		return left == "README.md" && right != "README.md"
	})
	selected := make([]string, 0, len(files))
	counts := map[string]int{}
	for _, path := range files {
		ext := strings.ToLower(filepath.Ext(path))
		if counts[ext] < 100 {
			selected = append(selected, path)
			counts[ext]++
		}
	}
	files = selected
	result := index{Root: root}
	for i, path := range files {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return 0, fmt.Errorf("RAG: не удалось проверить файл: %w", err)
		}
		if info.Size() > 1<<20 {
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
	return rankChunks(idx, question, limit), nil
}

func rankChunks(idx index, question string, limit int) []hit {
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
		for _, term := range terms(item.Source + " " + item.Text) {
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
	return hits
}

// Retrieve each kind separately so code matches cannot displace documentation.
func (s searchIndex) reviewContext(query string) ([]hit, error) {
	idx, err := s.load()
	if err != nil {
		return nil, err
	}
	ranked := rankChunks(idx, query, len(idx.Chunks))
	var result []hit
	for _, extension := range []string{".md", ".go"} {
		count := 0
		for _, item := range ranked {
			if strings.HasSuffix(strings.ToLower(item.Source), extension) && count < 3 {
				result = append(result, item)
				count++
			}
		}
		if count == 0 {
			// Supply baseline context when the diff vocabulary has no lexical match.
			for _, item := range idx.Chunks {
				if strings.HasSuffix(strings.ToLower(item.Source), extension) && count < 3 {
					result = append(result, hit{chunk: item})
					count++
				}
			}
		}
	}
	return result, nil
}
