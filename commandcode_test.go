package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type hostCallbackScript struct {
	mu           sync.Mutex
	calls        []hostCallbackRecord
	streamChunks [][]byte
	streamError  string
	emitError    error
	statusCode   int
}

type hostCallbackRecord struct {
	Method  string
	Request any
}

func (s *hostCallbackScript) call(method string, request any, result any) error {
	s.mu.Lock()
	s.calls = append(s.calls, hostCallbackRecord{Method: method, Request: request})
	s.mu.Unlock()
	switch method {
	case pluginabi.MethodHostHTTPDoStream:
		response := result.(*hostHTTPStreamResponse)
		status := s.statusCode
		if status == 0 {
			status = http.StatusOK
		}
		*response = hostHTTPStreamResponse{StatusCode: status, StreamID: "upstream-1"}
	case pluginabi.MethodHostHTTPStreamRead:
		response := result.(*hostHTTPStreamReadResponse)
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.streamChunks) > 0 {
			response.Payload = append([]byte(nil), s.streamChunks[0]...)
			s.streamChunks = s.streamChunks[1:]
			return nil
		}
		response.Done = true
		response.Error = s.streamError
	case pluginabi.MethodHostStreamEmit:
		return s.emitError
	case pluginabi.MethodHostHTTPDo:
		response := result.(*pluginapi.HTTPResponse)
		*response = pluginapi.HTTPResponse{StatusCode: 204, Headers: http.Header{"X-Test": []string{"1"}}, Body: []byte("ok")}
	}
	return nil
}

func TestExecutorRejectsUnselectedModel(t *testing.T) {
	key := "user_exec_models"
	raw := mustHandle(t, pluginabi.MethodExecutorExecute, executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "gpt-5.5",
			Payload:     []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`),
			StorageJSON: mustJSON(t, map[string]any{"type": pluginID, "api_key": key, "models": []any{map[string]any{"name": "deepseek/deepseek-v4-pro"}}}),
		},
	})
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "invalid_request" || envelope.Error.HTTPStatus != http.StatusBadRequest || envelope.Error.Message != "model is not enabled for this account" {
		t.Fatalf("envelope = %s", raw)
	}
	if strings.Contains(string(raw), "gpt-5.5") || strings.Contains(string(raw), "deepseek") {
		t.Fatalf("rejection leaked model names: %s", raw)
	}
}

func TestExecutorStreamRejectsUnselectedModel(t *testing.T) {
	raw := mustHandle(t, pluginabi.MethodExecutorExecuteStream, executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "gpt-5.5",
			Payload:     []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
			StorageJSON: mustJSON(t, map[string]any{"type": pluginID, "api_key": "user_exec_stream_models", "models": []any{map[string]any{"name": "deepseek/deepseek-v4-pro"}}}),
		},
		StreamID: "rejected-stream",
	})
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "invalid_request" || envelope.Error.HTTPStatus != http.StatusBadRequest || envelope.Error.Message != "model is not enabled for this account" {
		t.Fatalf("envelope = %s", raw)
	}
	if strings.Contains(string(raw), "gpt-5.5") || strings.Contains(string(raw), "deepseek") {
		t.Fatalf("rejection leaked model names: %s", raw)
	}
}

func TestExecutorRejectsMissingModelSet(t *testing.T) {
	raw := mustHandle(t, pluginabi.MethodExecutorExecute, executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "gpt-5.5",
			Payload:     []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`),
			StorageJSON: mustJSON(t, credential{Type: pluginID, APIKey: "user_exec_no_models"}),
		},
	})
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "invalid_request" || envelope.Error.HTTPStatus != http.StatusBadRequest || envelope.Error.Message != "model is not enabled for this account" {
		t.Fatalf("envelope = %s", raw)
	}
	if strings.Contains(string(raw), "gpt-5.5") {
		t.Fatalf("rejection leaked model name: %s", raw)
	}
}

func TestExecutorRewritesAliasToUpstream(t *testing.T) {
	var captured map[string]any
	withHostCall(t, func(method string, request any, result any) error {
		switch method {
		case pluginabi.MethodHostHTTPDoStream:
			req := request.(hostHTTPRequest)
			if err := json.Unmarshal(req.Body, &captured); err != nil {
				return err
			}
			*result.(*hostHTTPStreamResponse) = hostHTTPStreamResponse{StatusCode: http.StatusOK, StreamID: "alias-upstream"}
		case pluginabi.MethodHostHTTPStreamRead:
			*result.(*hostHTTPStreamReadResponse) = hostHTTPStreamReadResponse{Payload: []byte(`{"type":"finish","finishReason":"stop"}`), Done: true}
		}
		return nil
	})
	raw := mustHandle(t, pluginabi.MethodExecutorExecute, executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "cc-pro",
			Payload:     []byte(`{"model":"cc-pro","messages":[{"role":"user","content":"hi"}]}`),
			StorageJSON: mustJSON(t, map[string]any{"type": pluginID, "api_key": "user_exec_alias", "models": []any{map[string]any{"name": "deepseek/deepseek-v4-pro", "alias": "cc-pro"}}}),
		},
	})
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("envelope = %s", raw)
	}
	params := captured["params"].(map[string]any)
	if params["model"] != "deepseek/deepseek-v4-pro" {
		t.Fatalf("upstream model = %v, want deepseek/deepseek-v4-pro", params["model"])
	}
}

func TestExecutorHTTPRequestEnforcesModelSet(t *testing.T) {
	t.Run("rejects unselected", func(t *testing.T) {
		raw := mustHandle(t, pluginabi.MethodExecutorHTTPRequest, executorHTTPRPCRequest{
			ExecutorHTTPRequest: pluginapi.ExecutorHTTPRequest{
				Method: http.MethodPost, URL: "https://api.commandcode.ai/provider/v1/chat/completions",
				Body:        []byte(`{"model":"gpt-5.5"}`),
				StorageJSON: mustJSON(t, map[string]any{"type": pluginID, "api_key": "user_http_models", "models": []any{map[string]any{"name": "deepseek/deepseek-v4-pro"}}}),
			},
		})
		var envelope rpcEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.OK || envelope.Error == nil || envelope.Error.Code != "invalid_request" || envelope.Error.HTTPStatus != http.StatusBadRequest || envelope.Error.Message != "model is not enabled for this account" {
			t.Fatalf("envelope = %s", raw)
		}
		if strings.Contains(string(raw), "gpt-5.5") || strings.Contains(string(raw), "deepseek") {
			t.Fatalf("rejection leaked model names: %s", raw)
		}
	})

	t.Run("rewrites alias", func(t *testing.T) {
		var captured hostHTTPRequest
		withHostCall(t, func(method string, request any, result any) error {
			if method == pluginabi.MethodHostHTTPDo {
				captured = request.(hostHTTPRequest)
				*result.(*pluginapi.HTTPResponse) = pluginapi.HTTPResponse{StatusCode: http.StatusNoContent}
			}
			return nil
		})
		raw := mustHandle(t, pluginabi.MethodExecutorHTTPRequest, executorHTTPRPCRequest{
			ExecutorHTTPRequest: pluginapi.ExecutorHTTPRequest{
				Method: http.MethodPost, URL: "https://api.commandcode.ai/provider/v1/chat/completions",
				Body:        []byte(`{"model":"cc-pro"}`),
				StorageJSON: mustJSON(t, map[string]any{"type": pluginID, "api_key": "user_http_alias", "models": []any{map[string]any{"name": "deepseek/deepseek-v4-pro", "alias": "cc-pro"}}}),
			},
		})
		var response pluginapi.ExecutorHTTPResponse
		decodeResult(t, raw, &response)
		var body map[string]any
		if err := json.Unmarshal(captured.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "deepseek/deepseek-v4-pro" {
			t.Fatalf("upstream model = %v, want deepseek/deepseek-v4-pro", body["model"])
		}
	})
}

func TestExecutorNonStreamUsesHostTransportAndClosesUpstream(t *testing.T) {
	fixture := []byte("{\"type\":\"text-delta\",\"text\":\"hello\"}\n{\"type\":\"finish\",\"finishReason\":\"stop\"}\n")
	script := &hostCallbackScript{streamChunks: [][]byte{fixture}}
	withHostCall(t, script.call)
	storage := mustJSON(t, credential{Type: pluginID, APIKey: "user_secret", Models: []credentialModel{{Name: "deepseek/deepseek-v4-pro"}}})
	raw := mustHandle(t, pluginabi.MethodExecutorExecute, executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "deepseek/deepseek-v4-pro",
			Payload:     []byte(`{"model":"deepseek/deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`),
			StorageJSON: storage,
		},
		HostCallbackID: "callback-1",
	})
	var response pluginapi.ExecutorResponse
	decodeResult(t, raw, &response)
	if response.Headers.Get("Content-Type") != "application/json" || !strings.Contains(string(response.Payload), `"content":"hello"`) {
		t.Fatalf("response = %#v body=%s", response, response.Payload)
	}
	assertCallCount(t, script, pluginabi.MethodHostHTTPDoStream, 1)
	assertCallCount(t, script, pluginabi.MethodHostHTTPStreamClose, 1)
	assertCallCount(t, script, pluginabi.MethodHostStreamEmit, 0)
	open := firstCall(t, script, pluginabi.MethodHostHTTPDoStream).Request.(hostHTTPRequest)
	if open.HostCallbackID != "callback-1" || open.URL != generateURL || open.Method != http.MethodPost {
		t.Fatalf("open request = %#v", open)
	}
	if open.Headers.Get("Authorization") != "Bearer user_secret" || open.Headers.Get("x-command-code-version") != commandCodeCLIVersion || open.Headers.Get("x-session-id") == "" {
		t.Fatalf("headers = %#v", open.Headers)
	}
}

func TestExecutorMapsUpstreamStatusWithoutLeakingCredential(t *testing.T) {
	for status, code := range map[int]string{401: "invalid_credentials", 403: "upstream_forbidden", 429: "upstream_rate_limited", 500: "upstream_error"} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			script := &hostCallbackScript{statusCode: status}
			withHostCall(t, script.call)
			key := "user_status_secret"
			raw, err := handleMethod(pluginabi.MethodExecutorExecute, mustJSON(t, executorRPCRequest{
				ExecutorRequest: pluginapi.ExecutorRequest{
					Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
					StorageJSON: mustJSON(t, credential{Type: pluginID, APIKey: key, Models: []credentialModel{{Name: "m"}}}),
				},
				HostCallbackID: "callback",
			}))
			if err != nil {
				t.Fatal(err)
			}
			var envelope rpcEnvelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != code || strings.Contains(string(raw), key) {
				t.Fatalf("envelope = %s", raw)
			}
			assertCallCount(t, script, pluginabi.MethodHostHTTPStreamClose, 1)
		})
	}
}

func TestExecutorStreamEmitsAndClosesBothStreams(t *testing.T) {
	fixture := []byte("{\"type\":\"text-delta\",\"text\":\"hi\"}\n{\"type\":\"finish\",\"finishReason\":\"stop\"}\n")
	script := &hostCallbackScript{streamChunks: [][]byte{fixture}}
	withHostCall(t, script.call)
	raw := mustHandle(t, pluginabi.MethodExecutorExecuteStream, executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`),
			StorageJSON: mustJSON(t, credential{Type: pluginID, APIKey: "user_stream", Models: []credentialModel{{Name: "m"}}}),
		},
		StreamID: "downstream-1", HostCallbackID: "callback",
	})
	var response executorStreamRPCResponse
	decodeResult(t, raw, &response)
	if response.Headers.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("response headers = %#v", response.Headers)
	}
	waitForWorkers(t)
	assertCallCount(t, script, pluginabi.MethodHostHTTPStreamClose, 1)
	assertCallCount(t, script, pluginabi.MethodHostStreamClose, 1)
	script.mu.Lock()
	var payloads [][]byte
	for _, call := range script.calls {
		if call.Method == pluginabi.MethodHostStreamEmit {
			payloads = append(payloads, append([]byte(nil), call.Request.(hostStreamEmitRequest).Payload...))
		}
	}
	script.mu.Unlock()
	if len(payloads) < 2 {
		t.Fatalf("emit calls = %#v", script.calls)
	}
	for _, payload := range payloads {
		if bytes.HasPrefix(payload, []byte("data:")) {
			t.Fatalf("host stream emit payload must be raw JSON, got %q", payload)
		}
		if !json.Valid(payload) {
			t.Fatalf("host stream emit payload must be JSON, got %q", payload)
		}
	}
	closeRequest := firstCall(t, script, pluginabi.MethodHostStreamClose).Request.(hostStreamCloseRequest)
	if closeRequest.StreamID != "downstream-1" || closeRequest.Error != "" {
		t.Fatalf("close request = %#v", closeRequest)
	}
}

func TestExecutorStreamEmitFailureStillClosesOnce(t *testing.T) {
	script := &hostCallbackScript{
		streamChunks: [][]byte{[]byte("{\"type\":\"text-delta\",\"text\":\"hi\"}\n")},
		emitError:    errors.New("downstream closed"),
	}
	withHostCall(t, script.call)
	mustHandle(t, pluginabi.MethodExecutorExecuteStream, executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`),
			StorageJSON: mustJSON(t, credential{Type: pluginID, APIKey: "user_stream", Models: []credentialModel{{Name: "m"}}}),
		},
		StreamID: "downstream-1", HostCallbackID: "callback",
	})
	waitForWorkers(t)
	assertCallCount(t, script, pluginabi.MethodHostHTTPStreamClose, 1)
	assertCallCount(t, script, pluginabi.MethodHostStreamClose, 1)
	closeRequest := firstCall(t, script, pluginabi.MethodHostStreamClose).Request.(hostStreamCloseRequest)
	if closeRequest.Error == "" {
		t.Fatal("stream close did not carry safe error")
	}
}

func TestShutdownClosesBlockedExecutorUpstreamBeforeWaiting(t *testing.T) {
	previous := shuttingDown.Load()
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(previous) })
	readStarted := make(chan struct{})
	readReleased := make(chan struct{})
	var once sync.Once
	withHostCall(t, func(method string, request any, result any) error {
		switch method {
		case pluginabi.MethodHostHTTPDoStream:
			*(result.(*hostHTTPStreamResponse)) = hostHTTPStreamResponse{StatusCode: http.StatusOK, StreamID: "blocked-upstream"}
		case pluginabi.MethodHostHTTPStreamRead:
			once.Do(func() { close(readStarted) })
			<-readReleased
			return errors.New("closed")
		case pluginabi.MethodHostHTTPStreamClose:
			close(readReleased)
		case pluginabi.MethodHostStreamClose:
		}
		return nil
	})
	mustHandle(t, pluginabi.MethodExecutorExecuteStream, executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`),
			StorageJSON: mustJSON(t, credential{Type: pluginID, APIKey: "user_stream", Models: []credentialModel{{Name: "m"}}}),
		},
		StreamID: "downstream-1", HostCallbackID: "callback",
	})
	<-readStarted
	done := make(chan struct{})
	go func() { shutdownPlugin(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel blocked upstream stream")
	}
}
func TestExecutorAuxiliaryMethods(t *testing.T) {
	identifier := mustHandle(t, pluginabi.MethodExecutorIdentifier, struct{}{})
	var id map[string]string
	decodeResult(t, identifier, &id)
	if id["identifier"] != pluginID {
		t.Fatalf("identifier = %#v", id)
	}
	count, _ := handleMethod(pluginabi.MethodExecutorCountTokens, []byte(`{}`))
	var countEnvelope rpcEnvelope
	_ = json.Unmarshal(count, &countEnvelope)
	if countEnvelope.Error == nil || countEnvelope.Error.Code != "not_supported" {
		t.Fatalf("count envelope = %s", count)
	}

	script := &hostCallbackScript{}
	withHostCall(t, script.call)
	raw := mustHandle(t, pluginabi.MethodExecutorHTTPRequest, executorHTTPRPCRequest{
		ExecutorHTTPRequest: pluginapi.ExecutorHTTPRequest{
			Method: "GET", URL: "https://api.commandcode.ai/provider/v1/models",
			Headers:     http.Header{"X-Client": []string{"1"}},
			StorageJSON: mustJSON(t, credential{Type: pluginID, APIKey: "user_http", Models: []credentialModel{{Name: "unused"}}}),
		},
		HostCallbackID: "callback",
	})
	var response pluginapi.ExecutorHTTPResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != 204 || response.Headers.Get("X-Test") != "1" || string(response.Body) != "ok" {
		t.Fatalf("response = %#v", response)
	}
	request := firstCall(t, script, pluginabi.MethodHostHTTPDo).Request.(hostHTTPRequest)
	if request.Headers.Get("Authorization") != "Bearer user_http" || request.HostCallbackID != "callback" {
		t.Fatalf("request = %#v", request)
	}

	invalid, _ := handleMethod(pluginabi.MethodExecutorHTTPRequest, mustJSON(t, executorHTTPRPCRequest{ExecutorHTTPRequest: pluginapi.ExecutorHTTPRequest{Method: "GET", URL: "http://api.commandcode.ai/x"}}))
	var invalidEnvelope rpcEnvelope
	_ = json.Unmarshal(invalid, &invalidEnvelope)
	if invalidEnvelope.Error == nil || invalidEnvelope.Error.Code != "invalid_request" {
		t.Fatalf("invalid envelope = %s", invalid)
	}
}

func withHostCall(t *testing.T, fn func(string, any, any) error) {
	t.Helper()
	previous := hostCall
	hostCall = fn
	t.Cleanup(func() { hostCall = previous })
}

func countCalls(script *hostCallbackScript, method string) int {
	script.mu.Lock()
	defer script.mu.Unlock()
	count := 0
	for _, call := range script.calls {
		if call.Method == method {
			count++
		}
	}
	return count
}

func assertCallCount(t *testing.T, script *hostCallbackScript, method string, want int) {
	t.Helper()
	if got := countCalls(script, method); got != want {
		t.Fatalf("%s calls = %d, want %d; all=%#v", method, got, want, script.calls)
	}
}

func firstCall(t *testing.T, script *hostCallbackScript, method string) hostCallbackRecord {
	t.Helper()
	script.mu.Lock()
	defer script.mu.Unlock()
	for _, call := range script.calls {
		if call.Method == method {
			return call
		}
	}
	t.Fatalf("missing call %s; all=%#v", method, script.calls)
	return hostCallbackRecord{}
}

func waitForWorkers(t *testing.T) {
	t.Helper()
	workerGroup.Wait()
}
