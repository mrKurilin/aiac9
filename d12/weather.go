package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const weatherHost = "wttr.in"
const weatherBaseURL = "https://wttr.in"

type WeatherDay struct {
	Date        string `json:"date"`
	MinC        string `json:"min_c"`
	MaxC        string `json:"max_c"`
	MaxRainPct  int    `json:"max_rain_percent"`
	Description string `json:"description,omitempty"`
}

type WeatherReport struct {
	Location    string       `json:"location"`
	ObservedAt  string       `json:"observed_at,omitempty"`
	CurrentC    string       `json:"current_c,omitempty"`
	FeelsLikeC  string       `json:"feels_like_c,omitempty"`
	HumidityPct string       `json:"humidity_percent,omitempty"`
	WindKmh     string       `json:"wind_kmh,omitempty"`
	Description string       `json:"description,omitempty"`
	Forecast    []WeatherDay `json:"forecast"`
	Source      string       `json:"source"`
}

type WeatherProvider interface {
	Forecast(context.Context, string) (WeatherReport, error)
}

type WttrWeather struct{ HTTP *http.Client }

var weatherLocationPattern = regexp.MustCompile(`^[\p{L}][\p{L} .,'’()\-]{0,79}$`)
var weatherQuestionPattern = regexp.MustCompile(`(?i)(погод|прогноз|температур|дожд|снег|ветер|weather|forecast|temperature|rain|snow|wind)`)

func isWeatherQuestion(prompt string) bool { return weatherQuestionPattern.MatchString(prompt) }

func validateWeatherLocation(location string) error {
	location = strings.TrimSpace(location)
	if !weatherLocationPattern.MatchString(location) || sensitiveMemoryValue.MatchString(location) {
		return fmt.Errorf("нужно обычное название города или места длиной до 80 символов")
	}
	return nil
}

func (w WttrWeather) Forecast(ctx context.Context, location string) (WeatherReport, error) {
	location = strings.TrimSpace(location)
	if err := validateWeatherLocation(location); err != nil {
		return WeatherReport{}, err
	}
	requestURL := weatherBaseURL + "/" + url.PathEscape(location) + "?format=j1&lang=ru"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return WeatherReport{}, err
	}
	if req.URL.Scheme != "https" || req.URL.Hostname() != weatherHost {
		return WeatherReport{}, fmt.Errorf("weather host не разрешён")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "mrkai-aiac9/1.0")
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return WeatherReport{}, fmt.Errorf("получить прогноз: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return WeatherReport{}, fmt.Errorf("weather service: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return WeatherReport{}, fmt.Errorf("прочитать прогноз: %w", err)
	}
	return parseWttr(data, location)
}

type wttrPayload struct {
	Current []struct {
		FeelsLikeC  string `json:"FeelsLikeC"`
		Humidity    string `json:"humidity"`
		Observation string `json:"localObsDateTime"`
		TempC       string `json:"temp_C"`
		WeatherDesc []struct {
			Value string `json:"value"`
		} `json:"weatherDesc"`
		WindspeedKmph string `json:"windspeedKmph"`
	} `json:"current_condition"`
	Area []struct {
		AreaName []struct {
			Value string `json:"value"`
		} `json:"areaName"`
		Country []struct {
			Value string `json:"value"`
		} `json:"country"`
	} `json:"nearest_area"`
	Weather []struct {
		Date     string `json:"date"`
		MaxTempC string `json:"maxtempC"`
		MinTempC string `json:"mintempC"`
		Hourly   []struct {
			ChanceOfRain string `json:"chanceofrain"`
			WeatherDesc  []struct {
				Value string `json:"value"`
			} `json:"weatherDesc"`
		} `json:"hourly"`
	} `json:"weather"`
}

func firstDescription(values []struct {
	Value string `json:"value"`
}) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0].Value)
}

func parseWttr(data []byte, fallbackLocation string) (WeatherReport, error) {
	var payload wttrPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return WeatherReport{}, fmt.Errorf("разобрать прогноз: %w", err)
	}
	// Some deployments wrap the documented response in a data property.
	if len(payload.Current) == 0 && len(payload.Weather) == 0 {
		var wrapped struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(data, &wrapped) == nil && len(wrapped.Data) > 0 {
			_ = json.Unmarshal(wrapped.Data, &payload)
		}
	}
	if len(payload.Current) == 0 && len(payload.Weather) == 0 {
		return WeatherReport{}, fmt.Errorf("weather service вернул пустой прогноз")
	}
	report := WeatherReport{Location: fallbackLocation, Source: weatherHost}
	if len(payload.Area) > 0 && len(payload.Area[0].AreaName) > 0 {
		report.Location = payload.Area[0].AreaName[0].Value
		if len(payload.Area[0].Country) > 0 && payload.Area[0].Country[0].Value != "" {
			report.Location += ", " + payload.Area[0].Country[0].Value
		}
	}
	if len(payload.Current) > 0 {
		current := payload.Current[0]
		report.ObservedAt = current.Observation
		report.CurrentC = current.TempC
		report.FeelsLikeC = current.FeelsLikeC
		report.HumidityPct = current.Humidity
		report.WindKmh = current.WindspeedKmph
		report.Description = firstDescription(current.WeatherDesc)
	}
	for _, day := range payload.Weather {
		summary := WeatherDay{Date: day.Date, MinC: day.MinTempC, MaxC: day.MaxTempC}
		for _, hour := range day.Hourly {
			chance, _ := strconv.Atoi(hour.ChanceOfRain)
			if chance > summary.MaxRainPct {
				summary.MaxRainPct = chance
			}
			if summary.Description == "" {
				summary.Description = firstDescription(hour.WeatherDesc)
			}
		}
		report.Forecast = append(report.Forecast, summary)
		if len(report.Forecast) == 3 {
			break
		}
	}
	return report, nil
}
