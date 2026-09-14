package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type testResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (r *testResponse) Header() http.Header {
	if r.header == nil {
		r.header = make(http.Header)
	}
	return r.header
}
func (r *testResponse) WriteHeader(status int) { r.status = status }
func (r *testResponse) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(data)
}
func (r *testResponse) Flush() {}

func testRequest(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, "http://example.test"+target, body)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestWebMemoryAndStreamingChat(t *testing.T) {
	agent := NewAgent(&captureClient{reply: "Веб-ответ"}, "", nil, "web")
	handler := webHandler(agent)
	body := []byte(`{"action":"remember","layer":"working","key":"goal","value":"demo"}`)
	request := testRequest(t, http.MethodPost, "/api/memory", bytes.NewReader(body))
	recorder := &testResponse{}
	handler.ServeHTTP(recorder, request)
	if recorder.status != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.status, recorder.body.String())
	}
	var state webState
	if err := json.Unmarshal(recorder.body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Working["goal"] != "demo" {
		t.Fatalf("state=%+v", state)
	}

	request = testRequest(t, http.MethodPost, "/api/ask", strings.NewReader(`{"prompt":"hello"}`))
	recorder = &testResponse{}
	handler.ServeHTTP(recorder, request)
	if recorder.status != http.StatusOK || !strings.Contains(recorder.body.String(), `"type":"delta"`) || !strings.Contains(recorder.body.String(), "Веб-ответ") {
		t.Fatalf("status=%d body=%s", recorder.status, recorder.body.String())
	}
	if len(agent.Transcript()) != 2 || len(agent.Snapshot().ShortTerm) != 0 {
		t.Fatal("web transcript was not kept volatile")
	}
}

type streamFake struct{}

func (streamFake) Complete(context.Context, []Message) (string, error) { return "AB", nil }
func (streamFake) CompleteStream(_ context.Context, _ []Message, emit func(string) error) (string, error) {
	if err := emit("A"); err != nil {
		return "", err
	}
	if err := emit("B"); err != nil {
		return "", err
	}
	return "AB", nil
}

func TestWebForwardsStreamingChunks(t *testing.T) {
	handler := webHandler(NewAgent(streamFake{}, "", nil, "stream"))
	recorder := &testResponse{}
	handler.ServeHTTP(recorder, testRequest(t, http.MethodPost, "/api/ask", strings.NewReader(`{"prompt":"go"}`)))
	text := recorder.body.String()
	if strings.Count(text, `"type":"delta"`) != 2 || !strings.Contains(text, `"content":"A"`) || !strings.Contains(text, `"content":"B"`) {
		t.Fatalf("events=%s", text)
	}
}

func TestWebRejectsWrongMethodAndUnknownJSON(t *testing.T) {
	handler := webHandler(NewAgent(&captureClient{}, "", nil, "validation"))
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/state", ""},
		{http.MethodPost, "/api/memory", `{"action":"clear","layer":"short","extra":true}`},
	} {
		recorder := &testResponse{}
		handler.ServeHTTP(recorder, testRequest(t, tc.method, tc.path, strings.NewReader(tc.body)))
		if recorder.status < 400 {
			t.Fatalf("%s %s accepted: %d", tc.method, tc.path, recorder.status)
		}
	}
}

func TestWebRunsSlashCommandWithoutCallingModel(t *testing.T) {
	client := &captureClient{}
	handler := webHandler(NewAgent(client, "", nil, "command"))
	recorder := &testResponse{}
	body := strings.NewReader(`{"line":"/remember working goal ship"}`)
	handler.ServeHTTP(recorder, testRequest(t, http.MethodPost, "/api/command", body))
	if recorder.status != http.StatusOK || !strings.Contains(recorder.body.String(), "Сохранено явно") || !strings.Contains(recorder.body.String(), `"goal":"ship"`) {
		t.Fatalf("status=%d body=%s", recorder.status, recorder.body.String())
	}
	if len(client.calls) != 0 {
		t.Fatal("slash command was sent to model")
	}
}

func TestSiteRestartClearsMessagesButKeepsFacts(t *testing.T) {
	agent := NewAgent(&captureClient{}, "", nil, "restart")
	if err := agent.Remember("short", "topic", "memory"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Remember("working", "goal", "ship"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Remember("long", "profile.language", "ru"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Ask(context.Background(), "temporary message", nil); err != nil {
		t.Fatal(err)
	}
	if len(agent.Transcript()) != 2 {
		t.Fatal("test transcript was not created")
	}

	handler := webHandler(agent)
	recorder := &testResponse{}
	handler.ServeHTTP(recorder, testRequest(t, http.MethodPost, "/api/transcript/reset", nil))
	if recorder.status != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.status, recorder.body.String())
	}
	var state webState
	if err := json.Unmarshal(recorder.body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Transcript) != 0 {
		t.Fatalf("transcript=%+v", state.Transcript)
	}
	if state.ShortTerm["topic"] != "memory" || state.Working["goal"] != "ship" || state.LongTerm["profile.language"] != "ru" {
		t.Fatalf("facts were cleared: %+v", state)
	}
}

func TestWebPageContainsChatAndThreeMemoryWindows(t *testing.T) {
	handler := webHandler(NewAgent(&captureClient{}, "", nil, "page"))
	recorder := &testResponse{}
	handler.ServeHTTP(recorder, testRequest(t, http.MethodGet, "/", nil))
	page := recorder.body.String()
	for _, want := range []string{"Текущий диалог", "Краткосрочная", "Рабочая", "Долговременная", "Сохранить явно", "renderMarkdownInto", "noopener noreferrer", "commandChoices", "/api/command", "/api/transcript/reset"} {
		if !strings.Contains(page, want) {
			t.Errorf("page misses %q", want)
		}
	}
}
