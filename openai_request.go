package main

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"time"
)

type requestOptions struct {
	Stream       bool
	IncludeUsage bool
}

type openAIChatRequest struct {
	Model               string          `json:"model"`
	Messages            []openAIMessage `json:"messages"`
	Tools               []openAITool    `json:"tools"`
	MaxTokens           *int64          `json:"max_tokens"`
	MaxCompletionTokens *int64          `json:"max_completion_tokens"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	ReasoningEffort     string          `json:"reasoning_effort"`
	Stream              bool            `json:"stream"`
	StreamOptions       struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls"`
	ToolCallID string           `json:"tool_call_id"`
	Name       string           `json:"name"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func translateOpenAIRequest(raw []byte, executorModel string, now time.Time, sessionID string) ([]byte, requestOptions, *rpcError) {
	var request openAIChatRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&request); err != nil {
		return nil, requestOptions{}, invalidRequest("invalid OpenAI chat request")
	}
	model := strings.TrimSpace(executorModel)
	if model == "" {
		model = strings.TrimSpace(request.Model)
	}
	if model == "" {
		return nil, requestOptions{}, invalidRequest("model is required")
	}
	if len(request.Messages) == 0 {
		return nil, requestOptions{}, invalidRequest("messages are required")
	}

	var system []string
	messages := make([]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "developer", "system":
			text, err := textContent(message.Content)
			if err != nil {
				return nil, requestOptions{}, err
			}
			if text != "" {
				system = append(system, text)
			}
		case "user":
			blocks, err := textBlocks(message.Content)
			if err != nil {
				return nil, requestOptions{}, err
			}
			messages = append(messages, map[string]any{"role": "user", "content": blocks})
		case "assistant":
			blocks, err := textBlocksOptional(message.Content)
			if err != nil {
				return nil, requestOptions{}, err
			}
			for _, call := range message.ToolCalls {
				if strings.TrimSpace(call.ID) == "" || call.Type != "function" || strings.TrimSpace(call.Function.Name) == "" {
					return nil, requestOptions{}, invalidRequest("assistant tool call is invalid")
				}
				input, argumentErr := parseToolArguments(call.Function.Arguments)
				if argumentErr != nil {
					return nil, requestOptions{}, argumentErr
				}
				blocks = append(blocks, map[string]any{
					"type": "tool-call", "toolCallId": call.ID,
					"toolName": call.Function.Name, "input": input,
				})
			}
			if len(blocks) == 0 {
				return nil, requestOptions{}, invalidRequest("assistant message has no supported content")
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
		case "tool":
			if strings.TrimSpace(message.ToolCallID) == "" {
				return nil, requestOptions{}, invalidRequest("tool_call_id is required")
			}
			text, err := textContent(message.Content)
			if err != nil {
				return nil, requestOptions{}, err
			}
			messages = append(messages, map[string]any{
				"role": "tool",
				"content": []any{map[string]any{
					"type": "tool-result", "toolCallId": message.ToolCallID,
					"toolName": strings.TrimSpace(message.Name),
					"output":   map[string]any{"type": "text", "value": text},
				}},
			})
		default:
			return nil, requestOptions{}, invalidRequest("unsupported message role")
		}
	}
	if len(messages) == 0 {
		return nil, requestOptions{}, invalidRequest("messages contain no user, assistant, or tool content")
	}

	params := map[string]any{
		"model": model, "messages": messages, "stream": true,
	}
	if len(system) > 0 {
		params["system"] = strings.Join(system, "\n\n")
	}
	if request.MaxCompletionTokens != nil {
		params["max_tokens"] = *request.MaxCompletionTokens
	} else if request.MaxTokens != nil {
		params["max_tokens"] = *request.MaxTokens
	}
	if request.Temperature != nil {
		params["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		params["top_p"] = *request.TopP
	}
	if effort := strings.TrimSpace(request.ReasoningEffort); effort != "" {
		params["reasoning_effort"] = effort
	}
	if len(request.Tools) > 0 {
		tools := make([]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if tool.Type != "function" || strings.TrimSpace(tool.Function.Name) == "" {
				return nil, requestOptions{}, invalidRequest("only function tools are supported")
			}
			var schema any = map[string]any{"type": "object"}
			if len(tool.Function.Parameters) > 0 {
				if !json.Valid(tool.Function.Parameters) || json.Unmarshal(tool.Function.Parameters, &schema) != nil {
					return nil, requestOptions{}, invalidRequest("tool parameters must be valid JSON Schema")
				}
			}
			tools = append(tools, map[string]any{
				"type": "function", "name": tool.Function.Name,
				"description": tool.Function.Description, "input_schema": schema,
			})
		}
		params["tools"] = tools
	}

	body := map[string]any{
		"config": map[string]any{
			"workingDir": "", "date": now.UTC().Format("2006-01-02"),
			"environment": runtime.GOOS + "-" + runtime.GOARCH,
			"structure":   []any{}, "isGitRepo": false,
			"currentBranch": "", "mainBranch": "", "gitStatus": "", "recentCommits": []any{},
		},
		"memory": "", "taste": nil, "skills": nil,
		"permissionMode": "standard", "threadId": sessionID, "params": params,
	}
	translated, err := json.Marshal(body)
	if err != nil {
		return nil, requestOptions{}, &rpcError{Code: "plugin_error", Message: "failed to encode upstream request", HTTPStatus: 500}
	}
	return translated, requestOptions{Stream: request.Stream, IncludeUsage: request.StreamOptions.IncludeUsage}, nil
}

func textContent(raw json.RawMessage) (string, *rpcError) {
	blocks, err := textBlocks(raw)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		parts = append(parts, block["text"].(string))
	}
	return strings.Join(parts, ""), nil
}

func textBlocksOptional(raw json.RawMessage) ([]map[string]any, *rpcError) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	return textBlocks(raw)
}

func textBlocks(raw json.RawMessage) ([]map[string]any, *rpcError) {
	if len(raw) == 0 || string(raw) == "null" {
		return []map[string]any{{"type": "text", "text": ""}}, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []map[string]any{{"type": "text", "text": text}}, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, invalidRequest("message content must be text")
	}
	blocks := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text", "input_text":
			blocks = append(blocks, map[string]any{"type": "text", "text": part.Text})
		default:
			return nil, invalidRequest("unsupported message content block")
		}
	}
	if len(blocks) == 0 {
		return nil, invalidRequest("message content is empty")
	}
	return blocks, nil
}

func parseToolArguments(raw json.RawMessage) (map[string]any, *rpcError) {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	var input map[string]any
	if len(raw) == 0 || !json.Valid(raw) || json.Unmarshal(raw, &input) != nil || input == nil {
		return nil, invalidRequest("assistant tool arguments must be a valid JSON object")
	}
	return input, nil
}

func invalidRequest(message string) *rpcError {
	return &rpcError{Code: "invalid_request", Message: message, HTTPStatus: 400}
}
