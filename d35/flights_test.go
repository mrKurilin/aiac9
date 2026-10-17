package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testNow() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.FixedZone("MSK", 10800)) }
func cities() map[string]city {
	return map[string]city{"KZN": {Code: "KZN", Name: "Казань", Country: "RU"}, "AER": {Code: "AER", Name: "Сочи", Country: "RU"}, "MOW": {Code: "MOW", Name: "Москва", Country: "RU"}}
}
func plan() searchPlan {
	return searchPlan{Action: "search", Origin: "KZN", Destination: "AER", Departure: "2026-11", RoundTrip: true, MinDays: 5, MaxDays: 7, Budget: 20000, Direct: true}
}
func ticket() offer {
	return offer{Origin: "KZN", Destination: "AER", Departure: "2026-11-10T09:00:00+03:00", Return: "2026-11-16T10:00:00+03:00", Price: 15000, Airline: "SU", Link: "/search/KZN1011AER16111"}
}
func TestFiltersRejectRelaxedDatesForeignRoutesAndInvalidLinks(t *testing.T) {
	c := &travelClient{cities: cities()}
	p := plan()
	o := ticket()
	if !c.accepts(p, o, testNow()) {
		t.Fatal("valid ticket rejected")
	}
	cases := map[string]func(*offer){
		"budget": func(o *offer) { o.Price = 21000 }, "outbound date": func(o *offer) { o.Departure = "2026-12-10T09:00:00+03:00" },
		"stay": func(o *offer) { o.Return = "2026-11-12" }, "missing return": func(o *offer) { o.Return = "" }, "wrong origin": func(o *offer) { o.Origin = "MOW" },
		"international": func(o *offer) { o.Destination = "DXB" }, "wrong destination": func(o *offer) { o.Destination = "MOW" },
		"outbound transfer": func(o *offer) { o.Transfers = 1 }, "return transfer": func(o *offer) { o.ReturnTransfers = 1 },
		"redirect link": func(o *offer) { o.Link = "//other.example/ticket" }, "absolute foreign link": func(o *offer) { o.Link = "https://other.example/ticket" },
		"bad return date": func(o *offer) { o.Return = "2026-11-99" }, "no price": func(o *offer) { o.Price = 0 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v := o
			change(&v)
			if c.accepts(p, v, testNow()) {
				t.Fatal("invalid offer accepted")
			}
		})
	}
	p.Return = "2026-11-17"
	if c.accepts(p, o, testNow()) {
		t.Fatal("wrong exact return accepted")
	}
	p = plan()
	p.RoundTrip = false
	p.MinDays = 0
	p.MaxDays = 0
	if c.accepts(p, o, testNow()) {
		t.Fatal("round trip accepted as one way")
	}
	o.Return = ""
	if !c.accepts(p, o, testNow()) {
		t.Fatal("one way rejected")
	}
	p = plan()
	p.Departure = "2026-10"
	o = ticket()
	o.Departure = "2026-10-15"
	if c.accepts(p, o, testNow()) {
		t.Fatal("past flight accepted")
	}
}
func TestValidateSearch(t *testing.T) {
	c := &travelClient{cities: cities()}
	for _, mutate := range []func(*searchPlan){func(p *searchPlan) { p.Origin = "DXB" }, func(p *searchPlan) { p.Departure = "2020-01" }, func(p *searchPlan) { p.Departure = "2026-13" }, func(p *searchPlan) { p.MinDays = 10 }, func(p *searchPlan) { p.RoundTrip = false }, func(p *searchPlan) { p.Return = "2026-10-01" }, func(p *searchPlan) { p.MinDays = 0; p.MaxDays = 0 }} {
		p := plan()
		mutate(&p)
		if c.validate(p, testNow()) == nil {
			t.Fatal("invalid plan accepted")
		}
	}
	if err := c.validate(plan(), testNow()); err != nil {
		t.Fatal(err)
	}
}
func TestProviderPaginationAndDeterministicRendering(t *testing.T) {
	var pages int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Access-Token") != "placeholder" || r.URL.Query().Get("token") != "" {
			t.Error("credential transport")
		}
		q := r.URL.Query()
		if q.Get("one_way") != "false" || q.Get("direct") != "true" || q.Get("currency") != "rub" || q.Get("departure_at") != "2026-11" {
			t.Error("wrong query")
		}
		pages++
		list := []offer{ticket()}
		if pages == 1 {
			list = make([]offer, 100)
			for i := range list {
				list[i] = ticket()
				list[i].Price = 19000
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": list})
	}))
	defer server.Close()
	c := &travelClient{key: "placeholder", base: server.URL, http: server.Client(), cities: cities()}
	offers, err := c.search(context.Background(), plan(), testNow(), func(string) {})
	if err != nil || len(offers) != 2 || offers[0].Price != 15000 || pages != 2 {
		t.Fatalf("offers=%v pages=%d err=%v", offers, pages, err)
	}
	text := c.render(plan(), offers, testNow())
	for _, part := range []string{"Казань → Сочи", "15000 ₽", "https://www.aviasales.ru/search/", "кэша", "не полный поиск"} {
		if !strings.Contains(text, part) {
			t.Errorf("missing %q", part)
		}
	}
}

type scriptedPlanner struct {
	mu    sync.Mutex
	seen  [][]message
	plans []searchPlan
	block <-chan struct{}
}

func (p *scriptedPlanner) Plan(ctx context.Context, m []message) (searchPlan, error) {
	p.mu.Lock()
	p.seen = append(p.seen, append([]message(nil), m...))
	i := len(p.seen) - 1
	p.mu.Unlock()
	if p.block != nil {
		select {
		case <-ctx.Done():
			return searchPlan{}, ctx.Err()
		case <-p.block:
		}
	}
	if len(p.plans) == 0 {
		return searchPlan{Action: "ask", Question: "Укажите даты"}, nil
	}
	return p.plans[min(i, len(p.plans)-1)], nil
}
func TestAgentClarifiesKeepsContextAndResets(t *testing.T) {
	client := &scriptedPlanner{plans: []searchPlan{{Action: "ask", Question: "Откуда вылет?"}, {Action: "ask", Question: "Когда?"}}}
	a := &agent{client: client, travel: &travelClient{cities: cities()}, now: testNow}
	for i := 0; i < 2; i++ {
		if _, err := a.answer(context.Background(), "Хочу в Сочи", func(string) {}); err != nil {
			t.Fatal(err)
		}
	}
	if len(client.seen[1]) != 4 {
		t.Fatal("multi-turn history lost")
	}
	a.reset()
	if _, err := a.answer(context.Background(), "Другая поездка", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if len(client.seen[2]) != 2 {
		t.Fatal("history not cleared")
	}
}
func TestDeepSeekHTTPAndErrorsNeverExposeBodies(t *testing.T) {
	for _, status := range []int{200, 401, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if r.Header.Get("Authorization") != "Bearer placeholder" || body["response_format"] == nil {
					t.Error("bad request")
				}
				w.WriteHeader(status)
				if status == 200 {
					_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"action\":\"ask\",\"question\":\"Когда?\"}"}}]}`)
				} else {
					_, _ = io.WriteString(w, "SENSITIVE_RESPONSE_MUST_NOT_ESCAPE")
				}
			}))
			defer server.Close()
			c := deepSeekClient{key: "placeholder", url: server.URL, model: "deepseek-chat", http: server.Client()}
			p, err := c.Plan(context.Background(), []message{{Role: "user", Content: "Поездка"}})
			if status == 200 {
				if err != nil || p.Question != "Когда?" {
					t.Fatalf("%+v %v", p, err)
				}
			} else if err == nil || strings.Contains(err.Error(), "SENSITIVE") {
				t.Fatal("bad safe error")
			}
		})
	}
}
func TestTerminalResetAliasesAndCommandMetadata(t *testing.T) {
	client := &scriptedPlanner{}
	a := &agent{client: client, travel: &travelClient{cities: cities()}, now: testNow}
	var out bytes.Buffer
	if err := run(context.Background(), a, strings.NewReader("первый\nвторой\n/reset\nтретий\n/clear\nчетвёртый\n/exit\n"), &out); err != nil {
		t.Fatal(err)
	}
	if len(client.seen) != 4 || len(client.seen[1]) != 4 || len(client.seen[2]) != 2 || len(client.seen[3]) != 2 {
		t.Fatal("reset aliases failed")
	}
	if strings.Count(out.String(), "История очищена") != 2 {
		t.Fatal("visible reset failed")
	}
	for _, cmd := range []string{"/help", "/reset", "/clear", "/exit", "/quit"} {
		found := false
		for _, v := range commands {
			found = found || v.Value == cmd
		}
		if !found {
			t.Fatal("missing completion", cmd)
		}
	}
}
func TestRealEntrypointWithFakeAPIs(t *testing.T) {
	var mu sync.Mutex
	var calls [][]message
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `[{"code":"KZN","name":"Казань","country_code":"RU"},{"code":"AER","name":"Сочи","country_code":"RU"}]`)
			return
		}
		var request struct {
			Messages []message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		mu.Lock()
		calls = append(calls, request.Messages)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"action\":\"ask\",\"question\":\"Когда?\"}"}}]}`)
	}))
	defer server.Close()
	binary := filepath.Join(t.TempDir(), "mrkai")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	cmd := exec.Command(binary)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=placeholder", "TRAVELPAYOUTS_TOKEN=placeholder", "DEEPSEEK_BASE_URL="+server.URL, "TRAVELPAYOUTS_BASE_URL="+server.URL)
	cmd.Stdin = strings.NewReader("первый\n/reset\nвторой\n/clear\nтретий\n/exit\n")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatal("missing model calls")
	}
	for _, m := range calls {
		if len(m) != 2 {
			t.Fatal("reset failed")
		}
	}
}

func TestMurmanskUlyanovskClarificationRepairsCityAndSearchesTwoWeekRange(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.FixedZone("MSK", 10800)) }
	var months []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		months = append(months, q.Get("departure_at"))
		if q.Get("origin") != "MMK" || q.Get("destination") != "ULY" || q.Get("one_way") != "false" {
			t.Error("wrong normalized route")
		}
		o := offer{Origin: "MMK", Destination: "ULY", Price: 10000, Departure: "2026-10-28", Return: "2026-11-04", Link: "/search/example"}
		if q.Get("departure_at") == "2026-11" {
			o.Price = 8000
			o.Departure = "2026-11-03"
			o.Return = "2026-11-07"
		}
		outside := o
		outside.Price = 1000
		outside.Departure = "2026-11-12"
		outside.Return = "2026-11-19"
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": []offer{o, outside}})
	}))
	defer server.Close()
	c := &travelClient{base: server.URL, http: server.Client(), cities: map[string]city{"MMK": {Code: "MMK", Name: "Мурманск", Country: "RU"}, "ULY": {Code: "ULY", Name: "Ульяновск", Country: "RU"}}}
	first := searchPlan{Action: "ask", Question: "Нужен билет в одну сторону или туда-обратно?", Origin: "Мурманск", Destination: "Ульяновск", Departure: "2026-10-25", DepartureEnd: "2026-11-07"}
	wrong := first
	wrong.Action = "search"
	wrong.Destination = "ULV"
	wrong.RoundTrip = true
	wrong.MinDays = 4
	wrong.MaxDays = 10
	fixed := wrong
	fixed.Destination = "Ульяновск"
	p := &scriptedPlanner{plans: []searchPlan{first, wrong, fixed}}
	a := &agent{client: p, travel: c, now: now}
	if _, err := a.answer(context.Background(), "найди самый дешевый билет в ближайшие 2 недели из Мурманска в Ульяновск", func(string) {}); err != nil {
		t.Fatal(err)
	}
	answer, err := a.answer(context.Background(), "туда-обратно, длительность около недели +- 3 дня", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(months, ",") != "2026-10,2026-11" {
		t.Fatal("interval narrowed", months)
	}
	for _, part := range []string{"Мурманск → Ульяновск", "2026-10-25 — 2026-11-07 включительно", "8000 ₽", "от 4 дней до 10 дней", "https://www.aviasales.ru/"} {
		if !strings.Contains(answer, part) {
			t.Errorf("missing %q", part)
		}
	}
	if strings.Contains(answer, "1000 ₽") {
		t.Fatal("out-of-range ticket displayed")
	}
	if len(p.seen) != 3 || !strings.Contains(p.seen[2][0].Content, "Ульяновск = ULY") {
		t.Fatal("city resolution not grounded in catalog")
	}
	if !strings.Contains(p.seen[1][2].Content, first.Question) {
		t.Fatal("actual clarification missing from history")
	}
}

func TestValidationErrorIsAvailableWhenUserAsksWhy(t *testing.T) {
	bad := plan()
	bad.Destination = "UNKNOWN"
	p := &scriptedPlanner{plans: []searchPlan{bad, bad, {Action: "ask", Question: "Ошибка была в сопоставлении города, а не в длительности поездки."}}}
	a := &agent{client: p, travel: &travelClient{cities: cities()}, now: testNow}
	answer, err := a.answer(context.Background(), "поиск", func(string) {})
	if err != nil || !strings.Contains(answer, "UNKNOWN") {
		t.Fatalf("%q %v", answer, err)
	}
	if _, err := a.answer(context.Background(), "зачем известный город назначения", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if len(p.seen) != 3 || !strings.Contains(p.seen[2][2].Content, "не удалось сопоставить город назначения") {
		t.Fatal("model cannot see actual program error")
	}
}

func TestDepartureIntervalBoundsAndPagingBudget(t *testing.T) {
	p := plan()
	p.Departure = "2026-10-25"
	p.DepartureEnd = "2026-11-07"
	c := &travelClient{cities: cities()}
	if err := c.validate(p, testNow()); err != nil {
		t.Fatal(err)
	}
	for _, end := range []string{"2026-10-24", "2029-01-01", "2026-13-01"} {
		bad := p
		bad.DepartureEnd = end
		if c.validate(bad, testNow()) == nil {
			t.Fatal("invalid interval accepted", end)
		}
	}
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("departure_at")+":"+r.URL.Query().Get("page"))
		list := make([]offer, 100)
		for i := range list {
			list[i] = ticket()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": list})
	}))
	defer server.Close()
	c.base = server.URL
	c.http = server.Client()
	if _, err := c.search(context.Background(), p, testNow(), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(queries, ",") != "2026-10:1,2026-11:1,2026-10:2" {
		t.Fatal("months skipped or budget exceeded", queries)
	}
}

func TestInternationalSearchLoadsWorldCitiesAndReturnsTokyoLinks(t *testing.T) {
	var searchCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/data/ru/cities.json" {
			_, _ = io.WriteString(w, `[{"code":"MMK","name":"Мурманск","country_code":"RU"},{"code":"TYO","name":"Токио","country_code":"JP"},{"code":"NYC","name":"Нью-Йорк","country_code":"US"}]`)
			return
		}
		searchCalls++
		if r.Header.Get("X-Access-Token") != "placeholder" || r.URL.Query().Get("origin") != "MMK" || r.URL.Query().Get("destination") != "TYO" {
			t.Error("incorrect international API request")
		}
		valid := offer{Origin: "MMK", Destination: "TYO", Departure: "2026-10-20", Price: 54000, Transfers: 2, Link: "/search/MMK2010TYO1"}
		wrong := valid
		wrong.Destination = "NYC"
		wrong.Price = 10000
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": []offer{wrong, valid}})
	}))
	defer server.Close()
	c := &travelClient{key: "placeholder", base: server.URL, http: server.Client()}
	if err := c.loadCities(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(c.cities) != 3 {
		t.Fatal("foreign cities were discarded")
	}
	p := &scriptedPlanner{plans: []searchPlan{{Action: "search", Origin: "Мурманск", Destination: "Токио", Departure: "2026-10-17", DepartureEnd: "2026-10-30"}}}
	a := &agent{client: p, travel: c, now: testNow}
	answer, err := a.answer(context.Background(), "Самый дешёвый из Мурманска в Токио в ближайшие две недели, в одну сторону", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"Мурманск → Токио", "54000 ₽", "Пересадки: 2", "https://www.aviasales.ru/search/MMK2010TYO1"} {
		if !strings.Contains(answer, part) {
			t.Errorf("missing %q", part)
		}
	}
	if strings.Contains(answer, "10000 ₽") || searchCalls != 1 {
		t.Fatal("incorrect filtering or request count")
	}
	prompt := p.seen[0][0].Content
	if !strings.Contains(prompt, "Токио = TYO (JP)") || strings.Contains(prompt, "Нью-Йорк = NYC") {
		t.Fatal("catalog candidates not scoped to conversation")
	}
	if strings.Contains(prompt, "международных рейсов,") {
		t.Fatal("international travel still prohibited")
	}
}

func TestCountryConstraintPreservesDomesticSearchAndAllowsWorldSearch(t *testing.T) {
	catalog := cities()
	catalog["TYO"] = city{Code: "TYO", Name: "Токио", Country: "JP"}
	c := &travelClient{cities: catalog}
	p := plan()
	p.Destination = ""
	p.DestinationCountry = "RU"
	foreign := ticket()
	foreign.Destination = "TYO"
	if c.accepts(p, foreign, testNow()) {
		t.Fatal("domestic request expanded to international")
	}
	if !c.accepts(p, ticket(), testNow()) {
		t.Fatal("domestic offer rejected")
	}
	p.DestinationCountry = ""
	if !c.accepts(p, foreign, testNow()) {
		t.Fatal("worldwide request rejected")
	}
	p.DestinationCountry = "JP"
	if !c.accepts(p, foreign, testNow()) || c.accepts(p, ticket(), testNow()) {
		t.Fatal("country filter ignored")
	}
	p.Destination = "AER"
	if c.validate(p, testNow()) == nil {
		t.Fatal("city and country conflict ignored")
	}
}

func TestWorldCatalogBoundsPromptAndPreservesAmbiguity(t *testing.T) {
	catalog := map[string]city{"TYO": {Code: "TYO", Name: "Токио", Country: "JP"}, "PAR": {Code: "PAR", Name: "Париж", Country: "FR"}, "PRX": {Code: "PRX", Name: "Париж", Country: "US"}}
	for i := 0; i < 1000; i++ {
		code := fmt.Sprintf("X%d", i)
		catalog[code] = city{Code: code, Name: fmt.Sprintf("Город %d", i), Country: "RU"}
	}
	c := &travelClient{cities: catalog}
	candidates := c.cityCatalogFor("Токио или Париж, город отправления пока неизвестен")
	if len(strings.Split(candidates, "\n")) > 64 || !strings.Contains(candidates, "Токио = TYO (JP)") || !strings.Contains(candidates, "Париж = PAR (FR)") || !strings.Contains(candidates, "Париж = PRX (US)") {
		t.Fatal("unbounded catalog or missing precise matches")
	}
	if c.resolveCity("Париж") != "Париж" {
		t.Fatal("ambiguous city silently resolved")
	}
	if c.resolveCity("tyo") != "TYO" {
		t.Fatal("international IATA code not resolved")
	}
}

func TestPlanDecoderAcceptsLegacyEnvelopeAndRecoversInvalidTypes(t *testing.T) {
	cases := []string{
		`{"action":"ask","question":"Когда?"}`,
		`{"conditions":{"action":"ask","question":"Когда?"},"reply":"предыдущее сообщение"}`,
		"```json\n{\"action\":\"ask\",\"question\":\"Когда?\"}\n```",
	}
	for _, content := range cases {
		p, err := decodePlan(content)
		if err != nil || p.Question != "Когда?" {
			t.Fatalf("%+v %v", p, err)
		}
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Messages []message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		content := `{"action":"search","round_trip":"yes","min_days":"7"}`
		if calls == 2 {
			if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "поля или типы") {
				t.Error("repair instruction missing")
			}
			content = `{"action":"search","origin":"Мурманск","destination":"Токио","departure":"2026-10-17","departure_end":"2026-12-17","round_trip":true,"min_days":7,"max_days":7}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	client := deepSeekClient{key: "placeholder", url: server.URL, http: server.Client()}
	p, err := client.Plan(context.Background(), []message{{Role: "user", Content: "в ближайшие 62 дня"}})
	if err != nil || calls != 2 || p.MinDays != 7 || !p.RoundTrip {
		t.Fatalf("%+v calls=%d %v", p, calls, err)
	}
}

func TestMalformedPlanStopsAfterTwoAttemptsWithoutLeakingBody(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": "SENSITIVE_RESPONSE_MUST_NOT_ESCAPE"}}}})
	}))
	defer server.Close()
	client := deepSeekClient{key: "placeholder", url: server.URL, http: server.Client()}
	_, err := client.Plan(context.Background(), []message{{Role: "user", Content: "в ближайшие 62 дня"}})
	if err == nil || calls != 2 || strings.Contains(err.Error(), "SENSITIVE") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestHalfYearSearchQueriesEveryMonthAndRetainsTypedConditions(t *testing.T) {
	var months []string
	catalog := cities()
	catalog["MMK"] = city{Code: "MMK", Name: "Мурманск", Country: "RU"}
	catalog["TYO"] = city{Code: "TYO", Name: "Токио", Country: "JP"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		month := r.URL.Query().Get("departure_at")
		months = append(months, month)
		var list []offer
		if month == "2027-04" {
			list = []offer{{Origin: "MMK", Destination: "TYO", Departure: "2027-04-10", Return: "2027-04-17", Price: 55000, Transfers: 2, Link: "/search/example"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": list})
	}))
	defer server.Close()
	first := searchPlan{Action: "search", Origin: "MMK", Destination: "TYO", Departure: "2026-10-17", DepartureEnd: "2026-11-16", RoundTrip: true, MinDays: 7, MaxDays: 7}
	wide := first
	wide.DepartureEnd = "2027-04-16"
	client := &scriptedPlanner{plans: []searchPlan{first, wide}}
	a := &agent{client: client, travel: &travelClient{base: server.URL, http: server.Client(), cities: catalog}, now: testNow}
	if _, err := a.answer(context.Background(), "Мурманск — Токио, месяц, туда обратно на неделю", func(string) {}); err != nil {
		t.Fatal(err)
	}
	months = nil
	answer, err := a.answer(context.Background(), "в ближайшие полгода", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(months, ",") != "2026-10,2026-11,2026-12,2027-01,2027-02,2027-03,2027-04" {
		t.Fatal("months skipped", months)
	}
	if !strings.Contains(answer, "55000 ₽") || !strings.Contains(answer, "700 записей") {
		t.Fatal("last month missing or misleading coverage")
	}
	messages := client.seen[1]
	if !strings.Contains(messages[0].Content, `"destination":"TYO"`) || !strings.Contains(messages[0].Content, `"min_days":7`) {
		t.Fatal("previous structured conditions lost")
	}
	if strings.HasPrefix(messages[2].Content, "{") {
		t.Fatal("assistant history still masquerades as a plan schema")
	}
	a.reset()
	if a.conditions.Action != "" {
		t.Fatal("reset retained typed conditions")
	}
}

func TestMalformedModelReplyStillSearchesAppliedConditions(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": "unusable output"}}}})
	}))
	defer model.Close()
	var queried []string
	travel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queried = append(queried, r.URL.Query().Get("departure_at"))
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": []offer{ticket()}})
	}))
	defer travel.Close()
	a := &agent{client: deepSeekClient{url: model.URL, http: model.Client(), key: "placeholder"}, travel: &travelClient{base: travel.URL, http: travel.Client(), cities: cities()}, now: testNow, conditions: plan()}
	answer, err := a.answer(context.Background(), "в ближайшие 62 дня", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"Не все уточнения удалось распознать", "Казань → Сочи", "2026-10-16 — 2026-12-16", "15000 ₽", "от 5 дней до 7 дней"} {
		if !strings.Contains(answer, part) {
			t.Errorf("missing %q", part)
		}
	}
	if strings.Contains(answer, "формат") || strings.Join(queried, ",") != "2026-10,2026-11,2026-12" {
		t.Fatal("fallback failed", queried)
	}
	if strings.Contains(a.history[len(a.history)-1].Content, "https://") {
		t.Fatal("provider data leaked into model history")
	}
}

func TestPartialPlanRecoveryUsesRecognizedFieldsAndPreservesConstraints(t *testing.T) {
	fields := recoverPlanFields(`{"conditions":{"action":"search","origin":"Казань","departure":"2026-11","budget_rub":"18000","direct":"true","min_days":"bad","unknown":"ignored"},"reply":"metadata"}`)
	p := fallbackPlan(plan(), fields, "покажи варианты", testNow())
	c := &travelClient{cities: cities()}
	c.resolvePlan(&p)
	if p.Origin != "KZN" || p.Destination != "AER" || p.Budget != 18000 || !p.Direct || p.MinDays != 5 || p.MaxDays != 7 {
		t.Fatalf("recognized/lost fields: %+v", p)
	}
	if err := c.validate(p, testNow()); err != nil {
		t.Fatal(err)
	}
	empty := fallbackPlan(searchPlan{}, nil, "в ближайшие 52 дня", testNow())
	if empty.Action != "ask" || empty.DepartureEnd != "2026-12-06" || empty.RoundTrip {
		t.Fatalf("missing essentials invented: %+v", empty)
	}
	refined := fallbackPlan(searchPlan{Origin: "KZN", Destination: "AER"}, nil, "туда обратно, вылет в ближайший месяц, там быть неделю", testNow())
	if !refined.RoundTrip || refined.MinDays != 7 || refined.MaxDays != 7 || refined.DepartureEnd != "2026-11-15" {
		t.Fatalf("explicit refinements lost: %+v", refined)
	}
}

func TestRejectionDiagnosticsExplainWhyCacheOffersWereExcluded(t *testing.T) {
	p := plan()
	p.MinDays = 14
	p.MaxDays = 14
	short := ticket() // six calendar days instead of fourteen
	oneWay := ticket()
	oneWay.Return = ""
	outside := ticket()
	outside.Departure = "2026-12-10"
	invalidLink := ticket()
	invalidLink.Return = "2026-11-24"
	invalidLink.Link = ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": []offer{short, oneWay, outside, invalidLink}})
	}))
	defer server.Close()
	c := &travelClient{base: server.URL, http: server.Client(), cities: cities()}
	var diagnostics []string
	offers, err := c.search(context.Background(), p, testNow(), func(line string) { diagnostics = append(diagnostics, line) })
	if err != nil || len(offers) != 0 {
		t.Fatalf("%v %v", offers, err)
	}
	joined := strings.Join(diagnostics, "\n")
	for _, reason := range []string{"получено 4 записей, подошло 0", "длительность 6 дн. вместо 14–14 — 1", "нет обратного билета — 1", "вылет вне заданного диапазона — 1", "нет допустимой ссылки — 1"} {
		if !strings.Contains(joined, reason) {
			t.Errorf("missing %q in %s", reason, joined)
		}
	}
}
