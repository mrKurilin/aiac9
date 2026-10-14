package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const protocolVersion = "2025-06-18"

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpClient struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	id     int
}

func startMCP(ctx context.Context, root string) (*mcpClient, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, "--support-mcp-server", root)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &mcpClient{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := c.call(ctx, "initialize", map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "mrkai", "version": "d33"}}, &initialized); err != nil {
		c.close()
		return nil, err
	}
	if initialized.ProtocolVersion != protocolVersion {
		c.close()
		return nil, fmt.Errorf("неподдерживаемая версия MCP")
	}
	if err := json.NewEncoder(c.stdin).Encode(rpcMessage{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *mcpClient) close() {
	_ = c.stdin.Close()
	if c.cmd != nil {
		_ = c.cmd.Wait()
	}
}

func (c *mcpClient) call(ctx context.Context, method string, params any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.id++
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(c.stdin).Encode(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(c.id)), Method: method, Params: paramsJSON}); err != nil {
		return err
	}
	type response struct {
		msg rpcMessage
		err error
	}
	read := make(chan response, 1)
	go func() {
		var msg rpcMessage
		err := json.NewDecoder(c.stdout).Decode(&msg)
		read <- response{msg, err}
	}()
	var got response
	select {
	case got = <-read:
	case <-ctx.Done():
		if c.cmd != nil {
			_ = c.cmd.Process.Kill()
		} else {
			_ = c.stdin.Close()
		}
		<-read
		return ctx.Err()
	}
	if got.err != nil {
		return got.err
	}
	if string(got.msg.ID) != strconv.Itoa(c.id) {
		return fmt.Errorf("неверный ID ответа MCP")
	}
	if got.msg.Error != nil {
		return fmt.Errorf("MCP %s: %s", method, got.msg.Error.Message)
	}
	return json.Unmarshal(got.msg.Result, result)
}

func (c *mcpClient) listTools(ctx context.Context) ([]mcpTool, error) {
	var result struct {
		Tools []mcpTool `json:"tools"`
	}
	err := c.call(ctx, "tools/list", map[string]any{}, &result)
	return result.Tools, err
}

func (c *mcpClient) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	var arguments map[string]any
	if err := json.Unmarshal(args, &arguments); err != nil || arguments == nil {
		return "", fmt.Errorf("аргументы инструмента должны быть JSON объектом")
	}
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &result); err != nil {
		return "", err
	}
	if len(result.Content) == 0 || result.Content[0].Type != "text" {
		return "", fmt.Errorf("MCP инструмент не вернул текст")
	}
	if result.IsError {
		return "", fmt.Errorf("%s", result.Content[0].Text)
	}
	var texts []string
	for _, item := range result.Content {
		if item.Type == "text" {
			texts = append(texts, item.Text)
		}
	}
	return strings.Join(texts, "\n"), nil
}

func serveMCP(root string, in io.Reader, out io.Writer) error {
	decoder := json.NewDecoder(in)
	encoder := json.NewEncoder(out)
	ticketTool := mcpTool{"support_ticket", "Получить данные тикета и пользователя по ID для ответа поддержки", json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)}
	initialized, ready := false, false
	for {
		var req rpcMessage
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if req.ID == nil {
			if req.Method == "notifications/initialized" && initialized {
				ready = true
			}
			continue
		}
		answer := rpcMessage{JSONRPC: "2.0", ID: req.ID}
		var value any
		switch req.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if json.Unmarshal(req.Params, &params) != nil || params.ProtocolVersion != protocolVersion {
				answer.Error = &rpcError{-32602, "поддерживается MCP " + protocolVersion}
				break
			}
			initialized = true
			value = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "support", "version": "1.0"}}
		case "tools/list":
			if !ready {
				answer.Error = &rpcError{-32600, "MCP не инициализирован"}
				break
			}
			value = map[string]any{"tools": []mcpTool{ticketTool}}
		case "tools/call":
			if !ready {
				answer.Error = &rpcError{-32600, "MCP не инициализирован"}
				break
			}
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(req.Params, &params) != nil || params.Name != ticketTool.Name {
				answer.Error = &rpcError{-32602, "неверное имя или аргументы инструмента"}
				break
			}
			var result string
			var err error
			var args struct {
				ID string `json:"id"`
			}
			decoder := json.NewDecoder(strings.NewReader(string(params.Arguments)))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&args) != nil || args.ID == "" {
				answer.Error = &rpcError{-32602, "нужен ID тикета; дополнительные поля не поддерживаются"}
				break
			}
			result, err = supportTicket(root, args.ID)
			if err != nil {
				value = map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": err.Error()}}}
			} else {
				value = map[string]any{"content": []map[string]string{{"type": "text", "text": result}}}
			}
		default:
			answer.Error = &rpcError{-32601, "неизвестный метод"}
		}
		if answer.Error == nil {
			answer.Result, _ = json.Marshal(value)
		}
		if err := encoder.Encode(answer); err != nil {
			return err
		}
	}
}
