package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	generateURL           = "https://api.commandcode.ai/alpha/generate"
	commandCodeCLIVersion = "1.26.0"
)

func resolveRequestedModel(models []credentialModel, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	for _, model := range models {
		if requested == model.Name {
			return model.Name, true
		}
	}
	for _, model := range models {
		if requested == model.Alias {
			return model.Name, true
		}
	}
	return "", false
}

type executorRPCRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type executorHTTPRPCRequest struct {
	pluginapi.ExecutorHTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type executorStreamRPCResponse struct {
	Headers http.Header `json:"headers,omitempty"`
}

type hostHTTPStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers,omitempty"`
	StreamID   string      `json:"stream_id,omitempty"`
}

type hostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type hostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type hostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type hostStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type hostStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

type upstreamHTTPStream struct {
	id   string
	once sync.Once
}

func openUpstreamStream(request executorRPCRequest, apiKey string, body []byte) (*upstreamHTTPStream, int, *rpcError) {
	var response hostHTTPStreamResponse
	err := hostCall(pluginabi.MethodHostHTTPDoStream, hostHTTPRequest{
		HostCallbackID: request.HostCallbackID,
		Method:         http.MethodPost,
		URL:            generateURL,
		Headers: http.Header{
			"Authorization":          []string{"Bearer " + apiKey},
			"Content-Type":           []string{"application/json"},
			"X-Cli-Environment":      []string{"production"},
			"X-Command-Code-Version": []string{commandCodeCLIVersion},
			"X-Session-Id":           []string{newUUID()},
		},
		Body: body,
	}, &response)
	if err != nil {
		return nil, 0, &rpcError{Code: "upstream_error", Message: "CommandCode request failed", Retryable: true, HTTPStatus: 502}
	}
	stream := &upstreamHTTPStream{id: response.StreamID}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		stream.Close()
		return nil, response.StatusCode, upstreamStatusError(response.StatusCode)
	}
	if strings.TrimSpace(response.StreamID) == "" {
		return nil, response.StatusCode, &rpcError{Code: "protocol_error", Message: "CommandCode response stream is unavailable", HTTPStatus: 502}
	}
	return stream, response.StatusCode, nil
}

func (s *upstreamHTTPStream) Read() (hostHTTPStreamReadResponse, error) {
	var response hostHTTPStreamReadResponse
	if s == nil || s.id == "" {
		return response, errors.New("upstream stream is closed")
	}
	if err := hostCall(pluginabi.MethodHostHTTPStreamRead, hostHTTPStreamReadRequest{StreamID: s.id}, &response); err != nil {
		return response, err
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func (s *upstreamHTTPStream) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.id != "" {
			_ = hostCall(pluginabi.MethodHostHTTPStreamClose, hostHTTPStreamCloseRequest{StreamID: s.id}, &struct{}{})
		}
	})
}

func handleExecutorExecute(raw []byte) ([]byte, error) {
	request, value, body, options, rpcErr := prepareExecutorRequest(raw)
	if rpcErr != nil {
		return errorEnvelope(rpcErr), nil
	}
	stream, _, rpcErr := openUpstreamStream(request, value.APIKey, body)
	if rpcErr != nil {
		return errorEnvelope(rpcErr), nil
	}
	defer stream.Close()
	state := newResponseState("chatcmpl-"+newUUID(), time.Now().Unix(), request.Model, options.IncludeUsage)
	for {
		chunk, err := stream.Read()
		if err != nil {
			return errorEnvelope(&rpcError{Code: "upstream_error", Message: "CommandCode stream failed", Retryable: true, HTTPStatus: 502}), nil
		}
		if len(chunk.Payload) > 0 {
			if _, parseErr := state.Feed(chunk.Payload); parseErr != nil {
				return errorEnvelope(parseErr), nil
			}
		}
		if chunk.Done {
			break
		}
	}
	if _, finishErr := state.Finish(); finishErr != nil {
		return errorEnvelope(finishErr), nil
	}
	completion, completionErr := state.Completion()
	if completionErr != nil {
		return errorEnvelope(completionErr), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: completion,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	})
}

func handleExecutorExecuteStream(raw []byte) ([]byte, error) {
	request, value, body, options, rpcErr := prepareExecutorRequest(raw)
	if rpcErr != nil {
		return errorEnvelope(rpcErr), nil
	}
	if strings.TrimSpace(request.StreamID) == "" {
		return errorEnvelope(invalidRequest("stream_id is required")), nil
	}
	stream, _, rpcErr := openUpstreamStream(request, value.APIKey, body)
	if rpcErr != nil {
		return errorEnvelope(rpcErr), nil
	}
	if !startWorker(stream.Close, func() {
		runExecutorStreamWorker(request, stream, options)
	}) {
		stream.Close()
		return errorEnvelope(&rpcError{Code: "plugin_shutdown", Message: "plugin is shutting down", HTTPStatus: 503}), nil
	}
	return okEnvelope(executorStreamRPCResponse{Headers: http.Header{
		"Content-Type":  []string{"text/event-stream"},
		"Cache-Control": []string{"no-cache"},
	}})
}

func runExecutorStreamWorker(request executorRPCRequest, upstream *upstreamHTTPStream, options requestOptions) {
	var closeError string
	defer func() {
		upstream.Close()
		_ = hostCall(pluginabi.MethodHostStreamClose, hostStreamCloseRequest{StreamID: request.StreamID, Error: closeError}, &struct{}{})
	}()
	state := newResponseState("chatcmpl-"+newUUID(), time.Now().Unix(), request.Model, options.IncludeUsage)
	for {
		chunk, err := upstream.Read()
		if err != nil {
			closeError = "CommandCode stream failed"
			return
		}
		if len(chunk.Payload) > 0 {
			frames, parseErr := state.Feed(chunk.Payload)
			if parseErr != nil {
				closeError = parseErr.Message
				return
			}
			for _, frame := range frames {
				if err := emitDownstream(request.StreamID, frame); err != nil {
					closeError = "downstream stream closed"
					return
				}
			}
		}
		if chunk.Done {
			break
		}
	}
	frames, finishErr := state.Finish()
	if finishErr != nil {
		closeError = finishErr.Message
		return
	}
	for _, frame := range frames {
		if err := emitDownstream(request.StreamID, frame); err != nil {
			closeError = "downstream stream closed"
			return
		}
	}
}

func emitDownstream(streamID string, payload []byte) error {
	return hostCall(pluginabi.MethodHostStreamEmit, hostStreamEmitRequest{StreamID: streamID, Payload: payload}, &struct{}{})
}

func prepareExecutorRequest(raw []byte) (executorRPCRequest, credential, []byte, requestOptions, *rpcError) {
	var request executorRPCRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return request, credential{}, nil, requestOptions{}, invalidRequest("invalid executor request")
	}
	value, err := normalizeCredential(request.StorageJSON)
	if err != nil {
		return request, credential{}, nil, requestOptions{}, &rpcError{Code: "invalid_credentials", Message: errInvalidCredential.Error(), HTTPStatus: 401}
	}
	resolved, ok := resolveRequestedModel(value.Models, request.Model)
	if !ok {
		return request, credential{}, nil, requestOptions{}, &rpcError{Code: "invalid_request", Message: "model is not enabled for this account", HTTPStatus: http.StatusBadRequest}
	}
	request.Model = resolved
	payload := request.Payload
	if len(payload) == 0 {
		payload = request.OriginalRequest
	}
	body, options, translateErr := translateOpenAIRequest(payload, request.Model, time.Now(), newUUID())
	return request, value, body, options, translateErr
}

func upstreamStatusError(status int) *rpcError {
	switch status {
	case http.StatusUnauthorized:
		return &rpcError{Code: "invalid_credentials", Message: "CommandCode rejected the credential", HTTPStatus: status}

	case http.StatusForbidden:
		return &rpcError{Code: "upstream_forbidden", Message: "CommandCode rejected the request", HTTPStatus: status}
	case http.StatusTooManyRequests:
		return &rpcError{Code: "upstream_rate_limited", Message: "CommandCode rate limit exceeded", Retryable: true, HTTPStatus: status}
	default:
		return &rpcError{Code: "upstream_error", Message: fmt.Sprintf("CommandCode returned HTTP %d", status), Retryable: status >= 500, HTTPStatus: status}
	}
}

func handleExecutorHTTPRequest(raw []byte) ([]byte, error) {
	var request executorHTTPRPCRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return errorEnvelope(invalidRequest("invalid executor HTTP request")), nil
	}
	parsed, err := url.Parse(strings.TrimSpace(request.URL))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return errorEnvelope(invalidRequest("executor HTTP URL must be absolute HTTPS")), nil
	}
	headers := request.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	if strings.EqualFold(parsed.Hostname(), "api.commandcode.ai") {
		value, credentialErr := normalizeCredential(request.StorageJSON)
		if credentialErr != nil {
			return errorEnvelope(&rpcError{Code: "invalid_credentials", Message: errInvalidCredential.Error(), HTTPStatus: 401}), nil
		}
		headers.Set("Authorization", "Bearer "+value.APIKey)
		headers.Set("x-command-code-version", commandCodeCLIVersion)
		headers.Set("x-cli-environment", "production")
	}
	var response pluginapi.HTTPResponse
	if err := hostCall(pluginabi.MethodHostHTTPDo, hostHTTPRequest{
		HostCallbackID: request.HostCallbackID,
		Method:         request.Method, URL: request.URL, Headers: headers, Body: request.Body,
	}, &response); err != nil {
		return errorEnvelope(&rpcError{Code: "upstream_error", Message: "executor HTTP request failed", Retryable: true, HTTPStatus: 502}), nil
	}
	return okEnvelope(pluginapi.ExecutorHTTPResponse{StatusCode: response.StatusCode, Headers: response.Headers, Body: response.Body})
}

func newUUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
