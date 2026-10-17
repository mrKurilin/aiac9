package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCountryJourneyPairsArrivalAndExitWithoutInternalFlights(t *testing.T) {
	catalog := cities()
	catalog["MMK"] = city{Code: "MMK", Name: "Мурманск", Country: "RU"}
	catalog["TYO"] = city{Code: "TYO", Name: "Токио", Country: "JP"}
	catalog["OSA"] = city{Code: "OSA", Name: "Осака", Country: "JP"}
	var routes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		route := q.Get("origin") + "-" + q.Get("destination")
		routes = append(routes, route)
		if q.Get("one_way") != "true" {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": []offer{}})
			return
		}
		var list []offer
		switch route {
		case "MMK-JP":
			list = []offer{{Origin: "MMK", Destination: "TYO", Departure: "2026-11-10", Price: 50000, Link: "/search/outbound"}, {Origin: "MMK", Destination: "MOW", Departure: "2026-11-10", Price: 1000, Link: "/search/wrong-country"}}
		case "JP-MMK":
			list = []offer{{Origin: "OSA", Destination: "MMK", Departure: "2026-11-20", Price: 40000, Link: "/search/inbound"}, {Origin: "TYO", Destination: "MMK", Departure: "2026-11-12", Price: 10000, Link: "/search/too-soon"}}
		default:
			t.Errorf("unexpected/internal flight request: %s", route)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": list})
	}))
	defer server.Close()
	c := &travelClient{base: server.URL, http: server.Client(), cities: catalog}
	p := searchPlan{Action: "search", Origin: "MMK", DestinationCountry: "JP", Departure: "2026-11", RoundTrip: true, MinDays: 7, MaxDays: 14, Budget: 100000}
	offers, err := c.searchJourney(context.Background(), p, testNow(), func(string) {})
	if err != nil || len(offers) != 1 {
		t.Fatalf("offers=%+v err=%v routes=%v", offers, err, routes)
	}
	o := offers[0]
	if o.Price != 90000 || o.ReturnOrigin != "OSA" || o.ReturnLink != "/search/inbound" {
		t.Fatalf("incorrect combination %+v", o)
	}
	answer := c.render(p, offers, testNow())
	for _, part := range []string{"Мурманск → Токио / Осака → Мурманск", "всего 90000 ₽", "50000 ₽", "40000 ₽", "https://www.aviasales.ru/search/outbound", "https://www.aviasales.ru/search/inbound", "не включён в цену"} {
		if !strings.Contains(answer, part) {
			t.Errorf("missing %q", part)
		}
	}
	p.Budget = 80000
	offers, err = c.searchJourney(context.Background(), p, testNow(), func(string) {})
	if err != nil || len(offers) != 0 {
		t.Fatal("total journey budget ignored")
	}
}

func TestExplicitJourneyUsesOnlyRequestedArrivalAndExitCities(t *testing.T) {
	catalog := cities()
	catalog["TYO"] = city{Code: "TYO", Name: "Токио", Country: "JP"}
	catalog["OSA"] = city{Code: "OSA", Name: "Осака", Country: "JP"}
	var routes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		route := q.Get("origin") + "-" + q.Get("destination")
		routes = append(routes, route)
		var o offer
		switch route {
		case "KZN-TYO":
			o = offer{Origin: "KZN", Destination: "TYO", Departure: "2026-11-10", Price: 30000, Link: "/out"}
		case "OSA-KZN":
			o = offer{Origin: "OSA", Destination: "KZN", Departure: "2026-11-20", Price: 35000, Link: "/back"}
		default:
			t.Errorf("unexpected flight: %s", route)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": []offer{o}})
	}))
	defer server.Close()
	p := searchPlan{Action: "search", Origin: "Казань", Destination: "Токио", ReturnOrigin: "Осака", Departure: "2026-11-10", RoundTrip: true, MinDays: 10, MaxDays: 10}
	client := &scriptedPlanner{plans: []searchPlan{p}}
	a := &agent{client: client, travel: &travelClient{base: server.URL, http: server.Client(), cities: catalog}, now: testNow}
	answer, err := a.answer(context.Background(), "Казань — Токио, обратно из Осаки через 10 дней. Внутренние перелёты не нужны.", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(routes, ",") != "KZN-TYO,OSA-KZN" || !strings.Contains(answer, "65000 ₽") {
		t.Fatalf("routes=%v answer=%s", routes, answer)
	}
}

// Reproduce the log: outbound tickets exist, independent return tickets do not,
// but the cache contains a complete round trip for November 8–19.
func TestCountryRoundTripSurvivesEmptyReverseCache(t *testing.T) {
	for _, source := range []string{"country", "city", "country_without_outbound", "optional_http_error", "mixed"} {
		t.Run(source, func(t *testing.T) {
			catalog := cities()
			catalog["MMK"] = city{Code: "MMK", Name: "Мурманск", Country: "RU"}
			catalog["TYO"] = city{Code: "TYO", Name: "Токио", Country: "JP"}
			var readyQueries, reverseQueries int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				var list []offer
				if q.Get("origin") == "MMK" {
					if q.Get("one_way") == "false" {
						readyQueries++
						if q.Get("departure_at") == "2026-11" && ((source == "city" && q.Get("destination") == "TYO") || (source != "city" && q.Get("destination") == "JP")) {
							list = []offer{
								{Origin: "MMK", Destination: "TYO", Departure: "2026-11-08", Return: "2026-11-19", Price: 99130, Link: "/round-trip"},
								{Origin: "MMK", Destination: "TYO", Departure: "2026-11-01", Return: "2026-11-07", Price: 80000, Link: "/too-short"},
							}
						}
					} else if source == "optional_http_error" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					} else if source != "country_without_outbound" {
						list = []offer{{Origin: "MMK", Destination: "TYO", Departure: "2026-11-08", Price: 40000, Link: "/one-way"}}
					}
				} else {
					reverseQueries++
					if source == "mixed" {
						list = []offer{{Origin: "TYO", Destination: "MMK", Departure: "2026-11-19", Price: 50000, Link: "/back"}}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": list})
			}))
			defer server.Close()
			p := searchPlan{Action: "search", Origin: "MMK", DestinationCountry: "JP", Departure: "2026-11", RoundTrip: true, MinDays: 7, MaxDays: 14}
			a := &agent{client: &scriptedPlanner{plans: []searchPlan{p}}, travel: &travelClient{base: server.URL, http: server.Client(), cities: catalog}, now: testNow}
			answer, err := a.answer(context.Background(), "Из Мурманска в Японию в ноябре туда-обратно на 7–14 дней", func(string) {})
			if err != nil || !strings.Contains(answer, "99130 ₽") || !strings.Contains(answer, "Готовый билет туда-обратно") || !strings.Contains(answer, "https://www.aviasales.ru/round-trip") {
				t.Fatalf("err=%v answer=%s", err, answer)
			}
			if strings.Contains(answer, "80000 ₽") || readyQueries == 0 {
				t.Fatalf("stay filter or round-trip search failed: %s", answer)
			}
			if source == "city" && reverseQueries == 0 {
				t.Fatal("independent reverse search was not exercised")
			}
			if source == "mixed" && (!strings.Contains(answer, "всего 90000 ₽") || strings.Index(answer, "90000 ₽") > strings.Index(answer, "99130 ₽") || strings.Count(answer, "https://www.aviasales.ru/round-trip") != 1) {
				t.Fatalf("mixed tickets must be sorted by total price and deduplicated: %s", answer)
			}
		})
	}
}
