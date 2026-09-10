package main

import (
	"math"
	"testing"
	"time"
)

func TestOfficialPricingSchedule(t *testing.T) {
	p := &Pricing{Model: "deepseek-v4-flash", Endpoint: "https://api.deepseek.com/chat/completions"}
	for _, tc := range []struct {
		at   string
		peak bool
	}{
		{"2026-09-09T00:59:59Z", false}, {"2026-09-09T01:00:00Z", true},
		{"2026-09-09T03:59:59Z", true}, {"2026-09-09T04:00:00Z", false},
		{"2026-09-09T06:00:00Z", true}, {"2026-09-09T10:00:00Z", false},
		{"2026-09-12T02:00:00Z", false}, {"2026-09-13T07:00:00Z", false},
		{"2026-09-09T04:00:00+03:00", true},
	} {
		at, err := time.Parse(time.RFC3339, tc.at)
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.At(at)
		if err != nil {
			t.Fatal(err)
		}
		multiplier := 1.
		if tc.peak {
			multiplier = 2
		}
		if got.Input != .22*multiplier || got.Cached != .007*multiplier || got.Output != .66*multiplier {
			t.Fatalf("%s: %+v", tc.at, got)
		}
	}
	p.Model = "deepseek-v4-pro"
	got, err := p.At(time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC))
	if err != nil || got.Input != 1.32 || got.Cached != .044 || got.Output != 3.96 {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestCustomAndUnknownPricing(t *testing.T) {
	p := &Pricing{Model: "unknown", Endpoint: "https://example.com"}
	if _, err := p.At(time.Now()); err == nil {
		t.Fatal("silent unknown pricing")
	}
	zero, one := 0., 1.
	p.Input = &zero
	p.Cached = &zero
	p.Output = &one
	got, err := p.At(time.Now())
	if err != nil || got.Input != 0 || got.Cached != 0 || got.Output != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	p = &Pricing{Model: "deepseek-v4-flash", Endpoint: "https://api.deepseek.com/chat/completions", Input: &one}
	got, err = p.At(time.Date(2026, 9, 12, 2, 0, 0, 0, time.UTC))
	if err != nil || math.Abs(got.Input-1) > 1e-12 || got.Cached != .007 {
		t.Fatalf("%+v %v", got, err)
	}
}
