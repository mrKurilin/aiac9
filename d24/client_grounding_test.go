package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestDeepSeekResponseGetsDocumentCitation(t *testing.T) {
	client := &DeepSeekClient{key: "placeholder", url: "https://example.com/chat", model: "fake", http: &http.Client{Transport: fakeTransport(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer placeholder" {
			t.Fatal("authorization header missing")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"25 days per year"}}]}`)), Header: make(http.Header)}, nil
	})}}
	a := NewAgent(client, t.TempDir())
	a.rag = true
	if err := a.Build(context.Background(), "knowledge", nil); err != nil {
		t.Fatal(err)
	}
	got, _, err := a.Answer(context.Background(), "How many PTO days do employees with 3-5 years of service receive?", nil)
	if err != nil || !strings.Contains(got, "25 days per year") || !strings.Contains(got, "Employee_Handbook_2025.txt") || !strings.Contains(got, "3-5 years of service: 25 days per year") {
		t.Fatalf("answer=%q err=%v", got, err)
	}
}
