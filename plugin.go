package main

import (
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"sync"
	"sync/atomic"
)

var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

var (
	workerGroup   sync.WaitGroup
	workerMu      sync.Mutex
	workerCancels = make(map[uint64]func())
	workerNext    uint64
	shuttingDown  atomic.Bool
)

type rpcEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type capabilityRegistration struct {
	ModelProvider         bool     `json:"model_provider"`
	AuthProvider          bool     `json:"auth_provider"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string `json:"executor_output_formats,omitempty"`
	ManagementAPI         bool     `json:"management_api"`
}

type registrationResponse struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  capabilityRegistration `json:"capabilities"`
}

func rejectsAfterShutdown(method string) bool {
	switch method {
	case pluginabi.MethodExecutorExecute,
		pluginabi.MethodExecutorExecuteStream,
		pluginabi.MethodExecutorHTTPRequest,
		pluginabi.MethodManagementHandle:
		return true
	default:
		return false
	}
}
func handleMethod(method string, request []byte) ([]byte, error) {
	if shuttingDown.Load() && rejectsAfterShutdown(method) {
		return errorEnvelope(&rpcError{Code: "plugin_shutdown", Message: "plugin is shutting down", HTTPStatus: 503}), nil
	}
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var lifecycle lifecycleRequest
		if len(request) > 0 {
			if err := json.Unmarshal(request, &lifecycle); err != nil {
				return errorEnvelope(&rpcError{Code: "invalid_request", Message: "invalid lifecycle request", HTTPStatus: 400}), nil
			}
		}
		return okEnvelope(registration())
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case pluginabi.MethodAuthParse:
		return handleAuthParse(request)
	case pluginabi.MethodAuthRefresh:
		return handleAuthRefresh(request)
	case pluginabi.MethodAuthLoginStart, pluginabi.MethodAuthLoginPoll:
		return errorEnvelope(&rpcError{Code: "not_supported", Message: "CommandCode account enrollment uses the plugin Management API", HTTPStatus: 501}), nil
	case pluginabi.MethodModelStatic:
		return handleModelStatic()
	case pluginabi.MethodModelForAuth:
		return handleModelForAuth(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case pluginabi.MethodExecutorExecute:
		return handleExecutorExecute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return handleExecutorExecuteStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return errorEnvelope(&rpcError{Code: "not_supported", Message: "CommandCode token counting is not supported", HTTPStatus: 501}), nil
	case pluginabi.MethodExecutorHTTPRequest:
		return handleExecutorHTTPRequest(request)
	case pluginabi.MethodManagementRegister:
		return handleManagementRegister()
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	case pluginabi.MethodPluginShutdown:
		shutdownPlugin()
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope(&rpcError{Code: "unknown_method", Message: "unknown method: " + method}), nil
	}
}

func registration() registrationResponse {
	return registrationResponse{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "CommandCode Bridge",
			Version:          Version,
			Author:           "Agoy",
			GitHubRepository: "https://github.com/YogaSakti/CommandCode-Bridge",
			ConfigFields:     []pluginapi.ConfigField{},
		},
		Capabilities: capabilityRegistration{
			AuthProvider:          true,
			ModelProvider:         true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeOAuth),
			ExecutorInputFormats:  []string{"chat-completions"},
			ExecutorOutputFormats: []string{"chat-completions"},
			ManagementAPI:         true,
		},
	}
}

func okEnvelope(result any) ([]byte, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal RPC result: %w", err)
	}
	return json.Marshal(rpcEnvelope{OK: true, Result: raw})
}

func errorEnvelope(rpcErr *rpcError) []byte {
	raw, _ := json.Marshal(rpcEnvelope{OK: false, Error: rpcErr})
	return raw
}

func shutdownPlugin() {
	shuttingDown.Store(true)
	workerMu.Lock()
	cancels := make([]func(), 0, len(workerCancels))
	for _, cancel := range workerCancels {
		cancels = append(cancels, cancel)
	}
	workerMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	workerGroup.Wait()
}

func startWorker(cancel func(), run func()) bool {
	if run == nil || shuttingDown.Load() {
		return false
	}
	workerMu.Lock()
	if shuttingDown.Load() {
		workerMu.Unlock()
		return false
	}
	workerNext++
	id := workerNext
	if cancel != nil {
		workerCancels[id] = cancel
	}
	workerGroup.Add(1)
	workerMu.Unlock()
	go func() {
		defer workerGroup.Done()
		defer func() {
			workerMu.Lock()
			delete(workerCancels, id)
			workerMu.Unlock()
		}()
		run()
	}()
	return true
}
