package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeChatClient struct{}

func (fakeChatClient) Complete(context.Context, []Message) (string, error) {
	return "тестовый ответ", nil
}

func TestIndexIsAvailable(t *testing.T) {
	app := newWebApp(fakeChatClient{}, "", "")
	request := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	response := httptest.NewRecorder()

	app.routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET / returned %d instead of 200: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "DeepSeek-агент") {
		t.Fatal("GET / did not return the chat interface")
	}
}

func TestIndexRejectsPost(t *testing.T) {
	app := newWebApp(fakeChatClient{}, "", "")
	request := httptest.NewRequest(http.MethodPost, "http://localhost/", nil)
	response := httptest.NewRecorder()

	app.routes().ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST / returned %d instead of 405", response.Code)
	}
}
