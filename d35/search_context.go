package main

import "regexp"

var searchStart = regexp.MustCompile(`(?is)^\s*(?:найди|найти|поищи|ищи|новый поиск|новая поездка|новый маршрут)(?:\s|:)`)
var explicitRestart = regexp.MustCompile(`(?is)^\s*(?:новый поиск|новая поездка|новый маршрут)(?:\s|:)`)
var routeInRequest = regexp.MustCompile(`(?is)(?:\s[-—–→]\s|из\s+.+\s+в\s+)`)
var returnMention = regexp.MustCompile(`(?i)(?:туда[\s—–-]+обратно|обратн[а-яё]*\s+(?:билет|вылет)|обратно\s+(?:из|в)|вернут[а-яё]*|возвращ[а-яё]*)`)
var stayMention = regexp.MustCompile(`(?i)(?:на|быть|там|остаться|пробыть)\s+(?:(?:\d+(?:\s*[-—–]\s*\d+)?|одну|две|три|четыре)\s+)?(?:недел[а-яё]*|день|дня|дней)`)
var denyReturn = regexp.MustCompile(`(?i)(?:не\s+туда[\s—–-]+обратно|без\s+обратн[а-яё]*\s+билет)`)

func standaloneSearch(text string) bool {
	return explicitRestart.MatchString(text) || (searchStart.MatchString(text) && routeInRequest.MatchString(text))
}
func clearReturn(p *searchPlan) {
	p.RoundTrip = false
	p.Return = ""
	p.ReturnOrigin = ""
	p.MinDays = 0
	p.MaxDays = 0
}
func applyTripPolicy(p *searchPlan, text string, newSearch bool, previous searchPlan) {
	if p.Action != "search" {
		return
	}
	if oneWayText.MatchString(text) || denyReturn.MatchString(text) {
		clearReturn(p)
		return
	}
	if returnMention.MatchString(text) || stayMention.MatchString(text) {
		p.RoundTrip = true
		return
	}
	if newSearch {
		clearReturn(p)
	} else if previous.Action == "search" {
		p.RoundTrip = previous.RoundTrip
		if !p.RoundTrip {
			clearReturn(p)
		}
	}
}
