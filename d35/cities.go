package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

func normalizedCity(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "ё", "е")
}
func (c *travelClient) resolveCity(value string) string {
	if v, ok := c.cities[strings.ToUpper(strings.TrimSpace(value))]; ok {
		return v.Code
	}
	normalized := normalizedCity(value)
	match := ""
	for code, v := range c.cities {
		if normalizedCity(v.Name) == normalized {
			if match != "" && match != code {
				return value
			} // Don't silently choose between ambiguous cities.
			match = code
		}
	}
	if match != "" {
		return match
	}
	return strings.TrimSpace(value)
}
func (c *travelClient) resolvePlan(p *searchPlan) {
	p.Origin = c.resolveCity(p.Origin)
	if p.ReturnOrigin != "" {
		p.ReturnOrigin = c.resolveCity(p.ReturnOrigin)
	}
	p.DestinationCountry = strings.ToUpper(strings.TrimSpace(p.DestinationCountry))
	if p.Destination != "" {
		p.Destination = c.resolveCity(p.Destination)
	}
}

// Send relevant candidates rather than thousands of cities on every request.
func (c *travelClient) cityCatalogFor(context string) string {
	normalized := normalizedCity(context)
	words := strings.FieldsFunc(normalized, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	wordSet := map[string]bool{}
	for _, word := range words {
		wordSet[word] = true
	}
	type candidate struct {
		text  string
		score int
	}
	var list []candidate
	for code, v := range c.cities {
		score := 0
		if wordSet[strings.ToLower(code)] {
			score = 100
		}
		name := normalizedCity(v.Name)
		if strings.Contains(normalized, name) {
			score += 50
		}
		for _, part := range strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) }) {
			runes := []rune(part)
			if len(runes) < 4 {
				continue
			}
			prefix := string(runes[:4])
			for _, word := range words {
				if strings.HasPrefix(word, prefix) {
					score++
					break
				}
			}
		}
		if score > 0 {
			list = append(list, candidate{fmt.Sprintf("%s = %s (%s)", v.Name, code, v.Country), score})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].text < list[j].text
	})
	var lines []string
	for _, v := range list[:min(len(list), 64)] {
		lines = append(lines, v.text)
	}
	return strings.Join(lines, "\n")
}
