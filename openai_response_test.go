package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNDJSONHandlesEveryByteSplit(t *testing.T) {
	fixture, err := os.ReadFile("testdata/commandcode-text.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	for split := 0; split <= len(fixture); split++ {
		state := newResponseState("chatcmpl-fixed", 123, "m", true)
		var frames [][]byte
		first, err := state.Feed(fixture[:split])
		if err != nil {
			t.Fatalf("split %d first feed: %v", split, err)
		}
		frames = append(frames, first...)
		second, err := state.Feed(fixture[split:])
		if err != nil {
			t.Fatalf("split %d second feed: %v", split, err)
		}
		frames = append(frames, second...)
		final, err := state.Finish()
		if err != nil {
			t.Fatalf("split %d finish: %v", split, err)
		}
		frames = append(frames, final...)
		joined := string(joinFrames(frames))
		if strings.Count(joined, "data: [DONE]\n\n") != 1 || !strings.Contains(joined, `"content":"Hel"`) || !strings.Contains(joined, `"content":"lo"`) {
			t.Fatalf("split %d frames = %s", split, joined)
		}
	}
}

func TestOpenAIResponseMapsReasoningUsageAndFinish(t *testing.T) {
	fixture, _ := os.ReadFile("testdata/commandcode-reasoning.ndjson")
	state := newResponseState("chatcmpl-fixed", 123, "model", true)
	frames, err := state.Feed(fixture)
	if err != nil {
		t.Fatal(err)
	}
	final, err := state.Finish()
	if err != nil {
		t.Fatal(err)
	}
	joined := string(joinFrames(append(frames, final...)))
	for _, want := range []string{`"role":"assistant"`, `"reasoning_content":"think"`, `"content":"answer"`, `"finish_reason":"length"`, `"reasoning_tokens":3`, `data: [DONE]`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	completion, err := state.Completion()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(completion, &decoded); err != nil {
		t.Fatal(err)
	}
	choice := decoded["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if message["content"] != "answer" || message["reasoning_content"] != "think" || choice["finish_reason"] != "length" {
		t.Fatalf("completion = %s", completion)
	}
}

func TestOpenAIResponseDeduplicatesFinalToolCall(t *testing.T) {
	fixture, _ := os.ReadFile("testdata/commandcode-tool.ndjson")
	state := newResponseState("chatcmpl-fixed", 123, "model", false)
	frames, err := state.Feed(fixture)
	if err != nil {
		t.Fatal(err)
	}
	final, err := state.Finish()
	if err != nil {
		t.Fatal(err)
	}
	joined := string(joinFrames(append(frames, final...)))
	if strings.Count(joined, `"id":"call_1"`) != 1 || strings.Count(joined, `"name":"lookup"`) != 1 || !strings.Contains(joined, `"arguments":"{\"q\":"`) || !strings.Contains(joined, `"arguments":"\"x\"}"`) || !strings.Contains(joined, `"finish_reason":"tool_calls"`) {
		t.Fatalf("frames = %s", joined)
	}
	if strings.Contains(joined, `"usage"`) {
		t.Fatalf("usage emitted when include_usage=false: %s", joined)
	}
	completion, err := state.Completion()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(completion), `"id":"call_1"`) != 1 {
		t.Fatalf("completion duplicate tool call: %s", completion)
	}
}

func TestNDJSONRejectsMalformedOversizedAndPrematureEOF(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		state := newResponseState("id", 1, "m", false)
		_, err := state.Feed([]byte("{bad}\n"))
		assertProtocolError(t, err)
	})
	t.Run("oversized", func(t *testing.T) {
		state := newResponseState("id", 1, "m", false)
		_, err := state.Feed([]byte(strings.Repeat("x", maxEventBytes+1)))
		assertProtocolError(t, err)
	})
	t.Run("premature eof", func(t *testing.T) {
		state := newResponseState("id", 1, "m", false)
		if _, err := state.Feed([]byte(`{"type":"text-delta","text":"partial"}\n`)); err != nil {
			t.Fatal(err)
		}
		_, err := state.Finish()
		assertProtocolError(t, err)
	})
}

func TestNDJSONErrorNeverBecomesAssistantContent(t *testing.T) {
	fixture, _ := os.ReadFile("testdata/commandcode-error.ndjson")
	state := newResponseState("id", time.Now().Unix(), "m", false)
	frames, err := state.Feed(fixture)
	if err == nil || err.Code != "upstream_error" {
		t.Fatalf("error = %#v", err)
	}
	if strings.Contains(string(joinFrames(frames)), "safe failure") {
		t.Fatal("upstream error became assistant content")
	}
}

func assertProtocolError(t *testing.T, err *rpcError) {
	t.Helper()
	if err == nil || err.Code != "protocol_error" || err.HTTPStatus != 502 {
		t.Fatalf("error = %#v", err)
	}
}

func joinFrames(frames [][]byte) []byte {
	var out []byte
	for _, frame := range frames {
		out = append(out, frame...)
	}
	return out
}
