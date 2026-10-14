package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testClock() time.Time {
	return time.Date(2026, 10, 14, 12, 0, 0, 0, time.FixedZone("MSK", 3*3600))
}

func TestAccessExpiresTomorrowAndRestartDoesNotExtendIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crm.sqlite")
	store, err := openCRMStore(path, testClock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := store.lookupAccess(ctx, "8 (900) 000-00-01")
	if err != nil || first.Allowed || first.NeedsInvestigation || first.BlockedDate != "2026-10-14" || first.RetryDate != "2026-10-15" {
		t.Fatalf("today: %+v %v", first, err)
	}
	second, err := store.lookupAccess(ctx, "+7 900 000-00-02")
	if err != nil || second.Allowed || !second.NeedsInvestigation {
		t.Fatalf("unknown outage: %+v %v", second, err)
	}
	_ = store.db.Close()
	store, err = openCRMStore(path, func() time.Time { return testClock().AddDate(0, 0, 1) })
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	first, err = store.lookupAccess(ctx, "79000000001")
	if err != nil || !first.Allowed || first.NeedsInvestigation {
		t.Fatalf("tomorrow after restart: %+v %v", first, err)
	}
	for _, phone := range []string{"", "123", "79000000001' OR 1=1", "+7<script>"} {
		if _, err := store.lookupAccess(ctx, phone); err == nil {
			t.Fatalf("accepted invalid phone %q", phone)
		}
	}
}

type serviceTestModel struct{ t *testing.T }

func (f serviceTestModel) Complete(_ context.Context, messages []message, tools []toolDefinition) (message, error) {
	if len(tools) != 3 || tools[0].Function.Name != "mcp__crm__inventory_list" {
		f.t.Fatal("CRM tools were not discovered")
	}
	last := messages[len(messages)-1]
	var question string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			question = messages[i].Content
			break
		}
	}
	if last.Role == "user" {
		call := toolCall{ID: "call-1", Type: "function"}
		switch {
		case strings.Contains(question, "фильтр"), strings.Contains(question, "колодки"):
			call.Function.Name = tools[0].Function.Name
			call.Function.Arguments = `{}`
		case strings.Contains(question, "79000000001"):
			call.Function.Name = tools[1].Function.Name
			call.Function.Arguments = `{"phone":"79000000001"}`
		case strings.Contains(question, "79000000002"):
			call.Function.Name = tools[1].Function.Name
			call.Function.Arguments = `{"phone":"79000000002"}`
		case strings.Contains(question, "номер не указан"):
			return message{Content: `{"answer":"Укажите телефон.","resolved":true,"reason":"уточнение"}`}, nil
		default:
			return message{Content: `{"answer":"В документации нет ответа, нужна проверка.","resolved":false,"reason":"Вопрос не описан в FAQ","category":"general"}`}, nil
		}
		return message{ToolCalls: []toolCall{call}}, nil
	}
	if last.Role != "tool" {
		return message{}, fmt.Errorf("tool result not returned")
	}
	if strings.Contains(question, "79000000001") {
		if !strings.Contains(last.Content, "2026-10-14") || !strings.Contains(last.Content, "2026-10-15") {
			f.t.Fatal("today and retry date missing")
		}
		return message{Content: `{"answer":"Сегодня, 14 октября, доступ закрыт до полуночи по Москве. Завтра, 15 октября, доступ восстановится.","resolved":true,"category":"access"}`}, nil
	}
	if strings.Contains(question, "79000000002") {
		if !strings.Contains(last.Content, `"needs_investigation":true`) {
			f.t.Fatal("unexplained outage missing")
		}
		// Deliberately omit create_support_task and claim solved: the business rule must still escalate.
		return message{Content: `{"answer":"Попробуйте снова.","resolved":true,"category":"access"}`}, nil
	}
	if strings.Contains(question, "колодки") {
		if !strings.Contains(last.Content, `"quantity":0`) {
			f.t.Fatal("out-of-stock result missing")
		}
		return message{Content: `{"answer":"Колодок Brembo P 85 020 сейчас нет в наличии.","resolved":true,"category":"inventory"}`}, nil
	}
	if !strings.Contains(last.Content, `"quantity":8`) {
		f.t.Fatal("stock result missing")
	}
	return message{Content: `{"answer":"MANN W 914/2 в наличии: 8 штук, 790 рублей.","resolved":true,"category":"inventory"}`}, nil
}

type webFixture struct {
	store   *crmStore
	crm     *httptest.Server
	support *httptest.Server
	visitor *http.Client
}

func newWebFixture(t *testing.T) webFixture {
	t.Helper()
	store, err := openCRMStore(filepath.Join(t.TempDir(), "crm.sqlite"), testClock)
	if err != nil {
		t.Fatal(err)
	}
	key := "placeholder-service-key"
	app := &crmHTTP{store: store, adminPassword: "placeholder-password", serviceKey: key, client: &http.Client{Timeout: 5 * time.Second}}
	crm := httptest.NewServer(app.handler())
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	search := searchIndex{root: root, path: filepath.Join(t.TempDir(), "index.json")}
	if _, err := search.build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	agent := &serviceAgent{model: serviceTestModel{t: t}, crm: &crmClient{url: crm.URL, key: key, http: &http.Client{Timeout: 5 * time.Second}}, search: search}
	support := httptest.NewServer((&supportHTTP{agent: agent, key: key, slots: make(chan struct{}, 8)}).handler())
	app.supportURL = support.URL
	jar, _ := cookiejar.New(nil)
	t.Cleanup(func() { support.Close(); crm.Close(); store.db.Close() })
	return webFixture{store: store, crm: crm, support: support, visitor: &http.Client{Jar: jar}}
}

func (f webFixture) chat(t *testing.T, text string) supportReply {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"message": text})
	response, err := f.visitor.Post(f.crm.URL+"/api/chat", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("chat HTTP %d: %s", response.StatusCode, data)
	}
	var reply supportReply
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestWebChatInventoryAccessEscalationAndKanban(t *testing.T) {
	f := newWebFixture(t)
	ctx := context.Background()
	for _, question := range []string{"Есть фильтр MANN W 914/2?", "Есть колодки Brembo?", "Не работает доступ 79000000001", "Для доступа номер не указан"} {
		reply := f.chat(t, question)
		if !reply.Resolved || reply.TaskID != 0 {
			t.Fatalf("known answer wrongly escalated: %+v", reply)
		}
	}
	tasks, err := f.store.tasks(ctx)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("unexpected tasks: %v %v", tasks, err)
	}
	reply := f.chat(t, "Не работает доступ 79000000002")
	if reply.Resolved || reply.TaskID == 0 || !strings.Contains(reply.Answer, "Подтверждённой причины нет") {
		t.Fatalf("unexplained outage not escalated: %+v", reply)
	}
	task, err := f.store.task(ctx, reply.TaskID)
	if err != nil || task.Status != "TODO" || task.Phone != "79000000002" || len(task.Messages) < 5 || len(task.CodeHints) < 2 || len(task.Sources) == 0 {
		t.Fatalf("missing context: %+v %v", task, err)
	}
	if !strings.Contains(strings.Join(task.CodeHints, "\n"), "accessDecision") {
		t.Fatal("code investigation hint missing")
	}
	second := f.chat(t, "Проверь ещё раз 79000000002")
	if second.TaskID != reply.TaskID {
		t.Fatal("repeated question created duplicate active task")
	}
	for _, status := range []string{"IN PROGRESS", "DONE"} {
		payload, _ := json.Marshal(map[string]string{"title": task.Title, "reason": task.Reason, "status": status})
		request, _ := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/api/crm/tasks/%d", f.crm.URL, task.ID), strings.NewReader(string(payload)))
		request.SetBasicAuth("admin", "placeholder-password")
		response, err := f.visitor.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("move to %s: HTTP %d", status, response.StatusCode)
		}
		updated, err := f.store.task(ctx, task.ID)
		if err != nil || updated.Status != status {
			t.Fatalf("status not persisted: %+v %v", updated, err)
		}
	}
	unknown := f.chat(t, "Почему приложение теряет мою запись на обслуживание?")
	if unknown.Resolved || unknown.TaskID == 0 {
		t.Fatalf("unanswered question not escalated: %+v", unknown)
	}
}

func TestWebBoundariesAndChatResetPreserveTasks(t *testing.T) {
	f := newWebFixture(t)
	for _, path := range []string{"/crm", "/api/crm/tasks", "/mcp", "/internal/messages"} {
		response, err := f.visitor.Get(f.crm.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 401 && response.StatusCode != 403 {
			t.Fatalf("unprotected %s: %d", path, response.StatusCode)
		}
	}
	response, err := f.visitor.Get(f.crm.URL + "/crm.html")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("CRM alternate static path bypasses auth")
	}
	request, _ := http.NewRequest(http.MethodPost, f.crm.URL+"/api/chat", strings.NewReader(`{"message":"test"}`))
	request.Header.Set("Origin", "https://example.com")
	response, err = f.visitor.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-origin chat request accepted")
	}
	created := f.chat(t, "Проверь доступ 79000000002")
	request, _ = http.NewRequest(http.MethodDelete, f.crm.URL+"/api/chat", nil)
	response, err = f.visitor.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = f.visitor.Get(f.crm.URL + "/api/chat")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var records []chatRecord
	if err := json.NewDecoder(response.Body).Decode(&records); err != nil || len(records) != 0 {
		t.Fatal("reset did not start fresh chat")
	}
	if _, err := f.store.task(context.Background(), created.TaskID); err != nil {
		t.Fatal("chat reset removed CRM task")
	}
	secondJar, _ := cookiejar.New(nil)
	secondClient := &http.Client{Jar: secondJar}
	response, err = secondClient.Get(f.crm.URL + "/api/chat")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(&records); err != nil || len(records) != 0 {
		t.Fatal("another visitor received previous chat")
	}
}

func TestCRMTaskPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crm.sqlite")
	s, err := openCRMStore(path, testClock)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.createTask(context.Background(), "", "Проверить запас фильтров", "inventory", "", "Нужна сверка", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.updateTask(context.Background(), task.ID, "IN PROGRESS", task.Title, task.Reason); err != nil {
		t.Fatal(err)
	}
	s.db.Close()
	s, err = openCRMStore(path, testClock)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	task, err = s.task(context.Background(), task.ID)
	if err != nil || task.Status != "IN PROGRESS" {
		t.Fatalf("task disappeared after restart: %+v %v", task, err)
	}
}
