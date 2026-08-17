package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOpenAIRequestMapsMessagesToolsAndOptions(t *testing.T) {
	raw := []byte(`{
		"model":"ignored-by-executor-model",
		"messages":[
			{"role":"developer","content":"developer rules"},
			{"role":"system","content":[{"type":"text","text":"system rules"}]},
			{"role":"user","content":[{"type":"text","text":"hello"},{"type":"input_text","text":" world"}]},
			{"role":"assistant","content":"checking","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","name":"lookup","content":"result"}
		],
		"tools":[{"type":"function","function":{"name":"lookup","description":"Lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}],
		"max_tokens":100,
		"max_completion_tokens":80,
		"temperature":0.2,
		"top_p":0.9,
		"stream":true,
		"stream_options":{"include_usage":true}
	}`)
	translated, options, err := translateOpenAIRequest(raw, "deepseek/deepseek-v4-pro", time.Date(2026, 8, 16, 1, 2, 3, 0, time.UTC), "session-1")
	if err != nil {
		t.Fatalf("translateOpenAIRequest() error = %v", err)
	}
	if !options.Stream || !options.IncludeUsage {
		t.Fatalf("options = %#v", options)
	}
	var body map[string]any
	if err := json.Unmarshal(translated, &body); err != nil {
		t.Fatalf("decode translated body: %v", err)
	}
	if body["memory"] != "" || body["taste"] != nil || body["skills"] != nil || body["permissionMode"] != "standard" || body["threadId"] != "session-1" {
		t.Fatalf("top-level envelope = %#v", body)
	}
	config := body["config"].(map[string]any)
	if config["workingDir"] != "" || config["date"] != "2026-08-16" || config["isGitRepo"] != false {
		t.Fatalf("config = %#v", config)
	}
	params := body["params"].(map[string]any)
	if params["model"] != "deepseek/deepseek-v4-pro" || params["system"] != "developer rules\n\nsystem rules" || params["stream"] != true || params["max_tokens"] != float64(80) || params["temperature"] != 0.2 || params["top_p"] != 0.9 {
		t.Fatalf("params = %#v", params)
	}
	messages := params["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	user := messages[0].(map[string]any)
	if user["role"] != "user" || len(user["content"].([]any)) != 2 {
		t.Fatalf("user message = %#v", user)
	}
	assistant := messages[1].(map[string]any)
	assistantBlocks := assistant["content"].([]any)
	if assistant["role"] != "assistant" || len(assistantBlocks) != 2 || assistantBlocks[1].(map[string]any)["type"] != "tool-call" {
		t.Fatalf("assistant message = %#v", assistant)
	}
	tool := messages[2].(map[string]any)
	toolBlock := tool["content"].([]any)[0].(map[string]any)
	if tool["role"] != "tool" || toolBlock["type"] != "tool-result" || toolBlock["toolCallId"] != "call_1" || toolBlock["toolName"] != "lookup" {
		t.Fatalf("tool message = %#v", tool)
	}
	tools := params["tools"].([]any)
	toolSchema := tools[0].(map[string]any)
	if toolSchema["type"] != "function" || toolSchema["name"] != "lookup" || toolSchema["description"] != "Lookup" {
		t.Fatalf("tool schema = %#v", toolSchema)
	}
}

func TestOpenAIRequestDefaultsToUpstreamStreaming(t *testing.T) {
	translated, options, err := translateOpenAIRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), "m", time.Unix(0, 0), "session")
	if err != nil {
		t.Fatalf("translate error = %v", err)
	}
	if options.Stream || options.IncludeUsage {
		t.Fatalf("options = %#v", options)
	}
	var body struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(translated, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Params["stream"] != true {
		t.Fatalf("upstream stream = %#v", body.Params["stream"])
	}
}

func TestOpenAIRequestRejectsUnsupportedOrLossyInputs(t *testing.T) {
	cases := map[string]string{
		"missing model":       `{"messages":[{"role":"user","content":"hi"}]}`,
		"empty messages":      `{"model":"m","messages":[]}`,
		"image":               `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`,
		"audio":               `{"model":"m","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"x"}}]}]}`,
		"unknown role":        `{"model":"m","messages":[{"role":"function","content":"x"}]}`,
		"unknown block":       `{"model":"m","messages":[{"role":"user","content":[{"type":"future","value":"x"}]}]}`,
		"bad tool arguments":  `{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"f","arguments":"{"}}]}]}`,
		"bad tool definition": `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"custom"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := translateOpenAIRequest([]byte(raw), "", time.Unix(0, 0), "session")
			if err == nil || err.Code != "invalid_request" || err.HTTPStatus != 400 {
				t.Fatalf("error = %#v", err)
			}
			if strings.Contains(err.Message, raw) {
				t.Fatal("error contains full request")
			}
		})
	}
}
