package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

type Embedder interface {
	ID() string
	Embed(context.Context, []string) ([][]float64, error)
}

// HashEmbedder preserves the offline d21 behavior for tests and explicitly
// configured local development. The normal d22 entrypoint uses Ollama.
type HashEmbedder struct{}

func (HashEmbedder) ID() string { return "hash-tf-384" }

func (HashEmbedder) Embed(_ context.Context, input []string) ([][]float64, error) {
	vectors := make([][]float64, len(input))
	for i, text := range input {
		vectors[i] = hashTF(text)
	}
	return vectors, nil
}

type OllamaEmbedder struct {
	URL    string
	Model  string
	APIKey string
	HTTP   *http.Client
}

func NewOllamaEmbedder(url, model, apiKey string, httpClient *http.Client) *OllamaEmbedder {
	return &OllamaEmbedder{URL: strings.TrimRight(url, "/"), Model: strings.TrimSpace(model), APIKey: strings.TrimSpace(apiKey), HTTP: httpClient}
}

func (o *OllamaEmbedder) ID() string { return "ollama:" + o.Model }

func (o *OllamaEmbedder) Embed(ctx context.Context, input []string) ([][]float64, error) {
	if o.Model == "" {
		return nil, fmt.Errorf("не задана модель Ollama")
	}
	if len(input) == 0 {
		return [][]float64{}, nil
	}
	body, err := json.Marshal(map[string]any{"model": o.Model, "input": input, "truncate": false})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("сетевая ошибка Ollama")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Ollama API, HTTP %d", resp.StatusCode)
	}
	var result struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("разобрать ответ Ollama: %w", err)
	}
	if err := validateVectors(result.Embeddings, len(input)); err != nil {
		return nil, err
	}
	return result.Embeddings, nil
}

func validateVectors(vectors [][]float64, expected int) error {
	if len(vectors) != expected {
		return fmt.Errorf("Ollama вернул %d векторов вместо %d", len(vectors), expected)
	}
	dimensions := 0
	for _, vector := range vectors {
		if len(vector) == 0 {
			return fmt.Errorf("Ollama вернул пустой вектор")
		}
		if dimensions == 0 {
			dimensions = len(vector)
		}
		if len(vector) != dimensions {
			return fmt.Errorf("Ollama вернул векторы разной длины")
		}
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("Ollama вернул недопустимое значение вектора")
			}
		}
	}
	return nil
}
