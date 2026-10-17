package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type planRecoveryError struct{ fields map[string]json.RawMessage }

func (*planRecoveryError) Error() string {
	return "не удалось извлечь все условия поиска"
}

// Salvage only known fields with unambiguous types, never raw model prose.
func recoverPlanFields(content string) map[string]json.RawMessage {
	content = strings.TrimSpace(content)
	content = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(content, "```json"), "```"))
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &fields) != nil {
		return nil
	}
	if nested, ok := fields["conditions"]; ok {
		if json.Unmarshal(nested, &fields) != nil {
			return nil
		}
	}
	template, _ := json.Marshal(searchPlan{})
	var known map[string]json.RawMessage
	_ = json.Unmarshal(template, &known)
	// Optional fields are omitted by Marshal and must be registered explicitly.
	known["departure_end"] = json.RawMessage(`""`)
	known["return_origin"] = json.RawMessage(`""`)
	known["new_search"] = json.RawMessage(`false`)
	known["destination_country"] = json.RawMessage(`""`)
	result := map[string]json.RawMessage{}
	for field, raw := range fields {
		target, ok := known[field]
		if !ok || string(raw) == "null" {
			continue
		}
		var value any
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		switch string(target) {
		case `""`:
			if _, ok := value.(string); !ok {
				continue
			}
		case "false":
			if text, ok := value.(string); ok {
				parsed, err := strconv.ParseBool(text)
				if err != nil {
					continue
				}
				value = parsed
			}
			if _, ok := value.(bool); !ok {
				continue
			}
		default:
			if text, ok := value.(string); ok {
				parsed, err := strconv.Atoi(text)
				if err != nil {
					continue
				}
				value = parsed
			}
			number, ok := value.(float64)
			if ok && number != float64(int(number)) {
				continue
			}
			switch value.(type) {
			case int, float64:
			default:
				continue
			}
		}
		encoded, _ := json.Marshal(value)
		result[field] = encoded
	}
	return result
}

var relativePeriod = regexp.MustCompile(`(?i)ближайш[а-яё]*\s+(\d{1,3})\s*(день|дня|дней|недел[а-яё]*|месяц[а-яё]*)`)
var nextHalfYear = regexp.MustCompile(`(?i)ближайш[а-яё]*\s+полгода`)
var nextMonth = regexp.MustCompile(`(?i)ближайш[а-яё]*\s+месяц`)
var roundTripText = regexp.MustCompile(`(?i)туда[\s—–-]+обратно`)
var oneWayText = regexp.MustCompile(`(?i)в\s+одну\s+сторону`)
var weekStay = regexp.MustCompile(`(?i)(на|быть|там)\s+неделю`)

func fallbackPlan(base searchPlan, fields map[string]json.RawMessage, text string, now time.Time) searchPlan {
	tripKnown := base.Action == "search" || base.RoundTrip
	if _, ok := fields["round_trip"]; ok {
		tripKnown = true
	}
	encoded, _ := json.Marshal(base)
	var merged map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &merged)
	for key, value := range fields {
		merged[key] = value
	}
	encoded, _ = json.Marshal(merged)
	_ = json.Unmarshal(encoded, &base)
	base.Action = "search"
	base.Question = ""
	if roundTripText.MatchString(text) {
		base.RoundTrip = true
		tripKnown = true
	}
	if oneWayText.MatchString(text) {
		base.RoundTrip = false
		base.Return = ""
		base.MinDays = 0
		base.MaxDays = 0
		tripKnown = true
	}
	if weekStay.MatchString(text) {
		base.RoundTrip = true
		base.MinDays = 7
		base.MaxDays = 7
		tripKnown = true
	}
	// Common date refinements can be applied exactly even if the model's JSON failed.
	first, _ := time.Parse("2006-01-02", now.Format("2006-01-02"))
	var last time.Time
	if parts := relativePeriod.FindStringSubmatch(text); len(parts) > 0 {
		count, _ := strconv.Atoi(parts[1])
		unit := strings.ToLower(parts[2])
		if count > 0 && count <= 731 {
			switch {
			case strings.HasPrefix(unit, "недел"):
				last = first.AddDate(0, 0, count*7-1)
			case strings.HasPrefix(unit, "месяц"):
				last = first.AddDate(0, count, 0).AddDate(0, 0, -1)
			default:
				last = first.AddDate(0, 0, count-1)
			}
		}
	} else if nextHalfYear.MatchString(text) {
		last = first.AddDate(0, 6, 0).AddDate(0, 0, -1)
	} else if nextMonth.MatchString(text) {
		last = first.AddDate(0, 1, 0).AddDate(0, 0, -1)
	}
	if !last.IsZero() {
		base.Departure = first.Format("2006-01-02")
		base.DepartureEnd = last.Format("2006-01-02")
	}
	if !tripKnown {
		base.Action = "ask"
		base.Question = "Нужен билет в одну сторону или туда-обратно?"
	}
	return base
}

func (c *travelClient) describeConditions(p searchPlan) string {
	var parts []string
	cityName := func(value string) string {
		if v, ok := c.cities[value]; ok {
			return v.Name
		}
		return value
	}
	if p.Origin != "" {
		parts = append(parts, "откуда: "+cityName(p.Origin))
	}
	if p.Destination != "" {
		parts = append(parts, "куда: "+cityName(p.Destination))
	}
	if p.ReturnOrigin != "" {
		parts = append(parts, "обратный вылет из: "+cityName(p.ReturnOrigin))
	}
	if p.Departure != "" {
		dates := p.Departure
		if p.DepartureEnd != "" {
			dates += " — " + p.DepartureEnd
		}
		parts = append(parts, "вылет: "+dates)
	}
	if p.RoundTrip {
		parts = append(parts, "туда-обратно")
	} else if p.Action == "search" {
		parts = append(parts, "в одну сторону")
	}
	if p.MinDays > 0 || p.MaxDays > 0 {
		parts = append(parts, fmt.Sprintf("длительность: %d–%d дней", p.MinDays, p.MaxDays))
	}
	if p.Budget > 0 {
		parts = append(parts, fmt.Sprintf("бюджет: до %d ₽", p.Budget))
	}
	return "Применённые условия: " + strings.Join(parts, "; ") + "."
}
