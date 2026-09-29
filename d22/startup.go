package main

import (
	"fmt"
	"io"
	"strings"
)

const (
	deepSeekKeysURL   = "https://platform.deepseek.com/api_keys"
	ollamaDownloadURL = "https://ollama.com/download"
	ollamaKeysURL     = "https://ollama.com/settings/keys"
)

// printStartupChecks reports configuration state without ever printing token
// values. RAG stays optional, so a missing provider only produces a useful
// instruction instead of preventing the terminal chat from opening.
func printStartupChecks(out io.Writer, deepSeekKey, embedURL, ollamaKey string, ollamaAvailable bool) {
	if strings.TrimSpace(deepSeekKey) == "" {
		fmt.Fprintf(out, "DeepSeek: DEEPSEEK_API_KEY не задан; ответы модели недоступны. Ключ: %s\n", deepSeekKeysURL)
	} else {
		fmt.Fprintln(out, "DeepSeek: DEEPSEEK_API_KEY задан.")
	}

	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(embedURL)), "https://ollama.com/") {
		if strings.TrimSpace(ollamaKey) == "" {
			fmt.Fprintf(out, "Ollama Cloud: OLLAMA_API_KEY не задан; RAG недоступен. Ключ: %s\n", ollamaKeysURL)
			return
		}
		fmt.Fprintln(out, "Ollama Cloud: OLLAMA_API_KEY задан.")
		return
	}

	if !ollamaAvailable {
		fmt.Fprintf(out, "Локальный Ollama не найден; RAG недоступен. Установка: %s\n", ollamaDownloadURL)
		fmt.Fprintln(out, "После установки выполните: ollama pull embeddinggemma")
		return
	}
	fmt.Fprintln(out, "Локальный Ollama найден. Для RAG нужна модель: ollama pull embeddinggemma")
}
