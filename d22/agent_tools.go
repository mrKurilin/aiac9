package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const maxToolRounds = 16
const maxToolCalls = 48

type completeFunc func(context.Context, []Message, func(string) error) (string, error)

type mcpToolRoute struct {
	client   *MCPClient
	server   string
	name     string
	runLocal func(context.Context, json.RawMessage, func(string)) (string, error)
}

// One registry supplies discovery, routing and the terminal server list.
type mcpServer struct {
	name   string
	client *MCPClient
}

func (a *Agent) mcpServers() []mcpServer {
	return []mcpServer{{"mrkGitlab", a.gitlab}, {"mrkScheduler", a.scheduler}, {"mrkPipeline", a.pipeline}}
}

// Scope each tool name by server so equally named tools remain distinguishable.
func modelToolName(server, name string) string {
	return "mcp__" + strings.ToLower(server) + "__" + name
}

func (a *Agent) availableTools(ctx context.Context) ([]ToolDefinition, map[string]mcpToolRoute, error) {
	definitions := []ToolDefinition{}
	routes := map[string]mcpToolRoute{}
	for _, server := range a.mcpServers() {
		if server.client == nil {
			continue
		}
		tools, err := server.client.ListTools(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("список инструментов %s: %w", server.name, err)
		}
		for _, tool := range tools {
			alias := modelToolName(server.name, tool.Name)
			if _, exists := routes[alias]; exists {
				return nil, nil, fmt.Errorf("повторное имя MCP-инструмента %q", alias)
			}
			definition := toolDefinitions([]MCPTool{tool})[0]
			definition.Function.Name = alias
			definitions = append(definitions, definition)
			routes[alias] = mcpToolRoute{client: server.client, server: server.name, name: tool.Name}
		}
	}
	if a.githubReadmes != nil {
		name := "index_github_readmes"
		definitions = append(definitions, ToolDefinition{Type: "function", Function: ToolFunctionSchema{
			Name:        name,
			Description: "Найти публичные GitHub-репозитории по указанной теме, скачать их README, сохранить локально и построить два индекса с эмбеддингами (fixed и structure). Если пользователь указал число репозиториев, передай его в repositories; иначе сбор завершится после 10000 слов или 40 README.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"Тема поиска репозиториев на GitHub"},"repositories":{"type":"integer","minimum":1,"maximum":40,"description":"Сколько README скачать, если пользователь указал число"}},"required":["topic"],"additionalProperties":false}`),
		}})
		routes[name] = mcpToolRoute{server: "GitHub", name: name, runLocal: func(ctx context.Context, raw json.RawMessage, progress func(string)) (string, error) {
			var args struct {
				Topic        string `json:"topic"`
				Repositories int    `json:"repositories"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("неверные аргументы: %w", err)
			}
			result, err := a.IndexGitHubReadmes(ctx, args.Topic, progress, args.Repositories)
			if err != nil {
				return "", err
			}
			data, err := json.Marshal(result)
			return string(data), err
		}}
	}
	return definitions, routes, nil
}

func (a *Agent) completeTurn(ctx context.Context, messages []Message, emit func(string) error, progress func(string), fallback completeFunc) (string, error) {
	toolClient, ok := a.client.(ToolChatClient)
	if !ok || (a.gitlab == nil && a.scheduler == nil && a.pipeline == nil && a.githubReadmes == nil) {
		return fallback(ctx, messages, emit)
	}
	if progress != nil {
		progress("MCP: обнаружение инструментов подключённых серверов")
	}
	definitions, routes, err := a.availableTools(ctx)
	if err != nil {
		return "", err
	}
	if len(definitions) == 0 {
		return fallback(ctx, messages, emit)
	}
	if progress != nil {
		progress(fmt.Sprintf("MCP: доступно инструментов: %d", len(definitions)))
	}
	calls := 0
	for round := 0; round < maxToolRounds; round++ {
		if progress != nil {
			progress(fmt.Sprintf("MCP: шаг модели %d/%d", round+1, maxToolRounds))
		}
		var reply Message
		if streaming, ok := toolClient.(StreamingToolChatClient); ok && emit != nil {
			decoder := &answerStreamDecoder{}
			reply, err = streaming.CompleteWithToolsStream(ctx, messages, definitions, func(chunk string) error {
				return decoder.feed(chunk, emit)
			})
		} else {
			reply, err = toolClient.CompleteWithTools(ctx, messages, definitions)
		}
		if err != nil {
			return "", err
		}
		if len(reply.ToolCalls) == 0 {
			return reply.Content, nil
		}
		messages = append(messages, Message{Role: "assistant", Content: reply.Content, ToolCalls: reply.ToolCalls})
		for _, call := range reply.ToolCalls {
			if calls >= maxToolCalls {
				return "", fmt.Errorf("превышен предел вызовов MCP-инструментов (%d)", maxToolCalls)
			}
			calls++
			result := a.runToolCall(ctx, call, routes, progress)
			if err := ctx.Err(); err != nil {
				return "", err
			}
			messages = append(messages, Message{Role: "tool", Content: result, ToolCallID: call.ID})
		}
	}
	return "", fmt.Errorf("превышен предел последовательных вызовов MCP-инструментов (%d)", maxToolRounds)
}

func (a *Agent) runToolCall(ctx context.Context, call ToolCall, routes map[string]mcpToolRoute, progress func(string)) string {
	route, ok := routes[call.Function.Name]
	if !ok {
		return "Ошибка инструмента: неизвестный MCP-инструмент"
	}
	arguments := strings.TrimSpace(call.Function.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	if progress != nil {
		progress(fmt.Sprintf("%s.%s: вызываю инструмент", route.server, route.name))
	}
	toolProgress := func(message string) {
		if progress != nil {
			progress("  ↳ " + message)
		}
	}
	var result string
	var err error
	if route.runLocal != nil {
		result, err = route.runLocal(ctx, json.RawMessage(arguments), toolProgress)
	} else {
		result, err = route.client.CallJSONToolWithProgress(ctx, route.name, json.RawMessage(arguments), toolProgress)
	}
	if err != nil {
		if progress != nil {
			progress(fmt.Sprintf("%s.%s: ошибка инструмента", route.server, route.name))
		}
		return "Ошибка инструмента: " + err.Error()
	}
	if progress != nil {
		progress(fmt.Sprintf("%s.%s: результат получен и передан модели", route.server, route.name))
	}
	return result
}
