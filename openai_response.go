package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const maxEventBytes = 8 * 1024 * 1024

type responseState struct {
	id           string
	created      int64
	model        string
	includeUsage bool
	buffer       []byte
	roleEmitted  bool
	terminalSeen bool
	terminalSent bool
	doneSent     bool
	failed       *rpcError
	text         strings.Builder
	reasoning    strings.Builder
	finishReason string
	usage        *openAIUsage
	tools        map[string]*responseTool
	toolOrder    []string
}

type responseTool struct {
	ID          string
	Name        string
	Arguments   strings.Builder
	Index       int
	Incremental bool
}

type upstreamEvent struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	ToolCallID   string          `json:"toolCallId"`
	ToolName     string          `json:"toolName"`
	Text         string          `json:"text"`
	Delta        string          `json:"delta"`
	FinishReason string          `json:"finishReason"`
	Input        json.RawMessage `json:"input"`
	Usage        json.RawMessage `json:"usage"`
	TotalUsage   json.RawMessage `json:"totalUsage"`
	Error        json.RawMessage `json:"error"`
	Message      string          `json:"message"`
}

type upstreamUsage struct {
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	TotalTokens       int64 `json:"totalTokens"`
	InputTokenDetails struct {
		CacheReadTokens  int64 `json:"cacheReadTokens"`
		CacheWriteTokens int64 `json:"cacheWriteTokens"`
	} `json:"inputTokenDetails"`
	OutputTokenDetails struct {
		ReasoningTokens int64 `json:"reasoningTokens"`
	} `json:"outputTokenDetails"`
}

type openAIUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type openAIChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []openAIChunkChoice `json:"choices"`
	Usage   *openAIUsage        `json:"usage,omitempty"`
}

type openAIChunkChoice struct {
	Index        int            `json:"index"`
	Delta        map[string]any `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

func newResponseState(id string, created int64, model string, includeUsage bool) *responseState {
	return &responseState{
		id:           id,
		created:      created,
		model:        model,
		includeUsage: includeUsage,
		tools:        make(map[string]*responseTool),
	}
}

func (s *responseState) Feed(chunk []byte) ([][]byte, *rpcError) {
	if s.failed != nil {
		return nil, s.failed
	}
	if s.doneSent || len(chunk) == 0 {
		return nil, nil
	}
	s.buffer = append(s.buffer, chunk...)
	var frames [][]byte
	for {
		index := bytes.IndexByte(s.buffer, '\n')
		if index < 0 {
			if len(s.buffer) > maxEventBytes {
				return nil, s.failProtocol("CommandCode event exceeds size limit")
			}
			return frames, nil
		}
		if index > maxEventBytes {
			return nil, s.failProtocol("CommandCode event exceeds size limit")
		}
		line := append([]byte(nil), s.buffer[:index]...)
		s.buffer = s.buffer[index+1:]
		lineFrames, err := s.consumeLine(line)
		if err != nil {
			return frames, err
		}
		frames = append(frames, lineFrames...)
	}
}

func (s *responseState) Finish() ([][]byte, *rpcError) {
	if s.failed != nil {
		return nil, s.failed
	}
	if s.doneSent {
		return nil, nil
	}
	var frames [][]byte
	if len(bytes.TrimSpace(s.buffer)) > 0 {
		if len(s.buffer) > maxEventBytes {
			return nil, s.failProtocol("CommandCode event exceeds size limit")
		}
		lineFrames, err := s.consumeLine(s.buffer)
		if err != nil {
			return nil, err
		}
		frames = append(frames, lineFrames...)
	}
	s.buffer = nil
	if !s.terminalSeen {
		return nil, s.failProtocol("CommandCode stream ended before a terminal event")
	}
	if !s.terminalSent {
		terminal, err := s.terminalFrame()
		if err != nil {
			return nil, err
		}
		frames = append(frames, terminal)
	}
	if !s.doneSent {
		frames = append(frames, []byte("data: [DONE]\n\n"))
		s.doneSent = true
	}
	return frames, nil
}

func (s *responseState) Completion() ([]byte, *rpcError) {
	if s.failed != nil {
		return nil, s.failed
	}
	if !s.terminalSeen {
		return nil, s.failProtocol("CommandCode completion is not terminal")
	}
	message := map[string]any{
		"role":    "assistant",
		"content": s.text.String(),
	}
	if s.reasoning.Len() > 0 {
		message["reasoning_content"] = s.reasoning.String()
	}
	if len(s.toolOrder) > 0 {
		calls := make([]any, 0, len(s.toolOrder))
		for _, id := range s.toolOrder {
			tool := s.tools[id]
			calls = append(calls, map[string]any{
				"id":   tool.ID,
				"type": "function",
				"function": map[string]any{
					"name": tool.Name, "arguments": tool.Arguments.String(),
				},
			})
		}
		message["tool_calls"] = calls
	}
	body := map[string]any{
		"id": s.id, "object": "chat.completion", "created": s.created, "model": s.model,
		"choices": []any{map[string]any{
			"index": 0, "message": message, "finish_reason": s.normalizedFinishReason(),
		}},
	}
	if s.usage != nil {
		body["usage"] = s.usage
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, &rpcError{Code: "plugin_error", Message: "failed to encode completion", HTTPStatus: 500}
	}
	return raw, nil
}

func (s *responseState) consumeLine(line []byte) ([][]byte, *rpcError) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil, nil
	}
	var event upstreamEvent
	if err := json.Unmarshal(trimmed, &event); err != nil || strings.TrimSpace(event.Type) == "" {
		return nil, s.failProtocol("invalid CommandCode event")
	}
	return s.consumeEvent(event)
}

func (s *responseState) consumeEvent(event upstreamEvent) ([][]byte, *rpcError) {
	if s.terminalSent {
		return nil, nil
	}
	switch event.Type {
	case "text-delta":
		text := event.Text
		if text == "" {
			text = event.Delta
		}
		if text == "" {
			return nil, nil
		}
		s.text.WriteString(text)
		return [][]byte{s.deltaFrame(map[string]any{"content": text})}, nil
	case "reasoning-delta":
		text := event.Text
		if text == "" {
			text = event.Delta
		}
		if text == "" {
			return nil, nil
		}
		s.reasoning.WriteString(text)
		return [][]byte{s.deltaFrame(map[string]any{"reasoning_content": text})}, nil
	case "tool-input-start":
		id := firstNonEmptyString(event.ID, event.ToolCallID)
		if id == "" {
			return nil, s.failProtocol("tool input start is missing an id")
		}
		tool := s.ensureTool(id, event.ToolName)
		tool.Incremental = true
		delta := map[string]any{
			"tool_calls": []any{map[string]any{
				"index": tool.Index, "id": tool.ID, "type": "function",
				"function": map[string]any{"name": tool.Name, "arguments": ""},
			}},
		}
		return [][]byte{s.deltaFrame(delta)}, nil
	case "tool-input-delta":
		id := firstNonEmptyString(event.ID, event.ToolCallID)
		tool := s.tools[id]
		if tool == nil {
			return nil, s.failProtocol("tool input delta references an unknown id")
		}
		tool.Arguments.WriteString(event.Delta)
		delta := map[string]any{
			"tool_calls": []any{map[string]any{
				"index":    tool.Index,
				"function": map[string]any{"arguments": event.Delta},
			}},
		}
		return [][]byte{s.deltaFrame(delta)}, nil
	case "tool-input-end":
		return nil, nil
	case "tool-call":
		id := firstNonEmptyString(event.ToolCallID, event.ID)
		if id == "" {
			return nil, s.failProtocol("tool call is missing an id")
		}
		if existing := s.tools[id]; existing != nil && existing.Incremental {
			if existing.Name == "" {
				existing.Name = event.ToolName
			}
			return nil, nil
		}
		arguments, err := normalizeToolInput(event.Input)
		if err != nil {
			return nil, s.failProtocol("tool call input is invalid")
		}
		tool := s.ensureTool(id, event.ToolName)
		tool.Arguments.WriteString(arguments)
		delta := map[string]any{
			"tool_calls": []any{map[string]any{
				"index": tool.Index, "id": tool.ID, "type": "function",
				"function": map[string]any{"name": tool.Name, "arguments": arguments},
			}},
		}
		return [][]byte{s.deltaFrame(delta)}, nil
	case "finish-step":
		s.captureFinish(event.FinishReason, event.Usage)
		s.terminalSeen = true
		return nil, nil
	case "finish":
		usage := event.TotalUsage
		if len(usage) == 0 {
			usage = event.Usage
		}
		s.captureFinish(event.FinishReason, usage)
		s.terminalSeen = true
		frame, err := s.terminalFrame()
		if err != nil {
			return nil, err
		}
		return [][]byte{frame}, nil
	case "error":
		message := upstreamErrorMessage(event)
		s.failed = &rpcError{Code: "upstream_error", Message: message, HTTPStatus: 502}
		s.terminalSeen = true
		return nil, s.failed
	default:
		return nil, nil
	}
}

func (s *responseState) deltaFrame(delta map[string]any) []byte {
	if !s.roleEmitted {
		delta["role"] = "assistant"
		s.roleEmitted = true
	}
	return sseJSON(openAIChunk{
		ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model,
		Choices: []openAIChunkChoice{{Index: 0, Delta: delta}},
	})
}

func (s *responseState) terminalFrame() ([]byte, *rpcError) {
	if s.terminalSent {
		return nil, nil
	}
	finishReason := s.normalizedFinishReason()
	chunk := openAIChunk{
		ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model,
		Choices: []openAIChunkChoice{{Index: 0, Delta: map[string]any{}, FinishReason: &finishReason}},
	}
	if s.includeUsage {
		chunk.Usage = s.usage
	}
	s.terminalSent = true
	return sseJSON(chunk), nil
}

func (s *responseState) captureFinish(reason string, usage json.RawMessage) {
	if strings.TrimSpace(reason) != "" {
		s.finishReason = reason
	}
	if len(usage) == 0 || bytes.Equal(bytes.TrimSpace(usage), []byte("null")) {
		return
	}
	var upstream upstreamUsage
	if json.Unmarshal(usage, &upstream) != nil {
		return
	}
	mapped := &openAIUsage{
		PromptTokens:     upstream.InputTokens,
		CompletionTokens: upstream.OutputTokens,
		TotalTokens:      upstream.TotalTokens,
	}
	if mapped.TotalTokens == 0 {
		mapped.TotalTokens = mapped.PromptTokens + mapped.CompletionTokens
	}
	mapped.PromptTokensDetails.CachedTokens = upstream.InputTokenDetails.CacheReadTokens
	mapped.CompletionTokensDetails.ReasoningTokens = upstream.OutputTokenDetails.ReasoningTokens
	s.usage = mapped
}

func (s *responseState) normalizedFinishReason() string {
	switch strings.ToLower(strings.TrimSpace(s.finishReason)) {
	case "tool-calls", "tool_calls":
		return "tool_calls"
	case "length", "max_tokens", "max-tokens", "max_output_tokens":
		return "length"
	default:
		return "stop"
	}
}

func (s *responseState) ensureTool(id, name string) *responseTool {
	if tool := s.tools[id]; tool != nil {
		if tool.Name == "" {
			tool.Name = name
		}
		return tool
	}
	tool := &responseTool{ID: id, Name: name, Index: len(s.toolOrder)}
	s.tools[id] = tool
	s.toolOrder = append(s.toolOrder, id)
	return tool
}

func (s *responseState) failProtocol(message string) *rpcError {
	s.failed = &rpcError{Code: "protocol_error", Message: message, HTTPStatus: 502}
	return s.failed
}

func sseJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return append(append([]byte("data: "), raw...), []byte("\n\n")...)
}

func normalizeToolInput(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "{}", nil
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		if !json.Valid([]byte(encoded)) {
			return "", fmt.Errorf("invalid tool input")
		}
		var object map[string]any
		if json.Unmarshal([]byte(encoded), &object) != nil {
			return "", fmt.Errorf("invalid tool input")
		}
		compact, _ := json.Marshal(object)
		return string(compact), nil
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return "", fmt.Errorf("invalid tool input")
	}
	compact, _ := json.Marshal(object)
	return string(compact), nil
}

func upstreamErrorMessage(event upstreamEvent) string {
	message := strings.TrimSpace(event.Message)
	if len(event.Error) > 0 {
		var object struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(event.Error, &object) == nil && strings.TrimSpace(object.Message) != "" {
			message = strings.TrimSpace(object.Message)
		} else {
			var text string
			if json.Unmarshal(event.Error, &text) == nil {
				message = strings.TrimSpace(text)
			}
		}
	}
	if message == "" {
		message = "CommandCode upstream returned an error"
	}
	return message
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
