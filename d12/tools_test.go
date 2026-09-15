package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestWttrWeatherUsesOnlyAllowlistedHostAndNormalizesResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Hostname() != weatherHost || request.URL.Query().Get("format") != "j1" {
			t.Fatalf("unexpected request: %s", request.URL)
		}
		return jsonResponse(`{"current_condition":[{"temp_C":"7","FeelsLikeC":"5","humidity":"80","windspeedKmph":"12","weatherDesc":[{"value":"Cloudy"}]}],"nearest_area":[{"areaName":[{"value":"Moscow"}],"country":[{"value":"Russia"}]}],"weather":[{"date":"2026-09-14","maxtempC":"9","mintempC":"3","hourly":[{"chanceofrain":"40","weatherDesc":[{"value":"Cloudy"}]}]}]}`), nil
	})}
	report, err := (WttrWeather{HTTP: client}).Forecast(context.Background(), "Москва")
	if err != nil {
		t.Fatal(err)
	}
	if report.Location != "Moscow, Russia" || report.CurrentC != "7" || len(report.Forecast) != 1 || report.Forecast[0].MaxRainPct != 40 {
		t.Fatalf("report=%+v", report)
	}
}

func TestWeatherRejectsURLInsteadOfLocation(t *testing.T) {
	if err := validateWeatherLocation("https://example.com/private"); err == nil {
		t.Fatal("URL accepted as location")
	}
}

func TestDEVArticlesUsesOnlyPublicReadEndpoint(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Hostname() != articlesHost || request.URL.Path != "/api/articles" || request.URL.Query().Get("tag") != "golang" || request.URL.Query().Get("per_page") != "5" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
		}
		return jsonResponse(`[{"title":"Go article","description":"Useful","url":"https://dev.to/alice/go-1","published_at":"2026-09-01","reading_time_minutes":4,"public_reactions_count":12,"comments_count":3,"tag_list":["go"]},{"title":"External","url":"https://example.com/no"}]`), nil
	})}
	results, err := (DEVArticles{HTTP: client}).Search(context.Background(), "golang")
	if err != nil {
		t.Fatal(err)
	}
	if results.Source != articlesHost || len(results.Articles) != 1 || results.Articles[0].Title != "Go article" {
		t.Fatalf("results=%+v", results)
	}
}

type fixedWeatherPlace string

func (f fixedWeatherPlace) Resolve(context.Context, string, MemoryLayers) (string, error) {
	return string(f), nil
}

type fixedWeather struct{}

func (fixedWeather) Forecast(context.Context, string) (WeatherReport, error) {
	return WeatherReport{Location: "Москва", CurrentC: "7", Source: weatherHost}, nil
}

type fixedArticleTopic string

func (f fixedArticleTopic) Resolve(context.Context, string, MemoryLayers) (string, error) {
	return string(f), nil
}

type fixedArticles struct{}

func (fixedArticles) Search(context.Context, string) (ArticleResults, error) {
	return ArticleResults{Topic: "golang", Source: articlesHost, Articles: []Article{{Title: "Go", URL: "https://dev.to/a/go"}}}, nil
}

func TestAgentAddsWeatherToolResultToModelContext(t *testing.T) {
	client := &captureClient{}
	agent := NewAgent(client, "", nil, "weather")
	agent.weather = fixedWeather{}
	agent.weatherPlace = fixedWeatherPlace("Москва")
	if _, err := agent.Ask(context.Background(), "Какая погода в Москве?", nil); err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, message := range client.calls[0] {
		joined += message.Content
	}
	if !strings.Contains(joined, "WEATHER_TOOL") || !strings.Contains(joined, `"current_c":"7"`) {
		t.Fatalf("context=%s", joined)
	}
	if !strings.Contains(agent.DrainToolNotice(), weatherHost) {
		t.Fatal("weather source is not reported")
	}
}

func TestAgentAddsArticleResultsToModelContext(t *testing.T) {
	client := &captureClient{}
	agent := NewAgent(client, "", nil, "articles")
	agent.articles = fixedArticles{}
	agent.articleTopic = fixedArticleTopic("golang")
	if _, err := agent.Ask(context.Background(), "Посоветуй статьи про Go", nil); err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, message := range client.calls[0] {
		joined += message.Content
	}
	if !strings.Contains(joined, "ARTICLES_TOOL") || !strings.Contains(joined, "https://dev.to/a/go") {
		t.Fatalf("context=%s", joined)
	}
	if !strings.Contains(agent.DrainToolNotice(), articlesHost) {
		t.Fatal("article source is not reported")
	}
}

func TestLLMToolResolversReturnValidatedArguments(t *testing.T) {
	weatherClient := &captureClient{reply: `{"location":"Москва"}`}
	location, err := (LLMWeatherLocationResolver{Client: weatherClient}).Resolve(context.Background(), "погода", MemoryLayers{})
	if err != nil || location != "Москва" {
		t.Fatalf("location=%q err=%v", location, err)
	}
	articleClient := &captureClient{reply: `{"tag":"golang"}`}
	tag, err := (LLMArticleTopicResolver{Client: articleClient}).Resolve(context.Background(), "статьи", MemoryLayers{})
	if err != nil || tag != "golang" {
		t.Fatalf("tag=%q err=%v", tag, err)
	}
}
