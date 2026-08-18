package main

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
)

func TestPluginStoreTemplateValidates(t *testing.T) {
	raw, err := os.ReadFile("plugin-store.json")
	if err != nil {
		t.Fatal(err)
	}
	var registry pluginstore.Registry
	if err := json.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	if registry.SchemaVersion != pluginstore.SchemaVersion {
		t.Fatalf("schema = %d, want %d", registry.SchemaVersion, pluginstore.SchemaVersion)
	}
	if len(registry.Plugins) != 1 {
		t.Fatalf("plugins = %#v, want one plugin", registry.Plugins)
	}
	plugin := registry.Plugins[0]
	if err := pluginstore.ValidatePlugin(plugin); err != nil {
		t.Fatal(err)
	}
	if plugin.ID != pluginID || plugin.Name != "CommandCode Bridge" || plugin.Repository != "https://github.com/YogaSakti/CommandCode-Bridge" || plugin.Version != "" {
		t.Fatalf("plugin = %#v", plugin)
	}
}

func TestRegisterDeclaresExactContract(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodPluginRegister, mustJSON(t, lifecycleRequest{
		SchemaVersion: pluginabi.SchemaVersion,
	}))
	if err != nil {
		t.Fatalf("handleMethod() error = %v", err)
	}
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !envelope.OK || envelope.Error != nil {
		t.Fatalf("envelope = %#v, want success", envelope)
	}
	var registration registrationResponse
	if err := json.Unmarshal(envelope.Result, &registration); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	if registration.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", registration.SchemaVersion, pluginabi.SchemaVersion)
	}
	if registration.Metadata.Name != "CommandCode Bridge" || registration.Metadata.Version != Version || registration.Metadata.Author != "Agoy" || registration.Metadata.GitHubRepository != "https://github.com/YogaSakti/CommandCode-Bridge" || pluginID != "commandcode-bridge" {
		t.Fatalf("metadata = %#v, pluginID = %q", registration.Metadata, pluginID)
	}
	if len(registration.Metadata.ConfigFields) != 0 {
		t.Fatalf("config fields = %#v, want none", registration.Metadata.ConfigFields)
	}
	want := capabilityRegistration{
		AuthProvider:          true,
		ModelProvider:         true,
		ModelRouter:           true,
		Executor:              true,
		ExecutorModelScope:    "oauth",
		ExecutorInputFormats:  []string{"chat-completions"},
		ExecutorOutputFormats: []string{"chat-completions"},
		ManagementAPI:         true,
	}
	if got, wantJSON := string(mustJSON(t, registration.Capabilities)), string(mustJSON(t, want)); got != wantJSON {
		t.Fatalf("capabilities = %s, want %s", got, wantJSON)
	}
}

func TestLifecycleRegistrationMatchesReconfigure(t *testing.T) {
	request := mustJSON(t, lifecycleRequest{SchemaVersion: pluginabi.SchemaVersion})
	registered, err := handleMethod(pluginabi.MethodPluginRegister, request)
	if err != nil {
		t.Fatalf("register error = %v", err)
	}
	reconfigured, err := handleMethod(pluginabi.MethodPluginReconfigure, request)
	if err != nil {
		t.Fatalf("reconfigure error = %v", err)
	}
	if string(registered) != string(reconfigured) {
		t.Fatalf("reconfigure differs:\nregister=%s\nreconfigure=%s", registered, reconfigured)
	}
	shutdown, err := handleMethod(pluginabi.MethodPluginShutdown, nil)
	if err != nil {
		t.Fatalf("shutdown error = %v", err)
	}
	var envelope rpcEnvelope
	if err := json.Unmarshal(shutdown, &envelope); err != nil || !envelope.OK || string(envelope.Result) != "{}" {
		t.Fatalf("shutdown envelope = %s, err=%v", shutdown, err)
	}
}

func TestModelRouteForwardsCommandCodeToSelf(t *testing.T) {
	previous := shuttingDown.Load()
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(previous) })
	raw := mustHandle(t, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{
		SourceFormat:   "openai",
		RequestedModel: "deepseek/deepseek-v4-pro",
		Body:           []byte(`{"model":"deepseek/deepseek-v4-pro"}`),
	})
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.ModelRouteResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	if !response.Handled || response.TargetKind != pluginapi.ModelRouteTargetSelf || response.Reason != "commandcode" {
		t.Fatalf("response = %#v", response)
	}
}

func TestModelRouteLeavesForeignModelsUnhandled(t *testing.T) {
	previous := shuttingDown.Load()
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(previous) })
	raw := mustHandle(t, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{
		SourceFormat:   "openai",
		RequestedModel: "gpt-3.5-turbo",
		Body:           []byte(`{"model":"gpt-3.5-turbo"}`),
	})
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.ModelRouteResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	if response.Handled {
		t.Fatalf("foreign model handled: %#v", response)
	}
}

func TestModelRouteLeavesConfiguredModelUnavailableAtBoot(t *testing.T) {
	previous := shuttingDown.Load()
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(previous) })
	raw := mustHandle(t, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{RequestedModel: "cc-pro"})
	var response pluginapi.ModelRouteResponse
	decodeResult(t, raw, &response)
	if response.Handled {
		t.Fatalf("boot/config-less model handled: %#v", response)
	}
}

func TestModelRouteStripsThinkingSuffix(t *testing.T) {
	previous := shuttingDown.Load()
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(previous) })
	raw := mustHandle(t, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{RequestedModel: "deepseek/deepseek-v4-pro(thinking)"})
	var response pluginapi.ModelRouteResponse
	decodeResult(t, raw, &response)
	if !response.Handled || response.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("response = %#v", response)
	}
}

func TestModelRouteRejectsMalformedRequest(t *testing.T) {
	previous := shuttingDown.Load()
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(previous) })
	raw, err := handleMethod(pluginabi.MethodModelRoute, []byte(`{`))
	if err != nil {
		t.Fatal(err)
	}
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "invalid_request" || envelope.Error.Message != "invalid model route request" || envelope.Error.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("envelope = %s", raw)
	}
}

func TestUnknownMethodReturnsStructuredEnvelope(t *testing.T) {
	raw, err := handleMethod("missing.method", nil)
	if err != nil {
		t.Fatalf("handleMethod() error = %v", err)
	}
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "unknown_method" {
		t.Fatalf("envelope = %#v, want unknown_method", envelope)
	}
}

func TestShutdownRejectsNewExecutorAndManagementMutations(t *testing.T) {
	previous := shuttingDown.Load()
	shuttingDown.Store(true)
	t.Cleanup(func() { shuttingDown.Store(previous) })
	calls := 0
	withHostCall(t, func(string, any, any) error { calls++; return nil })

	executorRaw := mustHandle(t, pluginabi.MethodExecutorExecute, executorRPCRequest{})
	routerRaw := mustHandle(t, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{})
	managementRaw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: "POST", Path: managementAccountsPath, Body: []byte(`{"api_key":"user_test"}`)},
	})
	for _, raw := range [][]byte{executorRaw, routerRaw, managementRaw} {
		var envelope rpcEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error == nil || envelope.Error.Code != "plugin_shutdown" || envelope.Error.HTTPStatus != 503 {
			t.Fatalf("shutdown envelope = %s", raw)
		}
	}
	if calls != 0 {
		t.Fatalf("host callbacks after shutdown = %d", calls)
	}
}

func TestShutdownWaitsForWorkersAndIsIdempotent(t *testing.T) {
	previous := shuttingDown.Load()
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(previous) })
	release := make(chan struct{})
	started := make(chan struct{})
	if !startWorker(nil, func() { close(started); <-release }) {
		t.Fatal("worker did not start")
	}
	<-started
	done := make(chan struct{})
	go func() { shutdownPlugin(); close(done) }()
	select {
	case <-done:
		t.Fatal("shutdown returned before worker drained")
	default:
	}
	close(release)
	<-done
	shutdownPlugin()
}

func TestRPCContractRejectsMalformedRequestsStructurally(t *testing.T) {
	previous := shuttingDown.Load()
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(previous) })
	for _, method := range []string{
		pluginabi.MethodAuthParse,
		pluginabi.MethodAuthRefresh,
		pluginabi.MethodModelForAuth,
		pluginabi.MethodExecutorExecute,
		pluginabi.MethodExecutorExecuteStream,
		pluginabi.MethodExecutorHTTPRequest,
		pluginabi.MethodManagementHandle,
	} {
		raw, err := handleMethod(method, []byte(`{bad`))
		if err != nil {
			t.Fatalf("%s native error = %v", method, err)
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("%s invalid envelope: %v: %s", method, err, raw)
		}
		if envelope.OK || envelope.Error == nil || envelope.Error.Code == "" {
			t.Fatalf("%s envelope = %s", method, raw)
		}
	}
}

func TestRPCContractAcceptsCPAWrapperShapes(t *testing.T) {
	previous := shuttingDown.Load()
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(previous) })
	script := &hostCallbackScript{streamChunks: [][]byte{[]byte("{\"type\":\"finish\",\"finishReason\":\"stop\"}\n")}}
	withHostCall(t, func(method string, request any, result any) error {
		if method == pluginabi.MethodHostAuthList {
			result.(*hostAuthListResponse).Files = nil
			return nil
		}
		if method == pluginabi.MethodHostAuthSave {
			return nil
		}
		return script.call(method, request, result)
	})
	requests := []struct {
		method  string
		request any
	}{
		{pluginabi.MethodModelForAuth, authModelRPCRequest{AuthModelRequest: pluginapi.AuthModelRequest{StorageJSON: mustJSON(t, credential{Type: pluginID, APIKey: "user_model"})}, HostCallbackID: "callback"}},
		{pluginabi.MethodExecutorExecute, executorRPCRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), StorageJSON: mustJSON(t, credential{Type: pluginID, APIKey: "user_exec", Models: []credentialModel{{Name: "m"}}})}, HostCallbackID: "callback"}},
		{pluginabi.MethodManagementHandle, managementRPCRequest{ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: resourceAccountsPath}, HostCallbackID: "callback"}},
	}
	for _, item := range requests {
		raw, err := handleMethod(item.method, mustJSON(t, item.request))
		if err != nil {
			t.Fatalf("%s native error = %v", item.method, err)
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
			t.Fatalf("%s envelope = %s, err=%v", item.method, raw, err)
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return raw
}
