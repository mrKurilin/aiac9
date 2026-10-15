package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func safeProjectFile(root, name string, write bool) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "\\") {
		return "", fmt.Errorf("неверный путь")
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("путь вне проекта")
	}
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if strings.HasPrefix(part, ".") || !safeDocName(part+".md") {
			return "", fmt.Errorf("скрытый или чувствительный путь")
		}
	}
	ext := strings.ToLower(filepath.Ext(clean))
	if write {
		if ext != ".md" || filepath.Dir(clean) != "docs" {
			return "", fmt.Errorf("запись разрешена только в docs/*.md")
		}
	} else if ext != ".md" && ext != ".go" && ext != ".txt" {
		return "", fmt.Errorf("тип файла не поддерживается")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, clean)
	for p := filepath.Dir(path); p != root; p = filepath.Dir(p) {
		if p == filepath.Dir(p) {
			return "", fmt.Errorf("путь вне проекта")
		}
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("ссылки запрещены")
		}
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return "", fmt.Errorf("необычный файл запрещён")
	}
	return path, nil
}

func projectList(ctx context.Context, root string) (string, error) {
	var names []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
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
			if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "data" || entry.Name() == "vendor" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if len(names) >= 200 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if entry.Type().IsRegular() {
			if _, err := safeProjectFile(root, rel, false); err == nil {
				names = append(names, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	return strings.Join(names, "\n"), nil
}

func projectRead(root, name string) (string, error) {
	path, err := safeProjectFile(root, name, false)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > 64<<10 {
		return "", fmt.Errorf("файл слишком велик")
	}
	data, err := os.ReadFile(path)
	return string(data), err
}

func projectSearch(ctx context.Context, root, query string) (string, error) {
	if len(query) < 2 || len(query) > 100 {
		return "", fmt.Errorf("длина запроса должна быть 2–100 символов")
	}
	list, err := projectList(ctx, root)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, name := range strings.Split(list, "\n") {
		if name == "" {
			continue
		}
		content, err := projectRead(root, name)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(content, "\n") {
			if strings.Contains(strings.ToLower(line), strings.ToLower(query)) {
				if len(line) > 240 {
					line = line[:240]
				}
				matches = append(matches, fmt.Sprintf("%s:%d: %s", name, i+1, line))
				if len(matches) >= 50 {
					return strings.Join(matches, "\n"), nil
				}
			}
		}
	}
	if len(matches) == 0 {
		return "Совпадений нет", nil
	}
	return strings.Join(matches, "\n"), nil
}

func projectWrite(root, name, content string) (string, error) {
	path, err := safeProjectFile(root, name, true)
	if err != nil {
		return "", err
	}
	if len(content) > 64<<10 {
		return "", fmt.Errorf("содержимое слишком велико")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mrkai-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return fmt.Sprintf("Сохранён %s: было %d байт, стало %d байт", name, len(old), len(content)), nil
}
