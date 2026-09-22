package main

import (
	"context"
	"encoding/json"
)

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDefinition struct {
	Type     string             `json:"type"`
	Function ToolFunctionSchema `json:"function"`
}

type ToolFunctionSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolChatClient interface {
	CompleteWithTools(context.Context, []Message, []ToolDefinition) (Message, error)
}

func toolDefinitions(tools []MCPTool) []ToolDefinition {
	definitions := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.InputSchema
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		definitions = append(definitions, ToolDefinition{
			Type: "function",
			Function: ToolFunctionSchema{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	return definitions
}
