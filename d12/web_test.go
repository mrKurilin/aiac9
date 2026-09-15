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

func webFixture() ([]UserProfile, map[string]*Agent, map[string]*captureClient) {
	profiles := DefaultUserProfiles()
	agents := make(map[string]*Agent, len(profiles))
	clients := make(map[string]*captureClient, len(profiles))
	for _, profile := range profiles {
		client := &captureClient{reply: "Ответ для " + profile.Name}
		clients[profile.ID] = client
		agents[profile.ID] = NewAgent(client, "base", nil, "profile-"+profile.ID, profile)
	}
	return profiles, agents, clients
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

func TestProfilesAPIExposesThreeProfilesAndDemoPrompts(t *testing.T) {
	profiles, agents, _ := webFixture()
	response := &testResponse{}
	webHandler(profiles, agents).ServeHTTP(response, testRequest(t, http.MethodGet, "/api/profiles", nil))
	if response.status != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.status, response.body.String())
	}
	var payload profilesResponse
	if err := json.Unmarshal(response.body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Profiles) != 3 || len(payload.Prompts) != 3 {
		t.Fatalf("payload=%+v", payload)
	}
	for _, profile := range payload.Profiles {
		if len(profile.Rules) != 4 {
			t.Errorf("%s rules=%d", profile.ID, len(profile.Rules))
		}
	}
}

func TestWebSendsSamePromptToSelectedProfileWithItsContext(t *testing.T) {
	profiles, agents, clients := webFixture()
	handler := webHandler(profiles, agents)
	for _, profile := range profiles {
		response := &testResponse{}
		request := testRequest(t, http.MethodPost, "/api/profiles/"+profile.ID+"/ask", strings.NewReader(`{"prompt":"Что такое граф?"}`))
		handler.ServeHTTP(response, request)
		if response.status != http.StatusOK || !strings.Contains(response.body.String(), `"type":"delta"`) {
			t.Fatalf("%s status=%d body=%s", profile.ID, response.status, response.body.String())
		}
		calls := clients[profile.ID].calls
		if len(calls) != 1 {
			t.Fatalf("%s calls=%d", profile.ID, len(calls))
		}
		joined := ""
		for _, message := range calls[0] {
			joined += "\n" + message.Role + ":" + message.Content
		}
		if !strings.Contains(joined, profile.Name) || !strings.Contains(joined, "Что такое граф?") {
			t.Errorf("%s context=%s", profile.ID, joined)
		}
	}
}

func TestWebForwardsStreamingChunks(t *testing.T) {
	profile := DefaultUserProfiles()[0]
	agents := map[string]*Agent{
		profile.ID: NewAgent(streamFake{}, "base", nil, "stream", profile),
	}
	response := &testResponse{}
	request := testRequest(t, http.MethodPost, "/api/profiles/developer/ask", strings.NewReader(`{"prompt":"go"}`))
	webHandler([]UserProfile{profile}, agents).ServeHTTP(response, request)
	text := response.body.String()
	if strings.Count(text, `"type":"delta"`) != 2 || !strings.Contains(text, `"content":"A"`) || !strings.Contains(text, `"content":"B"`) {
		t.Fatalf("events=%s", text)
	}
}

func TestWebProfilesKeepIndependentDialogAndMemory(t *testing.T) {
	profiles, agents, _ := webFixture()
	developer := agents["developer"]
	student := agents["student"]
	if err := developer.Remember("long", "favorite", "graphs"); err != nil {
		t.Fatal(err)
	}
	if _, err := developer.Ask(context.Background(), "dev-only", nil); err != nil {
		t.Fatal(err)
	}
	if len(student.Transcript()) != 0 || len(student.Snapshot().LongTerm) != 0 {
		t.Fatal("developer context leaked into student")
	}

	response := &testResponse{}
	webHandler(profiles, agents).ServeHTTP(response, testRequest(t, http.MethodPost, "/api/reset", nil))
	if response.status != http.StatusOK || len(developer.Transcript()) != 0 {
		t.Fatalf("reset status=%d transcript=%+v", response.status, developer.Transcript())
	}
	if developer.Snapshot().LongTerm["favorite"] != "graphs" {
		t.Fatal("dialog reset erased long-term memory")
	}
}

func TestWebValidationAndPageContract(t *testing.T) {
	profiles, agents, _ := webFixture()
	handler := webHandler(profiles, agents)
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/profiles", ""},
		{http.MethodPost, "/api/profiles/unknown/ask", `{"prompt":"x"}`},
		{http.MethodPost, "/api/profiles/developer/ask", `{"prompt":"","extra":true}`},
	} {
		response := &testResponse{}
		handler.ServeHTTP(response, testRequest(t, tc.method, tc.path, bytes.NewBufferString(tc.body)))
		if response.status < 400 {
			t.Fatalf("%s %s accepted: %d", tc.method, tc.path, response.status)
		}
	}

	response := &testResponse{}
	handler.ServeHTTP(response, testRequest(t, http.MethodGet, "/", nil))
	page := response.body.String()
	for _, want := range []string{
		"Три профиля пользователей",
		"Ответы профилей",
		"Общий запрос для всех профилей",
		"Отправить всем",
		"/api/profiles/",
		"Promise.allSettled",
		"noopener noreferrer",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page misses %q", want)
		}
	}
	for _, unwanted := range []string{"Лаборатория персонализации", "Один вопрос · три взгляда", "Алгоритм один."} {
		if strings.Contains(page, unwanted) {
			t.Errorf("page still contains %q", unwanted)
		}
	}
	profilesAt := strings.Index(page, `id="profiles"`)
	responsesAt := strings.Index(page, `id="responses"`)
	composerAt := strings.Index(page, `class="lab"`)
	if profilesAt < 0 || responsesAt <= profilesAt || composerAt <= responsesAt {
		t.Errorf("unexpected section order: profiles=%d responses=%d composer=%d", profilesAt, responsesAt, composerAt)
	}
}
