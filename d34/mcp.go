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
	cmd := exec.CommandContext(ctx, exe, "--project-mcp-server", root)
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
	if err := c.call(ctx, "initialize", map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "mrkai", "version": "d31"}}, &initialized); err != nil {
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
	_ = c.cmd.Wait()
}

func (c *mcpClient) call(ctx context.Context, method string, params any, result any) error {
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
		_ = c.cmd.Process.Kill()
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
	return result.Content[0].Text, nil
}

func serveMCP(root string, in io.Reader, out io.Writer) error {
	decoder := json.NewDecoder(in)
	encoder := json.NewEncoder(out)
	tools := []mcpTool{
		{"git_branch", "Получить текущую git ветку проекта", json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		{"file_list", "Список файлов проекта", json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		{"file_read", "Прочитать файл проекта", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)},
		{"file_search", "Искать строку во многих файлах проекта", json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)},
		{"file_write", "Создать или обновить Markdown файл в docs", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)},
	}
	for {
		var req rpcMessage
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if req.ID == nil {
			continue
		}
		answer := rpcMessage{JSONRPC: "2.0", ID: req.ID}
		var value any
		switch req.Method {
		case "initialize":
			value = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "project", "version": "1.0"}}
		case "tools/list":
			value = map[string]any{"tools": tools}
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(req.Params, &params) != nil {
				answer.Error = &rpcError{-32602, "неверное имя или аргументы инструмента"}
				break
			}
			var args struct {
				Path    string `json:"path"`
				Query   string `json:"query"`
				Content string `json:"content"`
			}
			if len(params.Arguments) > 0 && json.Unmarshal(params.Arguments, &args) != nil {
				answer.Error = &rpcError{-32602, "неверные аргументы"}
				break
			}
			var result string
			var err error
			switch params.Name {
			case "git_branch":
				result, err = gitBranch(root)
			case "file_list":
				result, err = projectList(context.Background(), root)
			case "file_read":
				result, err = projectRead(root, args.Path)
			case "file_search":
				result, err = projectSearch(context.Background(), root, args.Query)
			case "file_write":
				result, err = projectWrite(root, args.Path, args.Content)
			default:
				answer.Error = &rpcError{-32602, "неизвестный инструмент"}
			}
			if answer.Error != nil {
				break
			}
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

func gitBranch(root string) (string, error) {
	output, err := exec.Command("git", "-C", root, "branch", "--show-current").Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(output))
	if branch == "" {
		return "HEAD (detached)", nil
	}
	return branch, nil
}
