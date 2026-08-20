package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestModelCatalogMapsSortsAndDeduplicates(t *testing.T) {
	raw := []byte(`{"object":"list","data":[
		{"id":"z-model","object":"model","created":2,"owned_by":"cc","name":"Z","context_length":20},
		{"id":"a-model","object":"model","created":1,"owned_by":"cc","name":"A","context_length":10},
		{"id":"a-model","object":"model","created":9,"owned_by":"other","name":"duplicate","context_length":99},
		{"id":"","name":"invalid"}
	]}`)
	models, err := parseModelCatalog(raw)
	if err != nil {
		t.Fatalf("parseModelCatalog() error = %v", err)
	}
	if len(models) != 2 || models[0].ID != "a-model" || models[1].ID != "z-model" {
		t.Fatalf("models = %#v", models)
	}
	if models[0].Object != "model" || models[0].Created != 1 || models[0].OwnedBy != "cc" || models[0].Name != "A" || models[0].ContextLength != 10 {
		t.Fatalf("mapped model = %#v", models[0])
	}
}

func TestModelCatalogRejectsInvalidOrEmptySets(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`not-json`),
		[]byte(`{"object":"list","data":[]}`),
		[]byte(`{"object":"list","data":[{"id":""}]}`),
	} {
		if _, err := parseModelCatalog(raw); err == nil {
			t.Fatalf("parseModelCatalog(%s) error = nil", raw)
		}
	}
}

func TestModelStaticReturnsEmptyOAuthCatalog(t *testing.T) {
	raw := mustHandle(t, pluginabi.MethodModelStatic, pluginapi.StaticModelRequest{})
	var response pluginapi.ModelResponse
	decodeResult(t, raw, &response)
	if response.Provider != pluginID || len(response.Models) != 0 {
		t.Fatalf("response = %#v", response)
	}
}

func TestModelForAuthUsesLiveCatalogAndCallbackID(t *testing.T) {
	var gotMethod string
	var gotRequest hostHTTPRequest
	withHostCall(t, func(method string, request any, result any) error {
		gotMethod = method
		gotRequest = request.(hostHTTPRequest)
		*result.(*pluginapi.HTTPResponse) = pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"object":"list","data":[{"id":"live","object":"model","name":"Live","context_length":123},{"id":"unselected","context_length":456}]}`),
		}
		return nil
	})
	raw := mustHandle(t, pluginabi.MethodModelForAuth, authModelRPCRequest{
		AuthModelRequest: pluginapi.AuthModelRequest{StorageJSON: mustJSON(t, credential{
			Type: pluginID, APIKey: "user_model", Models: []credentialModel{{Name: "live", Alias: "live-alias"}},
		})},
		HostCallbackID: "callback-1",
	})
	var response pluginapi.ModelResponse
	decodeResult(t, raw, &response)
	if gotMethod != pluginabi.MethodHostHTTPDo || gotRequest.HostCallbackID != "callback-1" || gotRequest.Method != http.MethodGet || gotRequest.URL != modelCatalogURL || gotRequest.Headers.Get("Accept") != "application/json" || gotRequest.Headers.Get("Authorization") != "Bearer user_model" {
		t.Fatalf("host call = %s %#v", gotMethod, gotRequest)
	}
	want := []pluginapi.ModelInfo{{ID: "live-alias", Name: "live", DisplayName: "live-alias", ContextLength: 123, SupportedGenerationMethods: []string{"chat"}, SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}}}
	if response.Provider != pluginID || !reflect.DeepEqual(response.Models, want) {
		t.Fatalf("response = %#v", response)
	}
}

func TestModelForAuthKeepsSelectedModelsForEveryLiveFailure(t *testing.T) {
	cases := []struct {
		name string
		call func(string, any, any) error
	}{
		{"callback error", func(string, any, any) error { return errors.New("offline") }},
		{"non-2xx", func(_ string, _ any, out any) error {
			*(out.(*pluginapi.HTTPResponse)) = pluginapi.HTTPResponse{StatusCode: http.StatusServiceUnavailable, Body: []byte(`down`)}
			return nil
		}},
		{"invalid json", func(_ string, _ any, out any) error {
			*(out.(*pluginapi.HTTPResponse)) = pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{`)}
			return nil
		}},
		{"empty set", func(_ string, _ any, out any) error {
			*(out.(*pluginapi.HTTPResponse)) = pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"object":"list","data":[]}`)}
			return nil
		}},
	}
	want := []pluginapi.ModelInfo{{
		ID: "shown", Name: "selected", DisplayName: "shown",
		SupportedGenerationMethods: []string{"chat"},
		SupportedInputModalities:   []string{"text"},
		SupportedOutputModalities:  []string{"text"},
	}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			withHostCall(t, test.call)
			raw := mustHandle(t, pluginabi.MethodModelForAuth, authModelRPCRequest{
				AuthModelRequest: pluginapi.AuthModelRequest{StorageJSON: mustJSON(t, credential{
					Type: pluginID, APIKey: "user_model", Models: []credentialModel{{Name: "selected", Alias: "shown"}},
				})},
				HostCallbackID: "callback",
			})
			var response pluginapi.ModelResponse
			decodeResult(t, raw, &response)
			if response.Provider != pluginID || !reflect.DeepEqual(response.Models, want) {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestSnapshotModelsReturnsIndependentSlices(t *testing.T) {
	first := snapshotModels()
	second := snapshotModels()
	if len(first) == 0 {
		t.Fatal("snapshot is empty")
	}
	original := second[0].ID
	first[0].ID = "mutated"
	if second[0].ID != original {
		t.Fatal("snapshot slices share model state")
	}
}

func TestModelResponseJSONUsesCurrentPluginAPIShape(t *testing.T) {
	raw, err := json.Marshal(pluginapi.ModelResponse{Provider: pluginID, Models: snapshotModels()[:1]})
	if err != nil || len(raw) == 0 {
		t.Fatalf("marshal model response: %v", err)
	}
}

func TestModelForAuthReturnsOnlySelectedModels(t *testing.T) {
	raw := mustHandle(t, pluginabi.MethodModelForAuth, authModelRPCRequest{
		AuthModelRequest: pluginapi.AuthModelRequest{
			StorageJSON: mustJSON(t, map[string]any{
				"type":    pluginID,
				"api_key": "user_discover",
				"models": []any{
					map[string]any{"name": "deepseek/deepseek-v4-pro", "alias": "cc-pro"},
					map[string]any{"name": "claude-sonnet-5"},
				},
			}),
		},
	})
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var resp pluginapi.ModelResponse
	if err := json.Unmarshal(envelope.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("models = %#v", resp.Models)
	}
	if resp.Models[0].ID != "cc-pro" || resp.Models[0].Name != "deepseek/deepseek-v4-pro" || resp.Models[0].DisplayName != "cc-pro" {
		t.Fatalf("model[0] = %#v", resp.Models[0])
	}
	if resp.Models[1].ID != "claude-sonnet-5" || resp.Models[1].Name != "claude-sonnet-5" || resp.Models[1].DisplayName != "claude-sonnet-5" {
		t.Fatalf("model[1] = %#v", resp.Models[1])
	}
}

func TestModelForAuthEmptySetReturnsZero(t *testing.T) {
	raw := mustHandle(t, pluginabi.MethodModelForAuth, authModelRPCRequest{
		AuthModelRequest: pluginapi.AuthModelRequest{
			StorageJSON: mustJSON(t, map[string]any{"type": pluginID, "api_key": "user_discover_empty"}),
		},
	})
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var resp pluginapi.ModelResponse
	if err := json.Unmarshal(envelope.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) != 0 {
		t.Fatalf("models = %#v, want zero", resp.Models)
	}
}
