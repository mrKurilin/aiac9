package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	maxToolRounds   = 3
	toolInstruction = `Тебе доступны MCP-инструменты сервера mrkGitlab. ` +
		`Доступ к GitLab пользователя уже настроен: не проси у пользователя токен, URL или список MR. ` +
		`Если для ответа нужны данные GitLab, например открытые merge requests, сначала вызови подходящий инструмент и не выдумывай данные. ` +
		`Получив результат инструмента, ответь по нему в JSON-формате, указанном выше.`
)

type completeFunc func(context.Context, []Message, func(string) error) (string, error)

func (a *Agent) completeTurn(ctx context.Context, messages []Message, emit func(string) error, progress func(string), fallback completeFunc) (string, error) {
	toolClient, ok := a.client.(ToolChatClient)
	if !ok || a.gitlab == nil {
		return fallback(ctx, messages, emit)
	}
	tools, err := a.gitlab.ListTools(ctx)
	if err != nil || len(tools) == 0 {
		return fallback(ctx, messages, emit)
	}
	definitions := toolDefinitions(tools)
	messages = withToolInstruction(messages)
	for round := 0; round < maxToolRounds; round++ {
		reply, err := toolClient.CompleteWithTools(ctx, messages, definitions)
		if err != nil {
			return "", err
		}
		if len(reply.ToolCalls) == 0 {
			return reply.Content, nil
		}
		messages = append(messages, Message{
			Role:      "assistant",
			Content:   reply.Content,
			ToolCalls: reply.ToolCalls,
		})
		for _, call := range reply.ToolCalls {
			result := a.runToolCall(ctx, call, progress)
			if err := ctx.Err(); err != nil {
				return "", err
			}
			messages = append(messages, Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: call.ID,
			})
		}
	}
	reply, err := toolClient.CompleteWithTools(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return reply.Content, nil
}

func withToolInstruction(messages []Message) []Message {
	result := make([]Message, 0, len(messages)+1)
	if len(messages) == 0 {
		return append(result, Message{Role: "system", Content: toolInstruction})
	}
	result = append(result, messages[:len(messages)-1]...)
	result = append(result, Message{Role: "system", Content: toolInstruction})
	return append(result, messages[len(messages)-1])
}

func (a *Agent) runToolCall(ctx context.Context, call ToolCall, progress func(string)) string {
	report := func(message string) {
		if progress != nil {
			progress(message)
		}
	}
	name := call.Function.Name
	arguments := strings.TrimSpace(call.Function.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	report(fmt.Sprintf("Агент вызывает mrkGitlab.%s %s", name, arguments))
	result, err := a.gitlab.CallJSONToolWithProgress(ctx, name, json.RawMessage(arguments), func(message string) {
		report("  ↳ " + message)
	})
	if err != nil {
		report(fmt.Sprintf("mrkGitlab.%s: ошибка — %v", name, err))
		return "Ошибка инструмента: " + err.Error()
	}
	report(fmt.Sprintf("mrkGitlab.%s: результат получен и передан модели", name))
	return result
}
