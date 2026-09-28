package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (a *Agent) indexPath(strategy string) string {
	return filepath.Join(a.indexDir, strategy+".json")
}

func (a *Agent) BuildIndexes(ctx context.Context, root string, progress func(string)) ([]DocumentIndex, error) {
	if root == "" {
		root = "."
	}
	if progress != nil {
		progress("Индекс: чтение документов из " + root)
	}
	docs, err := collectDocuments(ctx, root, progress)
	if err != nil {
		return nil, err
	}
	return a.buildIndexesFromDocuments(ctx, docs, progress)
}

func (a *Agent) buildIndexesFromDocuments(ctx context.Context, docs []sourceDocument, progress func(string)) ([]DocumentIndex, error) {
	indexes := make([]DocumentIndex, 0, 2)
	for _, strategy := range []string{"fixed", "structure"} {
		if progress != nil {
			progress("Индекс: генерация эмбеддингов, стратегия " + strategy)
		}
		index, err := buildDocumentIndex(ctx, docs, strategy, progress)
		if err != nil {
			return nil, err
		}
		if err := saveDocumentIndex(a.indexPath(strategy), index); err != nil {
			return nil, fmt.Errorf("сохранить %s: %w", strategy, err)
		}
		indexes = append(indexes, index)
		if progress != nil {
			progress("Индекс: сохранён " + a.indexPath(strategy))
		}
	}
	return indexes, nil
}

func (a *Agent) LoadIndex(strategy string) (DocumentIndex, error) {
	if strategy != "fixed" && strategy != "structure" {
		return DocumentIndex{}, fmt.Errorf("стратегия: fixed или structure")
	}
	return loadDocumentIndex(a.indexPath(strategy))
}

func printIndexHits(out io.Writer, hits []IndexHit) {
	if len(hits) == 0 {
		fmt.Fprintln(out, "Совпадений нет.")
		return
	}
	for _, hit := range hits {
		fmt.Fprintf(out, "  %.3f  %s  [%s]  %s\n", hit.Score, hit.Chunk.ChunkID, hit.Chunk.Section, hit.Chunk.Source)
	}
}

func runIndexCommand(ctx context.Context, a *Agent, rest string, out io.Writer) error {
	action, arguments, _ := strings.Cut(strings.TrimSpace(rest), " ")
	switch action {
	case "github":
		topic := strings.TrimSpace(arguments)
		if topic == "" {
			return fmt.Errorf("использование: /index github ТЕМА")
		}
		result, err := a.IndexGitHubReadmes(ctx, topic, func(message string) { fmt.Fprintln(out, message) })
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "GitHub: проиндексировано README: %d, слов: %d; fixed: %d чанков, structure: %d чанков.\n", result.Repositories, result.Words, result.FixedChunks, result.StructureChunks)
		if !result.TargetMet {
			fmt.Fprintln(out, "Корпус меньше учебной цели 10 000 слов; уточните или расширьте тему.")
		}
		for _, project := range result.Projects {
			fmt.Fprintf(out, "  ★ %d  %s\n", project.Stars, project.Source)
		}
		return nil
	case "build":
		root := strings.TrimSpace(arguments)
		if root == "" {
			root = "."
		}
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return fmt.Errorf("каталог документов не найден: %s", root)
		}
		indexes, err := a.BuildIndexes(ctx, root, func(message string) { fmt.Fprintln(out, message) })
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Готово: %d документов, %d слов; fixed: %d чанков, structure: %d чанков.\n", indexes[0].Sources, indexes[0].Words, len(indexes[0].Chunks), len(indexes[1].Chunks))
		return nil
	case "status":
		if strings.TrimSpace(arguments) != "" {
			return fmt.Errorf("использование: /index status")
		}
		for _, strategy := range []string{"fixed", "structure"} {
			index, err := a.LoadIndex(strategy)
			if os.IsNotExist(err) {
				fmt.Fprintf(out, "%s: не построен\n", strategy)
				continue
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: %d документов, %d слов, %d чанков, %s\n", strategy, index.Sources, index.Words, len(index.Chunks), a.indexPath(strategy))
		}
		return nil
	case "search":
		strategy, query, ok := strings.Cut(strings.TrimSpace(arguments), " ")
		if !ok || strings.TrimSpace(query) == "" {
			return fmt.Errorf("использование: /index search fixed|structure ЗАПРОС")
		}
		index, err := a.LoadIndex(strategy)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: лучшие совпадения для %q\n", strategy, query)
		printIndexHits(out, searchDocumentIndex(index, query, 5))
		return nil
	case "compare":
		query := strings.TrimSpace(arguments)
		if query == "" {
			return fmt.Errorf("использование: /index compare ЗАПРОС")
		}
		for _, strategy := range []string{"fixed", "structure"} {
			index, err := a.LoadIndex(strategy)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: %d чанков, топ-3 для %q\n", strategy, len(index.Chunks), query)
			printIndexHits(out, searchDocumentIndex(index, query, 3))
		}
		return nil
	default:
		return fmt.Errorf("использование: /index github ТЕМА, /index build [КАТАЛОГ], /index status, /index search fixed|structure ЗАПРОС, /index compare ЗАПРОС")
	}
}
