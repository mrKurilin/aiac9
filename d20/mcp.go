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
	"sync"
)

// MCP uses one JSON-RPC message per line on stdio.
type mcpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
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
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	nextID  int
	updates <-chan SchedulerUpdate
}

func StartSchedulerMCP(ctx context.Context) (*MCPClient, error) {
	return startMCPProcess(ctx, "--scheduler-mcp-server")
}

func StartGitLabMCP(ctx context.Context) (*MCPClient, error) {
	return startMCPProcess(ctx, "--gitlab-mcp-server")
}

func StartPipelineMCP(ctx context.Context) (*MCPClient, error) {
	return startMCPProcess(ctx, "--pipeline-mcp-server")
}

func startMCPProcess(ctx context.Context, mode string) (*MCPClient, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, mode)
	var schedulerStderr io.ReadCloser
	if mode == "--scheduler-mcp-server" {
		schedulerStderr, err = cmd.StderrPipe()
		if err != nil {
			return nil, err
		}
	}
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
	if schedulerStderr != nil {
		updates := make(chan SchedulerUpdate, 256)
		client.updates = updates
		go readSchedulerUpdates(schedulerStderr, updates)
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := client.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "mrkai", "version": "d20"},
	}, &initialized); err != nil {
		client.Close()
		return nil, err
	}
	if initialized.ProtocolVersion != "2025-06-18" {
		client.Close()
		return nil, fmt.Errorf("сервер вернул неподдерживаемую версию MCP: %s", initialized.ProtocolVersion)
	}
	if err := client.notify("notifications/initialized"); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

func readSchedulerUpdates(in io.Reader, updates chan SchedulerUpdate) {
	defer close(updates)
	decoder := json.NewDecoder(in)
	for {
		var update SchedulerUpdate
		if err := decoder.Decode(&update); err != nil {
			return
		}
		select {
		case updates <- update:
		default:
			// Preserve the newest result if the terminal has been busy for a long time.
			select {
			case <-updates:
			default:
			}
			updates <- update
		}
	}
}

func (c *MCPClient) CallJSONTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	return c.CallJSONToolWithProgress(ctx, name, arguments, nil)
}

func (c *MCPClient) CallJSONToolWithProgress(ctx context.Context, name string, arguments json.RawMessage, progress func(string)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var args map[string]any
	if err := json.Unmarshal(arguments, &args); err != nil || args == nil {
		return "", fmt.Errorf("аргументы MCP должны быть JSON-объектом")
	}
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := c.callWithProgress(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &result, progress); err != nil {
		return "", err
	}
	for _, item := range result.Content {
		if item.Type == "text" {
			if result.IsError {
				return "", fmt.Errorf("MCP-инструмент %q: %s", name, item.Text)
			}
			return item.Text, nil
		}
	}
	return "", fmt.Errorf("MCP-инструмент %q не вернул текст", name)
}

func (c *MCPClient) call(method string, params any, result any) error {
	return c.callWithProgress(context.Background(), method, params, result, nil)
}

func (c *MCPClient) callWithProgress(ctx context.Context, method string, params any, result any, progress func(string)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(c.stdin).Encode(mcpMessage{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(id)), Method: method, Params: encoded}); err != nil {
		return fmt.Errorf("отправить %s: %w", method, err)
	}
	for {
		type readResult struct {
			line []byte
			err  error
		}
		read := make(chan readResult, 1)
		go func() {
			line, err := c.stdout.ReadBytes('\n')
			read <- readResult{line: line, err: err}
		}()
		var received readResult
		select {
		case received = <-read:
		case <-ctx.Done():
			cancelParams, _ := json.Marshal(map[string]any{"requestId": id, "reason": "отменено пользователем"})
			_ = json.NewEncoder(c.stdin).Encode(mcpMessage{JSONRPC: "2.0", Method: "notifications/cancelled", Params: cancelParams})
			received = <-read
			for {
				if received.err != nil {
					return ctx.Err()
				}
				var cancelledResponse mcpMessage
				if json.Unmarshal(received.line, &cancelledResponse) == nil && cancelledResponse.ID != nil && string(cancelledResponse.ID) == strconv.Itoa(id) {
					return ctx.Err()
				}
				line, err := c.stdout.ReadBytes('\n')
				received = readResult{line: line, err: err}
			}
		}
		if received.err != nil {
			return fmt.Errorf("прочитать ответ %s: %w", method, received.err)
		}
		var response mcpMessage
		if err := json.Unmarshal(received.line, &response); err != nil {
			return fmt.Errorf("неверный JSON MCP: %w", err)
		}
		if response.ID == nil { // server notification
			if progress != nil && response.Method == "notifications/message" {
				var notification struct {
					Data string `json:"data"`
				}
				if json.Unmarshal(response.Params, &notification) == nil && notification.Data != "" {
					progress(notification.Data)
				}
			}
			continue
		}
		if response.JSONRPC != "2.0" || string(response.ID) != strconv.Itoa(id) {
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
	if err := c.callWithProgress(ctx, "tools/list", map[string]any{}, &result, nil); err != nil {
		return nil, err
	}
	return result.Tools, nil
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
