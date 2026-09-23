package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

type streamedToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (c *DeepSeekClient) CompleteWithToolsStream(ctx context.Context, messages []Message, tools []ToolDefinition, emit func(string) error) (Message, error) {
	request := map[string]any{"model": c.Model, "messages": messages, "stream": true}
	if len(tools) > 0 {
		request["tools"] = tools
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Message{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Message{}, fmt.Errorf("вызвать DeepSeek API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return Message{}, fmt.Errorf("DeepSeek API, HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var content strings.Builder
	calls := map[int]*ToolCall{}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	var dataLines []string
	done := false
	processEvent := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = nil
		if payload == "[DONE]" {
			done = true
			return nil
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content   string             `json:"content"`
					ToolCalls []streamedToolCall `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return fmt.Errorf("разобрать фрагмент DeepSeek: %w", err)
		}
		if event.Error != nil {
			return fmt.Errorf("DeepSeek API: %s", event.Error.Message)
		}
		for _, choice := range event.Choices {
			if chunk := choice.Delta.Content; chunk != "" {
				content.WriteString(chunk)
				if emit != nil {
					if err := emit(chunk); err != nil {
						return err
					}
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				call := calls[delta.Index]
				if call == nil {
					call = &ToolCall{}
					calls[delta.Index] = call
				}
				call.ID += delta.ID
				call.Type += delta.Type
				call.Function.Name += delta.Function.Name
				call.Function.Arguments += delta.Function.Arguments
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := processEvent(); err != nil {
				return Message{}, err
			}
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return Message{}, fmt.Errorf("читать поток DeepSeek: %w", err)
	}
	if !done {
		return Message{}, fmt.Errorf("поток DeepSeek оборвался до [DONE]")
	}
	reply := Message{Role: "assistant", Content: content.String()}
	indices := make([]int, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		reply.ToolCalls = append(reply.ToolCalls, *calls[index])
	}
	if reply.Content == "" && len(reply.ToolCalls) == 0 {
		return Message{}, fmt.Errorf("DeepSeek вернул пустой ответ")
	}
	return reply, nil
}
