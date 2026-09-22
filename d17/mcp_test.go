package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

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
