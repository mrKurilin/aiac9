package main

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Country searches combine complete round trips with independent ticket pairs.
// Travel within the destination country is neither searched nor priced.
func (c *travelClient) searchJourney(ctx context.Context, p searchPlan, now time.Time, progress func(string)) ([]offer, error) {
	start, end, err := departureRange(p)
	if err != nil {
		return nil, err
	}
	var ready []offer
	countrySearch := p.ReturnOrigin == "" && p.Destination == "" && p.DestinationCountry != ""
	if countrySearch {
		progress("Маршрут: ищу готовые билеты туда-обратно по стране")
		ready, err = c.search(ctx, p, now, progress)
		if err != nil {
			return nil, err
		}
	}
	// An optional search must not discard tickets already found.
	finish := func(extra []offer, searchErr error) ([]offer, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if searchErr != nil && len(ready) == 0 {
			return nil, searchErr
		}
		if searchErr != nil {
			progress("Маршрут: дополнительный поиск не завершён; показываю найденные билеты")
		}
		return cheapestJourneys(append(ready, extra...)), nil
	}
	outbound := p
	outbound.RoundTrip = false
	outbound.Return = ""
	outbound.ReturnOrigin = ""
	outbound.MinDays = 0
	outbound.MaxDays = 0
	progress("Маршрут: ищу перелёт в страну назначения")
	there, err := c.search(ctx, outbound, now, progress)
	if err != nil {
		return finish(nil, err)
	}
	if countrySearch {
		// Country-wide cache samples can differ from individual city samples.
		// Check up to eight cities observed in outbound offers, cheapest first.
		checked := map[string]bool{}
		for _, out := range there {
			if checked[out.Destination] || len(checked) >= 8 {
				continue
			}
			checked[out.Destination] = true
			cityPlan := p
			cityPlan.Destination = out.Destination
			progress(fmt.Sprintf("Маршрут: проверяю готовые билеты туда-обратно в %s", c.cities[out.Destination].Name))
			found, searchErr := c.search(ctx, cityPlan, now, progress)
			if searchErr != nil {
				return finish(nil, searchErr)
			}
			ready = append(ready, found...)
		}
	}
	if len(there) == 0 {
		c.trace.event(ctx, "journey.result", map[string]any{"conditions": p, "ready_round_trip_count": len(ready), "selected": cheapestJourneys(ready), "summary": "Нет отдельных билетов в страну назначения; обратное направление не запрашивалось"})
		return finish(nil, nil)
	}
	inbound := outbound
	inbound.Origin = p.ReturnOrigin
	if inbound.Origin == "" {
		inbound.Origin = p.Destination
	}
	if inbound.Origin == "" {
		inbound.Origin = p.DestinationCountry
	}
	inbound.Destination = p.Origin
	inbound.DestinationCountry = ""
	minDays, maxDays := p.MinDays, p.MaxDays
	if maxDays == 0 {
		maxDays = 365
	}
	if p.Return != "" {
		start, end, err = dateRange(p.Return)
		if err != nil {
			return nil, err
		}
	} else {
		start = start.AddDate(0, 0, minDays)
		end = end.AddDate(0, 0, maxDays)
	}
	inbound.Departure = start.Format("2006-01-02")
	inbound.DepartureEnd = end.Format("2006-01-02")
	progress("Маршрут: ищу обратный перелёт; переезд между городами не включён")
	back, err := c.search(ctx, inbound, now, progress)
	if err != nil {
		return finish(nil, err)
	}
	if len(back) == 0 {
		progress(fmt.Sprintf("Маршрут: в кэше нет обратных отдельных билетов; готовых билетов туда-обратно найдено %d", len(ready)))
	}
	var result []offer
	rejected := map[string]int{}
	seen := map[string]bool{}
	for _, out := range there {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		for _, ret := range back {
			if c.cities[out.Destination].Country != c.cities[ret.Origin].Country {
				rejected["города прилёта и обратного вылета в разных странах"]++
				continue
			}
			combined := out
			combined.Price = out.Price + ret.Price
			combined.OutboundPrice = out.Price
			combined.InboundPrice = ret.Price
			combined.Return = ret.Departure
			combined.ReturnTransfers = ret.Transfers
			combined.ReturnOrigin = ret.Origin
			combined.ReturnLink = ret.Link
			if reason := c.rejectionReason(p, combined, now); reason != "" {
				rejected[reason]++
				continue
			}
			key := out.Link + "\n" + ret.Link
			if seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, combined)
			sort.SliceStable(result, func(i, j int) bool { return result[i].Price < result[j].Price })
			if len(result) > 5 {
				result = result[:5]
			}
		}
	}
	c.trace.event(ctx, "journey.result", map[string]any{"conditions": p, "ready_round_trip_count": len(ready), "outbound_count": len(there), "inbound_count": len(back), "rejected_combinations": rejected, "selected": cheapestJourneys(append(ready, result...))})
	progress(fmt.Sprintf("Маршрут: подобрано %d комбинаций двух билетов", len(result)))
	return finish(result, nil)
}

func cheapestJourneys(offers []offer) []offer {
	sort.SliceStable(offers, func(i, j int) bool { return offers[i].Price < offers[j].Price })
	seen := map[string]bool{}
	var result []offer
	for _, o := range offers {
		key := o.Link + "\n" + o.ReturnLink
		if !seen[key] {
			seen[key] = true
			result = append(result, o)
		}
		if len(result) == 5 {
			break
		}
	}
	return result
}
