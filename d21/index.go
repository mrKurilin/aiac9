package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const embeddingDimensions = 384
const chunkWords = 180
const chunkOverlap = 30
const maxDocumentBytes = 512 * 1024
const maxCorpusBytes = 32 * 1024 * 1024

type IndexedChunk struct {
	Source  string    `json:"source"`
	Title   string    `json:"title"`
	Section string    `json:"section"`
	ChunkID string    `json:"chunk_id"`
	Text    string    `json:"text"`
	Vector  []float64 `json:"vector"`
}

type DocumentIndex struct {
	Version  int            `json:"version"`
	Strategy string         `json:"strategy"`
	Sources  int            `json:"sources"`
	Words    int            `json:"words"`
	Chunks   []IndexedChunk `json:"chunks"`
}

type IndexHit struct {
	Chunk IndexedChunk
	Score float64
}

type sourceDocument struct {
	path   string
	source string
	text   string
}

// collectDocuments reads only regular public text/code files. Hidden and
// configuration trees are deliberately outside the indexing boundary.
func collectDocuments(ctx context.Context, root string, progress func(string)) ([]sourceDocument, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var docs []sourceDocument
	total := int64(0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "data" || name == "node_modules" || name == "vendor" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || strings.HasPrefix(name, ".") || !publicDocumentName(name) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxDocumentBytes || info.Size() == 0 {
			return nil
		}
		if total+info.Size() > maxCorpusBytes {
			return fmt.Errorf("корпус превышает лимит %d байт", maxCorpusBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.IndexByte(string(data), 0) >= 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		total += info.Size()
		docs = append(docs, sourceDocument{path: filepath.ToSlash(rel), text: string(data)})
		if progress != nil && len(docs)%10 == 0 {
			progress(fmt.Sprintf("Индекс: прочитано файлов: %d", len(docs)))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("в каталоге нет .go, .md или .txt документов")
	}
	return docs, nil
}

func publicDocumentName(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, "_test.go") {
		return false
	}
	for _, forbidden := range []string{"secret", "credential", "password", "passwd", "token", "apikey", "api_key", "private"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	switch filepath.Ext(lower) {
	case ".go", ".md", ".txt":
		return true
	}
	return false
}

func splitWords(text string) []string { return strings.Fields(text) }

func fixedChunks(words []string) []string {
	var chunks []string
	for start := 0; start < len(words); start += chunkWords - chunkOverlap {
		end := start + chunkWords
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, strings.Join(words[start:end], " "))
		if end == len(words) {
			break
		}
	}
	return chunks
}

type documentSection struct {
	name string
	text string
}

func structuralSections(doc sourceDocument) []documentSection {
	section := filepath.Base(doc.path)
	var parts []documentSection
	var lines []string
	flush := func() {
		if strings.TrimSpace(strings.Join(lines, "")) != "" {
			parts = append(parts, documentSection{name: section, text: strings.Join(lines, "")})
		}
		lines = nil
	}
	for _, line := range strings.SplitAfter(doc.text, "\n") {
		trimmed := strings.TrimSpace(line)
		newSection := ""
		if filepath.Ext(doc.path) == ".go" {
			for _, prefix := range []string{"func ", "type ", "const ", "var "} {
				if strings.HasPrefix(trimmed, prefix) {
					newSection = trimmed
					break
				}
			}
		} else if strings.HasPrefix(trimmed, "#") {
			newSection = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		}
		if newSection != "" {
			flush()
			section = newSection
		}
		lines = append(lines, line)
	}
	flush()
	return parts
}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// hashTF embeds local text into a fixed dimensional, unit length TF vector.
// It is deterministic and offline; no pretrained semantic model is involved.
func hashTF(text string) []float64 {
	vector := make([]float64, embeddingDimensions)
	for _, word := range tokenize(text) {
		if len([]rune(word)) < 2 {
			continue
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(word))
		vector[h.Sum64()%embeddingDimensions]++
	}
	var norm float64
	for _, value := range vector {
		norm += value * value
	}
	if norm > 0 {
		for i := range vector {
			vector[i] /= math.Sqrt(norm)
		}
	}
	return vector
}

func buildDocumentIndex(ctx context.Context, docs []sourceDocument, strategy string, progress func(string)) (DocumentIndex, error) {
	if strategy != "fixed" && strategy != "structure" {
		return DocumentIndex{}, fmt.Errorf("неизвестная стратегия %q", strategy)
	}
	index := DocumentIndex{Version: 1, Strategy: strategy, Sources: len(docs), Chunks: []IndexedChunk{}}
	for _, doc := range docs {
		if err := ctx.Err(); err != nil {
			return DocumentIndex{}, err
		}
		index.Words += len(splitWords(doc.text))
		sections := []documentSection{{name: filepath.Base(doc.path), text: doc.text}}
		if strategy == "structure" {
			sections = structuralSections(doc)
		}
		sequence := 0
		source := doc.path
		if doc.source != "" {
			source = doc.source
		}
		for _, section := range sections {
			for _, part := range fixedChunks(splitWords(section.text)) {
				sequence++
				index.Chunks = append(index.Chunks, IndexedChunk{
					Source: source, Title: filepath.Base(doc.path), Section: section.name,
					ChunkID: fmt.Sprintf("%s:%04d", doc.path, sequence), Text: part, Vector: hashTF(part),
				})
			}
		}
	}
	if progress != nil {
		progress(fmt.Sprintf("Индекс %s: %d документов, %d слов, %d чанков", strategy, index.Sources, index.Words, len(index.Chunks)))
	}
	return index, nil
}

func saveDocumentIndex(path string, index DocumentIndex) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(index)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".index-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func loadDocumentIndex(path string) (DocumentIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return DocumentIndex{}, err
	}
	var index DocumentIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return DocumentIndex{}, err
	}
	if index.Version != 1 || (index.Strategy != "fixed" && index.Strategy != "structure") {
		return DocumentIndex{}, fmt.Errorf("неподдерживаемый формат индекса")
	}
	return index, nil
}

func searchDocumentIndex(index DocumentIndex, query string, limit int) []IndexHit {
	queryVector := hashTF(query)
	var hits []IndexHit
	for _, chunk := range index.Chunks {
		if len(chunk.Vector) != embeddingDimensions {
			continue
		}
		var score float64
		for i, value := range chunk.Vector {
			score += value * queryVector[i]
		}
		if score > 0 {
			hits = append(hits, IndexHit{Chunk: chunk, Score: score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].Chunk.ChunkID < hits[j].Chunk.ChunkID
		}
		return hits[i].Score > hits[j].Score
	})
	if len(hits) > limit {
		return hits[:limit]
	}
	return hits
}
