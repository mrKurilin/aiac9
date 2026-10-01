package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

var answerNumber = regexp.MustCompile(`[0-9]+`)

// modelAnswer accepts ordinary text as well as JSON, including JSON in a code fence.
// Source metadata and quotations are taken from the index, not from this text.
func modelAnswer(raw string) string {
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(value, "```") {
		if newline := strings.IndexByte(value, '\n'); newline >= 0 {
			value = value[newline+1:]
			if end := strings.LastIndex(value, "```"); end >= 0 {
				value = strings.TrimSpace(value[:end])
			}
		}
	}
	if start := strings.IndexByte(value, '{'); start >= 0 {
		var response struct {
			Answer string `json:"answer"`
		}
		if err := json.NewDecoder(strings.NewReader(value[start:])).Decode(&response); err == nil {
			return strings.TrimSpace(response.Answer)
		}
		if start == 0 {
			return ""
		}
	}
	for _, marker := range []string{"\nИсточники:", "\nИсточники и цитаты:", "\nSources:", "\nCitations:"} {
		if index := strings.Index(value, marker); index >= 0 {
			value = value[:index]
		}
	}
	value = strings.TrimSpace(strings.TrimPrefix(value, "Answer:"))
	value = strings.TrimSpace(strings.TrimPrefix(value, "Ответ:"))
	return value
}

func uncertainAnswer(answer string) bool {
	lower := strings.ToLower(strings.TrimSpace(answer))
	for _, prefix := range []string{"не знаю", "неизвестно", "недостаточно данных", "i don't know", "i do not know", "insufficient information"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return answer == "" || len([]rune(answer)) > 1000
}

func termSet(value string) map[string]bool {
	result := make(map[string]bool)
	for _, term := range tokens(value) {
		if len([]rune(term)) > 1 {
			result[term] = true
		}
	}
	return result
}

func overlap(a, b map[string]bool) int {
	count := 0
	for term := range a {
		if b[term] {
			count++
		}
	}
	return count
}

// evidenceForAnswer chooses a verbatim line that covers the answer and relates
// to the question. It rejects numeric claims missing from the source line.
func evidenceForAnswer(answer, question string, hits []Hit) (Citation, bool) {
	if uncertainAnswer(answer) {
		return Citation{}, false
	}
	answerTerms := termSet(answer)
	questionTerms := termSet(question)
	answerNumbers := answerNumber.FindAllString(answer, -1)
	bestScore := 0.0
	var best Citation
	for _, hit := range hits {
		contextTerms := termSet(hit.Chunk.Section)
		for _, line := range strings.Split(hit.Chunk.Text, "\n") {
			quote := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-* "))
			if len([]rune(quote)) < 8 {
				continue
			}
			quoteNumbers := answerNumber.FindAllString(quote, -1)
			allNumbersPresent := true
			for _, number := range answerNumbers {
				found := false
				for _, quoted := range quoteNumbers {
					if number == quoted {
						found = true
						break
					}
				}
				if !found {
					allNumbersPresent = false
					break
				}
			}
			if !allNumbersPresent {
				continue
			}
			lineTerms := termSet(quote)
			answerOverlap := overlap(answerTerms, lineTerms)
			questionOverlap := overlap(questionTerms, lineTerms) + overlap(questionTerms, contextTerms)
			if answerOverlap == 0 || questionOverlap == 0 {
				continue
			}
			score := float64(3*answerOverlap+questionOverlap) + hit.Score
			if score > bestScore {
				bestScore = score
				best = Citation{hit.Chunk.Source, hit.Chunk.Section, quote}
			}
		}
	}
	return best, bestScore > 0
}
