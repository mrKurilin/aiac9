package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewFullRouteDoesNotInheritReturnTicketOrStay(t *testing.T) {
	catalog := cities()
	catalog["MMK"] = city{Code: "MMK", Name: "Мурманск", Country: "RU"}
	catalog["ULY"] = city{Code: "ULY", Name: "Ульяновск", Country: "RU"}
	catalog["TYO"] = city{Code: "TYO", Name: "Токио", Country: "JP"}
	previous := searchPlan{Action: "search", Origin: "MMK", Destination: "TYO", DestinationCountry: "JP", RoundTrip: true, MinDays: 7, MaxDays: 14, Budget: 100000, Direct: true}
	stale := searchPlan{Action: "search", Origin: "Мурманск", Destination: "Ульяновск", Departure: "2026-10-17", DepartureEnd: "2026-12-16", RoundTrip: true, MinDays: 7, MaxDays: 14, ReturnOrigin: "Токио", Return: "2026-12"}
	planner := &scriptedPlanner{plans: []searchPlan{stale}}
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("one_way") != "true" || r.URL.Query().Get("return_at") != "" {
			t.Error("stale return ticket requested")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": []offer{{Origin: "MMK", Destination: "ULY", Departure: "2026-11-01", Price: 9000, Link: "/oneway"}}})
	}))
	defer server.Close()
	a := &agent{client: planner, travel: &travelClient{base: server.URL, http: server.Client(), cities: catalog}, now: testNow, conditions: previous, history: []message{{Role: "user", Content: "В Японию на 1–2 недели"}, {Role: "assistant", Content: "Предыдущая поездка"}}}
	answer, err := a.answer(context.Background(), "найди самый дешевый билет мурманск - ульяновск в ближайшие 2 месяца", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "в одну сторону") || !strings.Contains(answer, "9000 ₽") || strings.Contains(answer, "Обратно:") || strings.Contains(answer, "длительность:") {
		t.Fatal("new search inherited old trip requirements")
	}
	if len(planner.seen[0]) != 2 || strings.Contains(planner.seen[0][0].Content, "Предыдущие условия поиска (данные") {
		t.Fatal("old context sent for new route")
	}
	if a.conditions.RoundTrip || a.conditions.ReturnOrigin != "" || a.conditions.MinDays != 0 || a.conditions.MaxDays != 0 || len(a.history) != 2 || calls == 0 {
		t.Fatal("new search state not cleared")
	}
}
func TestTripTypeDefaultsAndRefinements(t *testing.T) {
	cases := []struct {
		text              string
		newSearch         bool
		previousRoundTrip bool
		wantRoundTrip     bool
	}{
		{"найди билет мурманск - ульяновск в ближайшие 2 месяца", true, true, false},
		{"найди билет мурманск - ульяновск туда-обратно на 2 недели", true, false, true},
		{"в ближайшие 52 дня", false, true, true},
		{"в ближайшие 52 дня", false, false, false},
		{"нет, в одну сторону", false, true, false},
		{"не туда-обратно", false, true, false},
		{"туда-обратно, там быть две недели", false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			p := searchPlan{Action: "search", RoundTrip: true, Return: "2026-12", ReturnOrigin: "TYO", MinDays: 7, MaxDays: 14}
			applyTripPolicy(&p, tc.text, tc.newSearch, searchPlan{Action: "search", RoundTrip: tc.previousRoundTrip})
			if p.RoundTrip != tc.wantRoundTrip {
				t.Fatal("incorrect trip type")
			}
			if !p.RoundTrip && (p.Return != "" || p.ReturnOrigin != "" || p.MinDays != 0 || p.MaxDays != 0) {
				t.Fatal("one-way retained return constraints")
			}
		})
	}
	if standaloneSearch("найди подешевле") || standaloneSearch("в ближайшие полгода") {
		t.Fatal("refinement treated as new route")
	}
	if !standaloneSearch("найди билет из Мурманска в Ульяновск") {
		t.Fatal("full route not recognized")
	}
}
