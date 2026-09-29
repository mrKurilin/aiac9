package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSchedulerUpdatesContinueAfterLargeMessage(t *testing.T) {
	large := SchedulerUpdate{Generation: 1, Events: []ScheduledEvent{{JobID: "R1", Text: strings.Repeat("а", 600000)}}}
	small := SchedulerUpdate{Generation: 1, Events: []ScheduledEvent{{JobID: "R2", Text: "следующее"}}}
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	if err := encoder.Encode(large); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(small); err != nil {
		t.Fatal(err)
	}
	if input.Len() <= 1<<20 {
		t.Fatalf("test message too small: %d", input.Len())
	}
	updates := make(chan SchedulerUpdate, 2)
	readSchedulerUpdates(&input, updates)
	first, ok := <-updates
	if !ok || len(first.Events) != 1 || first.Events[0].Text != large.Events[0].Text {
		t.Fatal("large update lost")
	}
	second, ok := <-updates
	if !ok || len(second.Events) != 1 || second.Events[0].Text != "следующее" {
		t.Fatal("subsequent update lost")
	}
}

func TestMCPRejectsListBeforeInitialized(t *testing.T) {
	in := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n")
	var out bytes.Buffer
	if err := serveGitLabMCP(in, &out, nil); err != nil {
		t.Fatal(err)
	}
	var response mcpMessage
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != -32000 {
		t.Fatalf("response=%+v", response)
	}
}

func TestMCPAcceptsExternalClientStringIDs(t *testing.T) {
	in := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":\"init-1\",\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"external\",\"version\":\"1\"}}}\n" +
		"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n" +
		"{\"jsonrpc\":\"2.0\",\"id\":\"list-1\",\"method\":\"tools/list\",\"params\":{}}\n")
	var out bytes.Buffer
	if err := serveGitLabMCP(in, &out, nil); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("responses=%q", out.String())
	}
	for index, want := range []string{`"init-1"`, `"list-1"`} {
		var response mcpMessage
		if err := json.Unmarshal(lines[index], &response); err != nil {
			t.Fatal(err)
		}
		if string(response.ID) != want || response.Error != nil {
			t.Fatalf("response=%+v", response)
		}
	}
}
