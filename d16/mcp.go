package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// MCP uses one JSON-RPC message per line on stdio.
type mcpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type MCPClient struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int
}

func StartLocalMCP(ctx context.Context) (*MCPClient, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, "--mcp-server")
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
	client := &MCPClient{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := client.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "mrkai", "version": "d16"},
	}, &initialized); err != nil {
		client.Close()
		return nil, err
	}
	if initialized.ProtocolVersion == "" {
		client.Close()
		return nil, fmt.Errorf("сервер не вернул protocolVersion")
	}
	if err := client.notify("notifications/initialized"); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

func (c *MCPClient) call(method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(c.stdin).Encode(mcpMessage{JSONRPC: "2.0", ID: &id, Method: method, Params: encoded}); err != nil {
		return fmt.Errorf("отправить %s: %w", method, err)
	}
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("прочитать ответ %s: %w", method, err)
		}
		var response mcpMessage
		if err := json.Unmarshal(line, &response); err != nil {
			return fmt.Errorf("неверный JSON MCP: %w", err)
		}
		if response.ID == nil { // server notification
			continue
		}
		if response.JSONRPC != "2.0" || *response.ID != id {
			return fmt.Errorf("неожиданный ответ MCP на %s", method)
		}
		if response.Error != nil {
			return fmt.Errorf("MCP %s: %s", method, response.Error.Message)
		}
		if len(response.Result) == 0 {
			return fmt.Errorf("MCP %s: пустой результат", method)
		}
		return json.Unmarshal(response.Result, result)
	}
}

func (c *MCPClient) notify(method string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return json.NewEncoder(c.stdin).Encode(mcpMessage{JSONRPC: "2.0", Method: method})
}

func (c *MCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var result struct {
		Tools []MCPTool `json:"tools"`
	}
	if err := c.call("tools/list", map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func (c *MCPClient) CallTool(ctx context.Context, name, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := c.call("tools/call", map[string]any{
		"name": name, "arguments": map[string]string{"text": text},
	}, &result); err != nil {
		return "", err
	}
	for _, item := range result.Content {
		if item.Type == "text" {
			return item.Text, nil
		}
	}
	return "", fmt.Errorf("MCP-инструмент %q не вернул текст", name)
}

func (c *MCPClient) Close() error {
	_ = c.stdin.Close()
	if c.cmd != nil {
		return c.cmd.Wait()
	}
	return nil
}

func printMCPTools(out io.Writer, tools []MCPTool) {
	fmt.Fprintf(out, "Инструменты MCP (%d):\n", len(tools))
	for _, tool := range tools {
		fmt.Fprintf(out, "  %s — %s\n", tool.Name, tool.Description)
	}
}

func serveMCP(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(out)
	initialized := false
	for scanner.Scan() {
		var request mcpMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return err
		}
		if request.JSONRPC != "2.0" {
			return fmt.Errorf("неподдерживаемый JSON-RPC")
		}
		if request.ID == nil {
			if request.Method == "notifications/initialized" {
				initialized = true
			}
			continue
		}
		response := mcpMessage{JSONRPC: "2.0", ID: request.ID}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "mrkai-local", "version": "d16"},
			}
		case "tools/list":
			if !initialized {
				response.Error = &mcpError{Code: -32000, Message: "клиент ещё не инициализирован"}
				break
			}
			result = map[string]any{"tools": []MCPTool{
				{Name: "echo", Description: "Повторяет переданный текст", InputSchema: textSchema()},
				{Name: "uppercase", Description: "Переводит текст в верхний регистр", InputSchema: textSchema()},
			}}
		case "tools/call":
			var params struct {
				Name      string `json:"name"`
				Arguments struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			if !initialized || json.Unmarshal(request.Params, &params) != nil || (params.Name != "echo" && params.Name != "uppercase") {
				response.Error = &mcpError{Code: -32602, Message: "неверный вызов инструмента"}
				break
			}
			value := params.Arguments.Text
			if params.Name == "uppercase" {
				value = strings.ToUpper(value)
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": value}}}
		default:
			response.Error = &mcpError{Code: -32601, Message: "метод не найден"}
		}
		if response.Error == nil {
			response.Result, _ = json.Marshal(result)
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func textSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
}
