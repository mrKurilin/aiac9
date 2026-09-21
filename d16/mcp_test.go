package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestMCPInitializeAndListTools(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		defer serverOut.Close()
		serverDone <- serveMCP(serverIn, serverOut)
	}()
	client := &MCPClient{stdin: clientOut, stdout: bufio.NewReader(clientIn)}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := client.call("initialize", map[string]any{"protocolVersion": "2025-06-18"}, &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.ProtocolVersion != "2025-06-18" {
		t.Fatalf("version=%q", initialized.ProtocolVersion)
	}
	if err := client.notify("notifications/initialized"); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "echo" || tools[1].Name != "uppercase" {
		t.Fatalf("tools=%+v", tools)
	}
	var schema struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(tools[0].InputSchema, &schema); err != nil || schema.Type != "object" {
		t.Fatalf("schema=%s err=%v", tools[0].InputSchema, err)
	}
	var display bytes.Buffer
	printMCPTools(&display, tools)
	if !strings.Contains(display.String(), "echo — Повторяет переданный текст") {
		t.Fatalf("output=%q", display.String())
	}
	got, err := client.CallTool(context.Background(), "uppercase", "Привет, MCP")
	if err != nil || got != "ПРИВЕТ, MCP" {
		t.Fatalf("call result=%q err=%v", got, err)
	}
	clientOut.Close()
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestMCPRejectsListBeforeInitialized(t *testing.T) {
	in := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n")
	var out bytes.Buffer
	if err := serveMCP(in, &out); err != nil {
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
