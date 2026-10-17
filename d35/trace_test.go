package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func readTraceRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var records []map[string]any
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal("invalid JSONL")
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}
func TestTraceCapturesAgentHTTPAndFilteringWithCorrelation(t *testing.T) {
	trace, err := openTrace(t.TempDir(), []string{"placeholder"})
	if err != nil {
		t.Fatal(err)
	}
	defer trace.close()
	encodedPlan, _ := json.Marshal(plan())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/model" {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": string(encodedPlan), "reasoning_content": "PRIVATE_THOUGHT_MUST_NOT_BE_LOGGED"}}}})
			return
		}
		rejected := ticket()
		rejected.Return = "2026-11-12"
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "currency": "rub", "data": []offer{ticket(), rejected}})
	}))
	defer server.Close()
	a := &agent{trace: trace, client: deepSeekClient{key: "placeholder", url: server.URL + "/model", http: server.Client(), trace: trace}, travel: &travelClient{key: "placeholder", base: server.URL, http: server.Client(), cities: cities(), trace: trace}, now: testNow}
	answer, err := a.answer(context.Background(), "Найди билет из Казани в Сочи туда-обратно на 5–7 дней", func(string) {})
	if err != nil || !strings.Contains(answer, "15000") {
		t.Fatal("search failed")
	}
	records := readTraceRecords(t, trace.path)
	found := map[string]bool{}
	turn := ""
	for _, record := range records {
		event, _ := record["event"].(string)
		found[event] = true
		id, _ := record["turn_id"].(string)
		if id == "" {
			t.Fatal("missing correlation")
		}
		if turn != "" && id != turn {
			t.Fatal("events not correlated")
		}
		turn = id
	}
	for _, event := range []string{"agent.input", "deepseek.request", "deepseek.response", "model.plan", "agent.decision", "travel.request", "travel.response", "travel.filter", "agent.output"} {
		if !found[event] {
			t.Errorf("missing event %s", event)
		}
	}
	raw, _ := os.ReadFile(trace.path)
	if strings.Contains(string(raw), "placeholder") || strings.Contains(string(raw), "PRIVATE_THOUGHT_MUST_NOT_BE_LOGGED") {
		t.Fatal("sensitive content logged")
	}
	if !strings.Contains(string(raw), "длительность 2 дн. вместо 5–7") {
		t.Fatal("filter explanation missing")
	}
	info, _ := os.Stat(trace.path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("log permissions")
	}
}
func TestTraceRedactsSecretsAndNetworkIdentifiers(t *testing.T) {
	secret := "dummy-configured-value"
	trace, err := openTrace(t.TempDir(), []string{secret})
	if err != nil {
		t.Fatal(err)
	}
	defer trace.close()
	address := strings.Join([]string{"192", "168", "50", "9"}, ".")
	hostname := "example" + ".internal"
	nested := `{"reasoning_content":"PRIVATE_THOUGHT_MUST_NOT_BE_LOGGED","action":"ask"}`
	trace.event(context.Background(), "test", map[string]any{"Authorization": "Bearer " + secret, "body": "token=dummy_redaction_value " + secret + " " + address + " " + hostname, "content": nested})
	raw, _ := os.ReadFile(trace.path)
	for _, value := range []string{secret, "dummy_redaction_value", address, hostname, "PRIVATE_THOUGHT_MUST_NOT_BE_LOGGED"} {
		if strings.Contains(string(raw), value) {
			t.Fatal("redaction failed")
		}
	}
	if len(readTraceRecords(t, trace.path)) != 1 {
		t.Fatal("trace not valid JSONL")
	}
}
func TestTraceDoesNotLogErrorResponseBodies(t *testing.T) {
	trace, err := openTrace(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer trace.close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, "SENSITIVE_ERROR_MUST_NOT_BE_LOGGED")
	}))
	defer server.Close()
	c := &travelClient{base: server.URL, http: server.Client(), trace: trace}
	var out any
	_ = c.get(context.Background(), "/test", nil, &out, true)
	model := deepSeekClient{url: server.URL, http: server.Client(), trace: trace}
	_, _ = model.Plan(context.Background(), []message{{Role: "user", Content: "Билет"}})
	raw, _ := os.ReadFile(trace.path)
	if strings.Contains(string(raw), "SENSITIVE_ERROR_MUST_NOT_BE_LOGGED") {
		t.Fatal("error body logged")
	}
}
func TestTraceRotationAndConcurrentJSONLines(t *testing.T) {
	trace, err := openTrace(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer trace.close()
	trace.maxBytes = 700
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			trace.event(context.Background(), "parallel", map[string]string{"summary": "test event"})
		}()
	}
	wg.Wait()
	for _, path := range []string{trace.path, trace.path + ".1", trace.path + ".2"} {
		if len(readTraceRecords(t, path)) == 0 {
			t.Fatal("missing rotation data")
		}
	}
	if !strings.Contains(trace.status(), "запись включена") {
		t.Fatal("trace write failed")
	}
}
func TestTraceRejectsSymlinkAndCapsLargeEvents(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "trace.jsonl")); err != nil {
		t.Fatal(err)
	}
	if trace, err := openTrace(dir, nil); err == nil {
		trace.close()
		t.Fatal("symlink accepted")
	}
	trace, err := openTrace(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer trace.close()
	trace.event(context.Background(), "large", map[string]string{"text": strings.Repeat("я", 300000)})
	records := readTraceRecords(t, trace.path)
	data := records[0]["data"].(map[string]any)
	if data["truncated"] != true {
		t.Fatal("large event not bounded")
	}
}
func TestLogCommandReportsPathAndHasCompletion(t *testing.T) {
	trace, err := openTrace(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer trace.close()
	a := &agent{trace: trace}
	var out strings.Builder
	if err := run(context.Background(), a, strings.NewReader("/log\n/exit\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), trace.path) {
		t.Fatal("log path not displayed")
	}
	found := false
	for _, cmd := range commands {
		found = found || cmd.Value == "/log"
	}
	if !found {
		t.Fatal("log completion missing")
	}
}
