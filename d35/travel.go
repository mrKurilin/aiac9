package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type city struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Country string `json:"country_code"`
}
type offer struct {
	Origin          string `json:"origin"`
	Destination     string `json:"destination"`
	Price           int    `json:"price"`
	Departure       string `json:"departure_at"`
	Return          string `json:"return_at"`
	Transfers       int    `json:"transfers"`
	ReturnTransfers int    `json:"return_transfers"`
	Airline         string `json:"airline"`
	Duration        int    `json:"duration"`
	Link            string `json:"link"`
	ReturnOrigin    string `json:"return_origin,omitempty"`
	ReturnLink      string `json:"return_link,omitempty"`
	OutboundPrice   int    `json:"-"`
	InboundPrice    int    `json:"-"`
}
type travelClient struct {
	key, base string
	http      *http.Client
	cities    map[string]city
	trace     *traceLog
}

func (c *travelClient) get(ctx context.Context, path string, q url.Values, out any, authenticated bool) error {
	callID := c.trace.callID()
	started := time.Now()
	c.trace.event(ctx, "travel.request", map[string]any{"call_id": callID, "method": "GET", "url": c.base + path, "path": path, "query": q})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	if err != nil {
		c.trace.event(ctx, "travel.error", map[string]any{"call_id": callID, "kind": "invalid_request"})
		return fmt.Errorf("Aviasales: неверный адрес")
	}
	if authenticated {
		req.Header.Set("X-Access-Token", c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.trace.event(ctx, "travel.error", map[string]any{"call_id": callID, "kind": "connection_or_cancellation"})
		return fmt.Errorf("Aviasales: соединение не удалось")
	}
	defer resp.Body.Close()
	c.trace.event(ctx, "travel.status", map[string]any{"call_id": callID, "status": resp.StatusCode, "elapsed_ms": time.Since(started).Milliseconds()})
	if resp.StatusCode != 200 {
		return fmt.Errorf("Aviasales: HTTP %d", resp.StatusCode)
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if readErr != nil || len(raw) > 8<<20 {
		c.trace.event(ctx, "travel.error", map[string]any{"call_id": callID, "kind": "unreadable_or_oversized_response"})
		return fmt.Errorf("Aviasales: не удалось получить полный ответ")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		c.trace.event(ctx, "travel.error", map[string]any{"call_id": callID, "kind": "invalid_json"})
		return fmt.Errorf("Aviasales: неверный ответ")
	}
	var meta struct {
		Success *bool `json:"success"`
	}
	_ = json.Unmarshal(raw, &meta)
	var response any = json.RawMessage(raw)
	if meta.Success != nil && !*meta.Success {
		response = map[string]any{"error_body_omitted": true}
	}
	if list, ok := out.(*[]city); ok {
		response = map[string]any{"catalog_count": len(*list)}
	}
	c.trace.event(ctx, "travel.response", map[string]any{"call_id": callID, "body": response, "elapsed_ms": time.Since(started).Milliseconds()})
	return nil
}
func (c *travelClient) loadCities(ctx context.Context) error {
	var list []city
	if err := c.get(ctx, "/data/ru/cities.json", nil, &list, false); err != nil {
		return err
	}
	cities := map[string]city{}
	for _, v := range list {
		if v.Country != "" && v.Code != "" && v.Name != "" {
			cities[v.Code] = v
		}
	}
	if len(cities) < 2 {
		return fmt.Errorf("Aviasales: справочник городов пуст")
	}
	c.cities = cities
	return nil
}
func dateRange(s string) (time.Time, time.Time, error) {
	if len(s) == 7 {
		first, err := time.Parse("2006-01", s)
		return first, first.AddDate(0, 1, 0).AddDate(0, 0, -1), err
	}
	day, err := time.Parse("2006-01-02", s)
	return day, day, err
}
func (c *travelClient) validate(p searchPlan, now time.Time) error {
	if p.ReturnOrigin != "" {
		if _, ok := c.cities[p.ReturnOrigin]; !ok {
			return fmt.Errorf("не удалось сопоставить город обратного вылета")
		}
		if !p.RoundTrip {
			return fmt.Errorf("обратный город вылета требует поездки туда-обратно")
		}
		country := p.DestinationCountry
		if p.Destination != "" {
			country = c.cities[p.Destination].Country
		}
		if country != "" && c.cities[p.ReturnOrigin].Country != country {
			return fmt.Errorf("города прилёта и обратного вылета должны быть в одной стране")
		}
	}
	if _, ok := c.cities[p.Origin]; !ok {
		return fmt.Errorf("не удалось сопоставить город отправления «%.80s» со справочником Aviasales", p.Origin)
	}
	if p.Destination != "" {
		if _, ok := c.cities[p.Destination]; !ok {
			return fmt.Errorf("не удалось сопоставить город назначения «%.80s» со справочником Aviasales", p.Destination)
		}
		if p.Destination == p.Origin {
			return fmt.Errorf("города отправления и назначения совпадают")
		}
		if p.DestinationCountry != "" && c.cities[p.Destination].Country != p.DestinationCountry {
			return fmt.Errorf("город назначения не соответствует указанной стране")
		}
	}
	if p.DestinationCountry != "" {
		found := false
		for _, v := range c.cities {
			if v.Country == p.DestinationCountry {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("страна назначения не найдена в справочнике Aviasales")
		}
	}
	start, end, err := departureRange(p)
	if err != nil {
		return fmt.Errorf("проверьте даты вылета: %s", err)
	}
	today, _ := time.Parse("2006-01-02", now.Format("2006-01-02"))
	if end.Before(today) || end.After(today.AddDate(2, 0, 31)) {
		return fmt.Errorf("укажите дату или месяц вылета в ближайшие два года")
	}
	if p.Return != "" {
		_, retEnd, err := dateRange(p.Return)
		if err != nil || retEnd.Before(start) || retEnd.After(today.AddDate(2, 0, 31)) {
			return fmt.Errorf("проверьте дату возвращения")
		}
	}
	if !p.RoundTrip && (p.Return != "" || p.MinDays != 0 || p.MaxDays != 0) {
		return fmt.Errorf("длительность поездки и возвращение требуют билета туда-обратно")
	}
	if p.Budget < 0 || p.Budget > 10000000 || p.MinDays < 0 || p.MaxDays < 0 || p.MinDays > 365 || p.MaxDays > 365 || (p.MaxDays > 0 && p.MinDays > p.MaxDays) {
		return fmt.Errorf("проверьте бюджет и длительность поездки")
	}
	if p.RoundTrip && p.Return == "" && p.MinDays == 0 && p.MaxDays == 0 {
		return fmt.Errorf("укажите дату возвращения или длительность поездки")
	}
	return nil
}
func departureRange(p searchPlan) (time.Time, time.Time, error) {
	first, last, err := dateRange(p.Departure)
	if err != nil || p.DepartureEnd == "" {
		return first, last, err
	}
	first, err = time.Parse("2006-01-02", p.Departure)
	if err != nil {
		return first, last, fmt.Errorf("начало диапазона должно быть точной датой")
	}
	last, err = time.Parse("2006-01-02", p.DepartureEnd)
	if err != nil || last.Before(first) || last.Sub(first) > 730*24*time.Hour {
		return first, last, fmt.Errorf("диапазон вылета должен составлять от 1 до 731 дня")
	}
	return first, last, nil
}
func departureQueries(p searchPlan) []string {
	if p.DepartureEnd == "" {
		return []string{p.Departure}
	}
	first, last, err := departureRange(p)
	if err != nil {
		return nil
	}
	var months []string
	for month := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, time.UTC); !month.After(last); month = month.AddDate(0, 1, 0) {
		months = append(months, month.Format("2006-01"))
	}
	return months
}
func matchesDate(actual, requested string) bool {
	if len(actual) < 10 {
		return false
	}
	day, err := time.Parse("2006-01-02", actual[:10])
	if err != nil {
		return false
	}
	first, last, err := dateRange(requested)
	return err == nil && !day.Before(first) && !day.After(last)
}
func (c *travelClient) accepts(p searchPlan, o offer, now time.Time) bool {
	return c.rejectionReason(p, o, now) == ""
}
func (c *travelClient) rejectionReason(p searchPlan, o offer, now time.Time) string {
	originMatches := o.Origin == p.Origin || (len(p.Origin) == 2 && c.cities[o.Origin].Country == p.Origin)
	if !originMatches || o.Destination == o.Origin || (p.Destination != "" && o.Destination != p.Destination) {
		return "другое направление"
	}
	if o.Price <= 0 {
		return "не указана цена"
	}
	if p.Budget > 0 && o.Price > p.Budget {
		return "превышен бюджет"
	}
	if _, ok := c.cities[o.Destination]; !ok {
		return "неизвестный город назначения"
	}
	if p.DestinationCountry != "" && c.cities[o.Destination].Country != p.DestinationCountry {
		return "другая страна"
	}
	first, last, err := departureRange(p)
	if err != nil || len(o.Departure) < 10 {
		return "неверная дата вылета"
	}
	day, err := time.Parse("2006-01-02", o.Departure[:10])
	if err != nil {
		return "неверная дата вылета"
	}
	if day.Before(first) || day.After(last) || o.Departure[:10] < now.Format("2006-01-02") {
		return "вылет вне заданного диапазона"
	}
	if p.Direct && (o.Transfers != 0 || (p.RoundTrip && o.ReturnTransfers != 0)) {
		return "есть пересадки"
	}
	if p.RoundTrip {
		if len(o.Return) < 10 {
			return "нет обратного билета"
		}
		if p.Return != "" && !matchesDate(o.Return, p.Return) {
			return "другая дата возвращения"
		}
		dep, err := time.Parse("2006-01-02", o.Departure[:10])
		if err != nil {
			return "неверная дата вылета"
		}
		ret, err := time.Parse("2006-01-02", o.Return[:10])
		if err != nil {
			return "неверная дата возвращения"
		}
		days := int(ret.Sub(dep).Hours() / 24)
		if days < 0 {
			return "возвращение раньше вылета"
		}
		if days < p.MinDays || (p.MaxDays > 0 && days > p.MaxDays) {
			return fmt.Sprintf("длительность %d дн. вместо %d–%d", days, p.MinDays, p.MaxDays)
		}
	} else if o.Return != "" {
		return "билет туда-обратно вместо одной стороны"
	}
	if safeTicketLink(o.Link) == "" {
		return "нет допустимой ссылки"
	}
	return ""
}
func safeTicketLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return ""
	}
	if u.IsAbs() {
		if u.Scheme != "https" || u.Host != "www.aviasales.ru" {
			return ""
		}
	} else {
		if u.Host != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
			return ""
		}
		u.Scheme = "https"
		u.Host = "www.aviasales.ru"
	}
	if strings.ContainsAny(u.String(), "\r\n\\") {
		return ""
	}
	return u.String()
}
func (c *travelClient) search(ctx context.Context, p searchPlan, now time.Time, progress func(string)) ([]offer, error) {
	var offers []offer
	seen := map[string]bool{}
	queries := departureQueries(p)
	pages := make([]int, len(queries))
	done := make([]bool, len(queries))
	limit := max(3, len(queries))
	for request := 0; request < limit; {
		advanced := false
		for i, departure := range queries {
			if done[i] || request >= limit {
				continue
			}
			advanced = true
			request++
			pages[i]++
			progress(fmt.Sprintf("Aviasales: запрос %d/%d, вылет %s, страница %d", request, limit, departure, pages[i]))
			q := url.Values{"origin": {p.Origin}, "departure_at": {departure}, "currency": {"rub"}, "market": {"ru"}, "sorting": {"price"}, "limit": {"100"}, "page": {strconv.Itoa(pages[i])}, "one_way": {strconv.FormatBool(!p.RoundTrip)}, "direct": {strconv.FormatBool(p.Direct)}, "unique": {"false"}}
			if p.Destination != "" {
				q.Set("destination", p.Destination)
			} else if p.DestinationCountry != "" {
				q.Set("destination", p.DestinationCountry)
			}
			if p.Return != "" {
				q.Set("return_at", p.Return)
			}
			var body struct {
				Success  bool    `json:"success"`
				Currency string  `json:"currency"`
				Data     []offer `json:"data"`
			}
			if err := c.get(ctx, "/aviasales/v3/prices_for_dates", q, &body, true); err != nil {
				return nil, err
			}
			if !body.Success || body.Currency != "rub" {
				return nil, fmt.Errorf("Aviasales: ошибка ответа или неожиданная валюта")
			}
			acceptedBefore := len(offers)
			rejected := map[string]int{}
			var decisions []map[string]any
			for _, o := range body.Data {
				key := o.Origin + o.Destination + o.Departure + o.Return + strconv.Itoa(o.Price) + o.Airline
				if reason := c.rejectionReason(p, o, now); reason != "" {
					decisions = append(decisions, map[string]any{"offer": o, "decision": "reject", "reason": reason})
					rejected[reason]++
					continue
				}
				if !seen[key] {
					decisions = append(decisions, map[string]any{"offer": o, "decision": "accept"})
					seen[key] = true
					offers = append(offers, o)
				}
			}
			c.trace.event(ctx, "travel.filter", map[string]any{"conditions": p, "departure_query": departure, "page": pages[i], "received": len(body.Data), "accepted": len(offers) - acceptedBefore, "rejected": rejected, "decisions": decisions})
			progress(fmt.Sprintf("Aviasales: получено %d записей, подошло %d", len(body.Data), len(offers)-acceptedBefore))
			var reasons []string
			for reason, count := range rejected {
				reasons = append(reasons, fmt.Sprintf("%s — %d", reason, count))
			}
			sort.Strings(reasons)
			if len(reasons) > 0 {
				progress("Aviasales: исключено: " + strings.Join(reasons, "; "))
			}
			if len(body.Data) < 100 {
				done[i] = true
			}
		}
		if !advanced {
			break
		}
	}
	sort.SliceStable(offers, func(i, j int) bool { return offers[i].Price < offers[j].Price })
	progress(fmt.Sprintf("Aviasales: подходят %d предложений", len(offers)))
	return offers, nil
}

var airlineCode = regexp.MustCompile(`^[A-Z0-9]{2,3}$`)

func (c *travelClient) render(p searchPlan, offers []offer, now time.Time) string {
	destination := "любой город мира"
	if p.DestinationCountry != "" {
		destination = "любое направление, страна " + p.DestinationCountry
	}
	if p.DestinationCountry == "RU" {
		destination = "любой город РФ"
	}
	if p.Destination != "" {
		destination = c.cities[p.Destination].Name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s → %s\nВылет: %s", c.cities[p.Origin].Name, destination, p.Departure)
	if p.DepartureEnd != "" {
		fmt.Fprintf(&b, " — %s включительно", p.DepartureEnd)
	}
	if p.ReturnOrigin != "" {
		fmt.Fprintf(&b, " · обратный вылет из %s", c.cities[p.ReturnOrigin].Name)
	}
	if p.RoundTrip {
		b.WriteString(" · туда-обратно")
		if p.Return != "" {
			fmt.Fprintf(&b, " · обратно: %s", p.Return)
		}
	} else {
		b.WriteString(" · в одну сторону")
	}
	if p.Budget > 0 {
		fmt.Fprintf(&b, " · до %d ₽", p.Budget)
	}
	if p.Direct {
		b.WriteString(" · без пересадок")
	}
	if p.MinDays > 0 || p.MaxDays > 0 {
		fmt.Fprintf(&b, " · длительность: от %d дней", p.MinDays)
		if p.MaxDays > 0 {
			fmt.Fprintf(&b, " до %d дней", p.MaxDays)
		}
	}
	b.WriteString("\n1 взрослый, эконом. Цена за весь указанный маршрут.\n\n")
	journey := p.ReturnOrigin != "" || (p.RoundTrip && p.Destination == "" && p.DestinationCountry != "")
	if journey {
		b.WriteString("Проверяются готовые билеты туда-обратно и/или пары отдельных билетов. Для пары показаны две ссылки и общая цена. Переезд между городами страны назначения самостоятельный и не включён в цену. Длительность считается между датами вылета из РФ и обратно.\n\n")
	}
	if len(offers) == 0 {
		b.WriteString("В просмотренном кэше нет вариантов под все условия. Это не означает, что билетов нет. Можно изменить даты, бюджет или длительность поездки.\n")
	}
	for i, o := range offers[:min(len(offers), 5)] {
		if o.ReturnLink != "" {
			fmt.Fprintf(&b, "%d. %s → %s / %s → %s — всего %d ₽\nВылет: %s (%d ₽)", i+1, c.cities[o.Origin].Name, c.cities[o.Destination].Name, c.cities[o.ReturnOrigin].Name, c.cities[o.Origin].Name, o.Price, o.Departure, o.OutboundPrice)
		} else {
			fmt.Fprintf(&b, "%d. %s → %s — %d ₽\nВылет: %s", i+1, c.cities[o.Origin].Name, c.cities[o.Destination].Name, o.Price, o.Departure)
			if journey && p.RoundTrip {
				b.WriteString("\nГотовый билет туда-обратно; цена за оба направления")
			}
		}
		if p.RoundTrip {
			fmt.Fprintf(&b, "\nОбратно: %s", o.Return)
			if o.ReturnLink != "" {
				fmt.Fprintf(&b, " (%d ₽)", o.InboundPrice)
			}
		}
		fmt.Fprintf(&b, "\nПересадки: %d", o.Transfers)
		if p.RoundTrip {
			fmt.Fprintf(&b, " / %d обратно", o.ReturnTransfers)
		}
		if airlineCode.MatchString(o.Airline) {
			fmt.Fprintf(&b, " · авиакомпания %s", o.Airline)
		}
		fmt.Fprintf(&b, "\nПроверить цену: %s\n\n", safeTicketLink(o.Link))
		if o.ReturnLink != "" {
			fmt.Fprintf(&b, "Обратный билет: %s\n\n", safeTicketLink(o.ReturnLink))
		}
	}
	coverage := fmt.Sprintf("Лимит просмотра — %d записей", max(3, len(departureQueries(p)))*100)
	if journey {
		coverage = "Проверены отдельные выборки кэша готовых билетов и/или двух направлений"
	}
	fmt.Fprintf(&b, "Данные Aviasales из кэша; получены %s МСК. Цена, наличие, багаж и условия требуют проверки по ссылке. %s; это не полный поиск всех рейсов. Показываю до 5 самых дешёвых подходящих вариантов из просмотренных.", now.Format("02.01.2006 15:04"), coverage)
	return b.String()
}
