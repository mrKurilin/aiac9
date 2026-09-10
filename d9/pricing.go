package main

import (
	"fmt"
	"net/url"
	"time"
)

// Published USD / 1M tokens, checked 2026-09-09:
// https://api-docs.deepseek.com/quick_start/pricing/
type Prices struct {
	Input, Cached, Output float64
	Label                 string
}
type Pricing struct {
	Model, Endpoint       string
	Input, Cached, Output *float64
}

func (p *Pricing) At(at time.Time) (Prices, error) {
	rates := Prices{Label: "ручные тарифы"}
	if p.Input == nil || p.Cached == nil || p.Output == nil {
		endpoint, err := url.Parse(p.Endpoint)
		if err != nil || endpoint.Scheme != "https" || endpoint.Host != "api.deepseek.com" {
			return rates, fmt.Errorf("для другого API задайте все три тарифа: -input-price, -cached-price, -output-price")
		}
		switch p.Model {
		case "deepseek-v4-flash", "deepseek-v4-flash-vision-exp":
			rates = Prices{Input: .22, Cached: .007, Output: .66, Label: "DeepSeek off-peak"}
		case "deepseek-v4-pro":
			rates = Prices{Input: .66, Cached: .022, Output: 1.98, Label: "DeepSeek off-peak"}
		default:
			return rates, fmt.Errorf("неизвестный тариф модели %q: задайте -input-price, -cached-price и -output-price", p.Model)
		}
		utc := at.UTC()
		hour := utc.Hour()
		day := utc.Weekday()
		if day != time.Saturday && day != time.Sunday && ((hour >= 1 && hour < 4) || (hour >= 6 && hour < 10)) {
			rates.Input *= 2
			rates.Cached *= 2
			rates.Output *= 2
			rates.Label = "DeepSeek peak"
		}
		if p.Input != nil || p.Cached != nil || p.Output != nil {
			rates.Label += " + ручные"
		}
	}
	if p.Input != nil {
		rates.Input = *p.Input
	}
	if p.Cached != nil {
		rates.Cached = *p.Cached
	}
	if p.Output != nil {
		rates.Output = *p.Output
	}
	return rates, nil
}
