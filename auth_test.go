package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func equalIntPtr(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func TestPlanPriorityPresets(t *testing.T) {
	for plan, want := range map[string]int{
		"go": 7, "goat": 6, "pro": 5, "team": 4,
		"max-10x": 3, "max-20x": 2, "provider": 1,
		"unspecified": 0,
	} {
		t.Run(plan, func(t *testing.T) {
			gotPlan, gotPriority, err := normalizePlan(plan)
			if err != nil || gotPlan != plan || gotPriority != want {
				t.Fatalf("normalizePlan(%q) = %q, %d, %v", plan, gotPlan, gotPriority, err)
			}
		})
	}
}

func TestCredentialRoutingNormalization(t *testing.T) {
	tests := []struct {
		name         string
		raw          map[string]any
		wantPlan     string
		wantOverride *int
		wantPriority int
		wantError    error
	}{
		{name: "default", raw: map[string]any{"api_key": "user_default"}, wantPlan: "unspecified", wantPriority: 0},
		{name: "plan", raw: map[string]any{"api_key": "user_go", "plan": " GO "}, wantPlan: "go", wantPriority: 7},
		{name: "override", raw: map[string]any{"api_key": "user_override", "plan": "go", "priority_override": 10}, wantPlan: "go", wantOverride: new(10), wantPriority: 10},
		{name: "legacy", raw: map[string]any{"api_key": "user_legacy", "priority": 4}, wantPlan: "unspecified", wantOverride: new(4), wantPriority: 4},
		{name: "legacy zero", raw: map[string]any{"api_key": "user_legacy_zero", "priority": 0}, wantPlan: "unspecified", wantPriority: 0},
		{name: "null override uses preset", raw: map[string]any{"api_key": "user_null", "plan": "pro", "priority_override": nil, "priority": 9}, wantPlan: "pro", wantPriority: 5},
		{name: "stale derived priority", raw: map[string]any{"api_key": "user_stale", "plan": "go", "priority": 2}, wantPlan: "go", wantPriority: 7},
		{name: "unknown plan", raw: map[string]any{"api_key": "user_bad_plan", "plan": "ultra"}, wantError: errInvalidPlan},
		{name: "zero override", raw: map[string]any{"api_key": "user_zero", "priority_override": 0}, wantError: errInvalidPriority},
		{name: "large override", raw: map[string]any{"api_key": "user_large", "priority_override": 11}, wantError: errInvalidPriority},
		{name: "fraction override", raw: map[string]any{"api_key": "user_fraction", "priority_override": 1.5}, wantError: errInvalidPriority},
		{name: "string override", raw: map[string]any{"api_key": "user_string", "priority_override": "4"}, wantError: errInvalidPriority},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeCredential(mustJSON(t, test.raw))
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("error = %v, want %v", err, test.wantError)
				}
				return
			}
			if err != nil || got.Plan != test.wantPlan || got.Priority != test.wantPriority || !equalIntPtr(got.PriorityOverride, test.wantOverride) {
				t.Fatalf("credential = %#v, err=%v", got, err)
			}
		})
	}
}

func TestCredentialModelValidationAndNormalization(t *testing.T) {
	value, err := normalizeCredential(mustJSON(t, map[string]any{
		"api_key": "user_models",
		"models": []any{
			map[string]any{"name": "deepseek/deepseek-v4-pro", "alias": "cc-pro"},
			map[string]any{"name": "claude-sonnet-5"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Models) != 2 || value.Models[0].Name != "deepseek/deepseek-v4-pro" || value.Models[0].Alias != "cc-pro" || value.Models[1].Name != "claude-sonnet-5" || value.Models[1].Alias != "" {
		t.Fatalf("models = %#v", value.Models)
	}
}

func TestCredentialModelSetRejectsInvalidEntries(t *testing.T) {
	for _, bad := range []any{
		[]any{map[string]any{"alias": "cc-pro"}},
		[]any{map[string]any{"name": "a", "alias": ""}, map[string]any{"name": "a"}},
		[]any{map[string]any{"name": "a", "alias": "x"}, map[string]any{"name": "b", "alias": "x"}},
		[]any{map[string]any{"name": "bad name!"}},
	} {
		if _, err := normalizeCredential(mustJSON(t, map[string]any{"api_key": "user_bad", "models": bad})); !errors.Is(err, errInvalidModelSet) {
			t.Fatalf("models %#v: err=%v, want errInvalidModelSet", bad, err)
		}
	}
}

func TestResolveRequestedModel(t *testing.T) {
	models := []credentialModel{{Name: "deepseek/deepseek-v4-pro", Alias: "cc-pro"}, {Name: "claude-sonnet-5"}}
	for _, tc := range []struct {
		requested, want string
		ok              bool
	}{
		{requested: "deepseek/deepseek-v4-pro", want: "deepseek/deepseek-v4-pro", ok: true},
		{requested: "cc-pro", want: "deepseek/deepseek-v4-pro", ok: true},
		{requested: "claude-sonnet-5", want: "claude-sonnet-5", ok: true},
		{requested: "gpt-5.5", ok: false},
	} {
		got, ok := resolveRequestedModel(models, tc.requested)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("resolveRequestedModel(%q) = %q, %v; want %q, %v", tc.requested, got, ok, tc.want, tc.ok)
		}
	}
	if _, ok := resolveRequestedModel(nil, "deepseek/deepseek-v4-pro"); ok {
		t.Fatal("empty model set must reject all")
	}
	if _, ok := resolveRequestedModel(models, ""); ok {
		t.Fatal("empty requested model must reject")
	}
}

func TestAuthParseAcceptsAliasesAndCanonicalizesStorage(t *testing.T) {
	aliases := []string{"api_key", "apiKey", "COMMAND_CODE_API_KEY", "COMMANDCODE_API_KEY"}
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			key := "user_test_secret_" + alias
			rawAuth := mustJSON(t, map[string]any{
				"type":    " CommandCode-Bridge ",
				alias:     "  " + key + "  ",
				"label":   " Primary ",
				"ignored": "drop-me",
			})
			raw := mustHandle(t, pluginabi.MethodAuthParse, pluginapi.AuthParseRequest{RawJSON: rawAuth})
			var response pluginapi.AuthParseResponse
			decodeResult(t, raw, &response)
			if !response.Handled {
				t.Fatal("Handled = false, want true")
			}
			auth := response.Auth
			wantFingerprint := fingerprint(key)
			if auth.Provider != "commandcode-bridge" || auth.ID != "commandcode-bridge-"+wantFingerprint+".json" || auth.FileName != "commandcode-bridge-"+wantFingerprint+".json" {
				t.Fatalf("auth identity = %#v", auth)
			}
			if auth.Label != "Primary" || auth.Metadata["type"] != "commandcode-bridge" || auth.Metadata["plan"] != "unspecified" || auth.Metadata["priority"] != float64(0) || auth.Attributes["priority"] != "0" {
				t.Fatalf("auth display/metadata = %#v", auth)
			}
			var stored map[string]any
			if err := json.Unmarshal(auth.StorageJSON, &stored); err != nil {
				t.Fatalf("decode storage: %v", err)
			}
			wantStored := map[string]any{"type": "commandcode-bridge", "api_key": key, "label": "Primary", "plan": "unspecified", "priority": 0}
			if got, want := string(mustJSON(t, stored)), string(mustJSON(t, wantStored)); got != want {
				t.Fatalf("storage = %s, want %s", got, want)
			}
		})
	}
}

func TestAuthParseDeclinesLegacyPluginType(t *testing.T) {
	raw := mustHandle(t, pluginabi.MethodAuthParse, pluginapi.AuthParseRequest{
		RawJSON: mustJSON(t, map[string]any{"type": "commandcode", "api_key": "user_legacy"}),
	})
	var response pluginapi.AuthParseResponse
	decodeResult(t, raw, &response)
	if response.Handled {
		t.Fatalf("legacy plugin type was handled: %#v", response)
	}
}

func TestAuthParseKeepsUpstreamCredentialAliases(t *testing.T) {
	for _, alias := range []string{"api_key", "apiKey", "COMMAND_CODE_API_KEY", "COMMANDCODE_API_KEY"} {
		value, err := normalizeCredential(mustJSON(t, map[string]any{"type": "commandcode-bridge", alias: "user_alias"}))
		if err != nil || value.APIKey != "user_alias" {
			t.Fatalf("alias %s: credential=%#v err=%v", alias, value, err)
		}
	}
}

func TestAuthParseDeclinesUnrelatedJSON(t *testing.T) {
	raw := mustHandle(t, pluginabi.MethodAuthParse, pluginapi.AuthParseRequest{
		RawJSON: mustJSON(t, map[string]any{"type": "other", "api_key": "user_other"}),
	})
	var response pluginapi.AuthParseResponse
	decodeResult(t, raw, &response)
	if response.Handled {
		t.Fatalf("response = %#v, want Handled false", response)
	}
}

func TestAuthParseRejectsInvalidCredentialWithoutLeakingIt(t *testing.T) {
	for _, key := range []string{"", "secret-without-prefix"} {
		raw, err := handleMethod(pluginabi.MethodAuthParse, mustJSON(t, pluginapi.AuthParseRequest{
			RawJSON: mustJSON(t, map[string]any{"type": pluginID, "api_key": key}),
		}))
		if err != nil {
			t.Fatalf("handleMethod() error = %v", err)
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		if envelope.OK || envelope.Error == nil || envelope.Error.Code != "invalid_credentials" || envelope.Error.HTTPStatus != 401 {
			t.Fatalf("envelope = %#v", envelope)
		}
		if key != "" && strings.Contains(string(raw), key) {
			t.Fatalf("error leaked key: %s", raw)
		}
	}
}

func TestAuthDerivedValuesNeverExposeKey(t *testing.T) {
	key := "user_super_secret_value"
	credential, err := normalizeCredential(mustJSON(t, map[string]any{"apiKey": key}))
	if err != nil {
		t.Fatalf("normalizeCredential() error = %v", err)
	}
	auth := credentialAuthData(credential)
	for field, value := range map[string]string{
		"id": auth.ID, "filename": auth.FileName, "label": auth.Label,
	} {
		if strings.Contains(value, key) || strings.Contains(value, "super_secret") {
			t.Fatalf("%s leaked key: %q", field, value)
		}
	}
	if len(fingerprint(key)) != 12 {
		t.Fatalf("fingerprint length = %d", len(fingerprint(key)))
	}
}

func TestAuthRefreshReturnsCanonicalCredential(t *testing.T) {
	storage := mustJSON(t, map[string]any{"type": pluginID, "apiKey": "user_refresh", "plan": "go", "priority": 2, "extra": true})
	raw := mustHandle(t, pluginabi.MethodAuthRefresh, pluginapi.AuthRefreshRequest{StorageJSON: storage})
	var response pluginapi.AuthRefreshResponse
	decodeResult(t, raw, &response)
	if response.Auth.Provider != pluginID || response.Auth.ID != pluginID+"-"+fingerprint("user_refresh")+".json" || response.Auth.Metadata["plan"] != "go" || response.Auth.Metadata["priority"] != float64(7) || response.Auth.Attributes["priority"] != "7" {
		t.Fatalf("response = %#v", response)
	}
	if strings.Contains(string(response.Auth.StorageJSON), "extra") {
		t.Fatalf("storage retained unknown field: %s", response.Auth.StorageJSON)
	}
}

func TestAuthRoutingFailuresAreRedacted(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
	}{
		{name: "unknown plan", raw: map[string]any{"api_key": "user_routing_plan", "plan": "ultra"}},
		{name: "large override", raw: map[string]any{"api_key": "user_routing_override", "priority_override": 11}},
	}
	for _, test := range tests {
		for _, method := range []string{pluginabi.MethodAuthParse, pluginabi.MethodAuthRefresh} {
			t.Run(test.name+"/"+method, func(t *testing.T) {
				credentialJSON := mustJSON(t, test.raw)
				var request any = pluginapi.AuthParseRequest{RawJSON: credentialJSON}
				if method == pluginabi.MethodAuthRefresh {
					request = pluginapi.AuthRefreshRequest{StorageJSON: credentialJSON}
				}
				raw, err := handleMethod(method, mustJSON(t, request))
				if err != nil {
					t.Fatalf("handleMethod() error = %v", err)
				}
				var envelope rpcEnvelope
				if err := json.Unmarshal(raw, &envelope); err != nil {
					t.Fatalf("decode envelope: %v", err)
				}
				if envelope.OK || envelope.Error == nil || envelope.Error.Code != "invalid_routing" || envelope.Error.HTTPStatus != 400 {
					t.Fatalf("envelope = %#v", envelope)
				}
				if strings.Contains(string(raw), test.raw["api_key"].(string)) {
					t.Fatalf("error leaked key: %s", raw)
				}
			})
		}
	}
}

func TestAuthLoginMethodsAreUnsupported(t *testing.T) {
	for _, method := range []string{pluginabi.MethodAuthLoginStart, pluginabi.MethodAuthLoginPoll} {
		raw, err := handleMethod(method, []byte(`{}`))
		if err != nil {
			t.Fatalf("%s error = %v", method, err)
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("decode %s: %v", method, err)
		}
		if envelope.OK || envelope.Error == nil || envelope.Error.Code != "not_supported" || envelope.Error.HTTPStatus != 501 {
			t.Fatalf("%s envelope = %#v", method, envelope)
		}
	}
}

func mustHandle(t *testing.T, method string, request any) []byte {
	t.Helper()
	raw, err := handleMethod(method, mustJSON(t, request))
	if err != nil {
		t.Fatalf("handleMethod(%s) error = %v", method, err)
	}
	return raw
}

func decodeResult(t *testing.T, raw []byte, out any) {
	t.Helper()
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !envelope.OK || envelope.Error != nil {
		t.Fatalf("envelope = %#v", envelope)
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
}
