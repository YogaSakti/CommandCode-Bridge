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
	previous := hostCall
	t.Cleanup(func() { hostCall = previous })
	var gotMethod string
	var gotRequest hostHTTPRequest
	hostCall = func(method string, request any, result any) error {
		gotMethod = method
		gotRequest = request.(hostHTTPRequest)
		response := result.(*pluginapi.HTTPResponse)
		*response = pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"object":"list","data":[{"id":"live","object":"model","name":"Live","context_length":123}]}`),
		}
		return nil
	}
	raw := mustHandle(t, pluginabi.MethodModelForAuth, authModelRPCRequest{HostCallbackID: "callback-1"})
	var response pluginapi.ModelResponse
	decodeResult(t, raw, &response)
	if gotMethod != pluginabi.MethodHostHTTPDo || gotRequest.HostCallbackID != "callback-1" || gotRequest.Method != http.MethodGet || gotRequest.URL != modelCatalogURL {
		t.Fatalf("host call = %s %#v", gotMethod, gotRequest)
	}
	if len(response.Models) != 1 || response.Models[0].ID != "live" {
		t.Fatalf("response = %#v", response)
	}
}

func TestModelForAuthFallsBackForEveryLiveFailure(t *testing.T) {
	previous := hostCall
	t.Cleanup(func() { hostCall = previous })
	cases := []struct {
		name string
		call func(string, any, any) error
	}{
		{"callback error", func(string, any, any) error { return errors.New("offline") }},
		{"non-2xx", func(_ string, _ any, out any) error {
			*(out.(*pluginapi.HTTPResponse)) = pluginapi.HTTPResponse{StatusCode: 503, Body: []byte(`down`)}
			return nil
		}},
		{"invalid json", func(_ string, _ any, out any) error {
			*(out.(*pluginapi.HTTPResponse)) = pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{`)}
			return nil
		}},
		{"empty set", func(_ string, _ any, out any) error {
			*(out.(*pluginapi.HTTPResponse)) = pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"object":"list","data":[]}`)}
			return nil
		}},
	}
	want := snapshotModels()
	if len(want) < 40 {
		t.Fatalf("snapshot model count = %d", len(want))
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			hostCall = test.call
			raw := mustHandle(t, pluginabi.MethodModelForAuth, authModelRPCRequest{HostCallbackID: "callback"})
			var response pluginapi.ModelResponse
			decodeResult(t, raw, &response)
			if response.Provider != pluginID || !reflect.DeepEqual(response.Models, want) {
				t.Fatalf("fallback differs: got %d models, want %d", len(response.Models), len(want))
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
