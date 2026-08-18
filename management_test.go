package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestAccountsPageIsStaticAndKeepsMutationsAuthenticated(t *testing.T) {
	raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: resourceAccountsFullPath},
	})
	var response pluginapi.ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != http.StatusOK || response.Headers.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("response = %#v", response)
	}
	if response.Headers.Get("Content-Security-Policy") != accountsPageCSP || response.Headers.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("security headers = %#v", response.Headers)
	}
	page := string(response.Body)
	for _, want := range []string{
		`<title>CommandCode Bridge Accounts</title>`, `<h1>CommandCode Bridge Accounts</h1>`, `CommandCode API key`,
		`/v0/management/plugins/commandcode-bridge/accounts`,
		`/v0/management/plugins/commandcode-bridge/import-local`,
		`/v0/management/plugins/commandcode-bridge/validate`,
		`/v0/management/auth-files/fields`, `method='PATCH'`,
		`Authorization`, `Bearer `, `cli-proxy-auth`, `enc::v1::`, `managementKey`, `sessionKey`,
		`href="/management.html#/auth-files"`, `type="password"`, `<form`, `<table`,
		`name="plan"`, `name="priority_override"`, `min="1"`, `max="10"`,
		`Round-robin`, `Fill-first`, `Effective priority`, `<th>Action</th>`, `colSpan=7`,
		`setAttribute('aria-label',`, `Edit routing for ${account.filename}`, `Save routing for ${account.filename}`,
		`button.onclick=()=>{`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`http://`, `https://`, `<script src=`, `<link rel=`, `localStorage.setItem`, `sessionStorage`,
		`/v0/management/plugins/commandcode/`, `/v0/resource/plugins/commandcode/`, `CommandCode Accounts`,
		`type="file"`, `name="path"`, `deleteAccount`, `method: "DELETE"`,
		`accounts.innerHTML`, `account.api_key`, `account.auth_index`, `account.index`, `account.path`,
		`button.addEventListener('click'`,
	} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("page contains forbidden %q", forbidden)
		}
	}
}

func TestAccountsPageResourceGetDoesNotOpenHostCallbacks(t *testing.T) {
	calls := 0
	withHostCall(t, func(string, any, any) error { calls++; return nil })
	_ = mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: resourceAccountsFullPath},
	})
	if calls != 0 {
		t.Fatalf("resource GET opened %d host callbacks", calls)
	}
}

func TestManagementRegisterDeclaresExactBoundary(t *testing.T) {
	raw := mustHandle(t, pluginabi.MethodManagementRegister, pluginapi.ManagementRegistrationRequest{})
	var response managementRegistrationResponse
	decodeResult(t, raw, &response)
	if len(response.Resources) != 1 || response.Resources[0].Path != "/accounts" || response.Resources[0].Menu != "CommandCode Bridge Accounts" {
		t.Fatalf("resources = %#v", response.Resources)
	}
	wantRoutes := map[string]bool{
		"GET /plugins/commandcode-bridge/accounts":      true,
		"POST /plugins/commandcode-bridge/accounts":     true,
		"POST /plugins/commandcode-bridge/import-local": true,
		"POST /plugins/commandcode-bridge/validate":     true,
	}
	if len(response.Routes) != len(wantRoutes) {
		t.Fatalf("routes = %#v", response.Routes)
	}
	for _, route := range response.Routes {
		key := route.Method + " " + route.Path
		if !wantRoutes[key] || strings.Contains(strings.ToLower(route.Path), "delete") {
			t.Fatalf("unexpected route %#v", route)
		}
	}
}

func TestManagementHandlesFullCPAPaths(t *testing.T) {
	withHostCall(t, func(method string, _ any, result any) error {
		if method == pluginabi.MethodHostAuthList {
			result.(*hostAuthListResponse).Files = nil
		}
		return nil
	})
	for _, path := range []string{managementAccountsFullPath, resourceAccountsFullPath} {
		raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: path}})
		var response pluginapi.ManagementResponse
		decodeResult(t, raw, &response)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("path %s status=%d body=%s", path, response.StatusCode, response.Body)
		}
	}
}

func TestManagementRegistrationUsesCPAFieldNames(t *testing.T) {
	raw := mustHandle(t, pluginabi.MethodManagementRegister, pluginapi.ManagementRegistrationRequest{})
	for _, want := range []string{`"routes"`, `"Method":"GET"`, `"Path":"/plugins/commandcode-bridge/accounts"`, `"resources"`, `"Menu":"CommandCode Bridge Accounts"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("registration missing %s: %s", want, raw)
		}
	}
}

func TestManagementListFiltersAndRedactsAccounts(t *testing.T) {
	const filename = "commandcode-bridge-a1b2c3d4e5f6.json"
	withHostCall(t, func(method string, request any, result any) error {
		switch method {
		case pluginabi.MethodHostAuthList:
			result.(*hostAuthListResponse).Files = []pluginapi.HostAuthFileEntry{
				{ID: "secret-id", AuthIndex: "idx", Name: filename, Provider: pluginID, Label: "Listed label", Status: "active", Disabled: true, Unavailable: true, Path: "/secret/path", Account: "user_list_secret", Success: 4, Failed: 2},
				{Provider: "commandcode", Name: "legacy-provider.json"},
				{Type: "commandcode", Name: "legacy-type.json"},
				{Name: "commandcode-aaaaaaaaaaaa.json"},
				{Name: "other.json", Provider: "other"},
			}
			return nil
		case pluginabi.MethodHostAuthGet:
			if request.(pluginapi.HostAuthGetRequest).AuthIndex != "idx" {
				t.Fatalf("auth get request = %#v", request)
			}
			*result.(*pluginapi.HostAuthGetResponse) = pluginapi.HostAuthGetResponse{
				AuthIndex: "idx",
				Name:      filepath.Join("/private/records", filename),
				Path:      "/private/records/" + filename,
				JSON:      mustJSON(t, map[string]any{"api_key": "user_list_secret", "label": "Physical label", "plan": "go", "priority": 7}),
			}
			return nil
		default:
			t.Fatalf("method = %s", method)
			return nil
		}
	})
	raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: managementAccountsPath},
		HostCallbackID:    "callback",
	})
	var response pluginapi.ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	text := string(response.Body)
	for _, forbidden := range []string{"user_list_secret", "idx", "secret-id", "/secret/path", "/private/records", `"auth_index"`, `"id"`, `"path"`, `"json"`, "callback", `"success"`, `"failed"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, text)
		}
	}
	var payload struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Accounts) != 1 {
		t.Fatalf("accounts = %#v", payload.Accounts)
	}
	account := payload.Accounts[0]
	if account["filename"] != filename || account["fingerprint"] != "a1b2c3d4e5f6" || account["label"] != "Physical label" || account["plan"] != "go" || account["priority_override"] != nil || account["effective_priority"] != float64(7) || account["status"] != "active" || account["disabled"] != true || account["unavailable"] != true || account["editable"] != true {
		t.Fatalf("account = %#v", account)
	}
	allowed := map[string]bool{
		"filename": true, "fingerprint": true, "label": true,
		"plan": true, "priority_override": true,
		"effective_priority": true, "status": true,
		"disabled": true, "unavailable": true, "editable": true,
	}
	for field := range account {
		if !allowed[field] {
			t.Fatalf("unexpected response field %q in %#v", field, account)
		}
	}
}

func TestManagementListDegradesBadPhysicalRows(t *testing.T) {
	const goodFilename = "commandcode-bridge-aaaaaaaaaaaa.json"
	getCalls := 0
	withHostCall(t, func(method string, request any, result any) error {
		switch method {
		case pluginabi.MethodHostAuthList:
			result.(*hostAuthListResponse).Files = []pluginapi.HostAuthFileEntry{
				{ID: "good-record-id", AuthIndex: "good-index", Name: goodFilename, Provider: pluginID, Label: "Listed good", Status: "active", Path: "/private/good", Account: "user_good_secret", Priority: 1},
				{ID: "failed-record-id", AuthIndex: "failed-index", Name: "commandcode-bridge-bbbbbbbbbbbb.json", Provider: pluginID, Label: "Listed failed", Path: "/private/failed", Account: "user_failed_secret", Priority: 2},
				{ID: "malformed-record-id", AuthIndex: "malformed-index", Name: "commandcode-bridge-cccccccccccc.json", Provider: pluginID, Label: "Listed malformed", Path: "/private/malformed", Account: "user_malformed_secret", Priority: 3},
				{ID: "missing-record-id", Name: "commandcode-bridge-dddddddddddd.json", Provider: pluginID, Label: "Listed missing", Path: "/private/missing", Account: "user_missing_secret", Priority: 4},
				{ID: "runtime-record-id", AuthIndex: "runtime-index", Name: "commandcode-bridge-eeeeeeeeeeee.json", Provider: pluginID, Label: "Listed runtime", RuntimeOnly: true, Path: "/private/runtime", Account: "user_runtime_secret", Priority: 5},
			}
			return nil
		case pluginabi.MethodHostAuthGet:
			getCalls++
			switch request.(pluginapi.HostAuthGetRequest).AuthIndex {
			case "good-index":
				*result.(*pluginapi.HostAuthGetResponse) = pluginapi.HostAuthGetResponse{Name: goodFilename, JSON: mustJSON(t, map[string]any{"api_key": "user_good_secret", "label": "Physical good", "plan": "go", "priority": 7})}
				return nil
			case "failed-index":
				return errors.New("host auth get failed")
			case "malformed-index":
				*result.(*pluginapi.HostAuthGetResponse) = pluginapi.HostAuthGetResponse{Name: "commandcode-bridge-cccccccccccc.json", JSON: []byte(`{`)}
				return nil
			default:
				t.Fatalf("unexpected auth get request %#v", request)
				return nil
			}
		default:
			t.Fatalf("method = %s", method)
			return nil
		}
	})
	raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: managementAccountsPath}})
	var response pluginapi.ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if getCalls != 3 {
		t.Fatalf("host auth get calls = %d, want 3", getCalls)
	}
	text := string(response.Body)
	for _, forbidden := range []string{"good-record-id", "failed-record-id", "malformed-record-id", "missing-record-id", "runtime-record-id", "good-index", "failed-index", "malformed-index", "runtime-index", "/private/", "user_good_secret", "user_failed_secret", "user_malformed_secret", "user_missing_secret", "user_runtime_secret", `"auth_index"`, `"id"`, `"path"`, `"json"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, text)
		}
	}
	var payload struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Accounts) != 5 {
		t.Fatalf("accounts = %#v", payload.Accounts)
	}
	wantNames := []string{goodFilename, "commandcode-bridge-bbbbbbbbbbbb.json", "commandcode-bridge-cccccccccccc.json", "commandcode-bridge-dddddddddddd.json", "commandcode-bridge-eeeeeeeeeeee.json"}
	allowed := map[string]bool{
		"filename": true, "fingerprint": true, "label": true,
		"plan": true, "priority_override": true,
		"effective_priority": true, "status": true,
		"disabled": true, "unavailable": true, "editable": true,
	}
	for index, account := range payload.Accounts {
		if account["filename"] != wantNames[index] {
			t.Fatalf("accounts = %#v", payload.Accounts)
		}
		for field := range account {
			if !allowed[field] {
				t.Fatalf("unexpected response field %q in %#v", field, account)
			}
		}
		if account["filename"] == goodFilename {
			if account["plan"] != "go" || account["effective_priority"] != float64(7) || account["editable"] != true {
				t.Fatalf("good account = %#v", account)
			}
			continue
		}
		if account["plan"] != "unspecified" || account["editable"] != false {
			t.Fatalf("degraded account = %#v", account)
		}
	}
}

func TestManagementListDeduplicatesByAuthIndex(t *testing.T) {
	const filename = "commandcode-bridge-1847417fcfce.json"
	withHostCall(t, func(method string, request any, result any) error {
		switch method {
		case pluginabi.MethodHostAuthList:
			result.(*hostAuthListResponse).Files = []pluginapi.HostAuthFileEntry{
				{ID: "commandcode-bridge-1847417fcfce", AuthIndex: "0b4a9c19524758b9", Name: filename, Provider: pluginID, Label: "ACC 1", Status: "active", Priority: 7},
				{ID: "commandcode-bridge-1847417fcfce.json", AuthIndex: "0b4a9c19524758b9", Name: filename, Provider: pluginID, Label: "ACC 1", Status: "active", Priority: 7},
				{ID: "commandcode-bridge-d25d345d901c", AuthIndex: "443f07786195a9ca", Name: "commandcode-bridge-d25d345d901c.json", Provider: pluginID, Label: "ACC 2", Status: "active", Priority: 7},
			}
			return nil
		case pluginabi.MethodHostAuthGet:
			req := request.(pluginapi.HostAuthGetRequest)
			var value map[string]any
			switch req.AuthIndex {
			case "0b4a9c19524758b9":
				value = map[string]any{"api_key": "user_aaa", "label": "ACC 1", "plan": "go", "priority": 7}
			case "443f07786195a9ca":
				value = map[string]any{"api_key": "user_bbb", "label": "ACC 2", "plan": "go", "priority": 7}
			default:
				t.Fatalf("unexpected auth index %q", req.AuthIndex)
			}
			*result.(*pluginapi.HostAuthGetResponse) = pluginapi.HostAuthGetResponse{Name: filename, JSON: mustJSON(t, value)}
			return nil
		default:
			t.Fatalf("method = %s", method)
			return nil
		}
	})
	raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: managementAccountsPath},
	})
	var response pluginapi.ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	var payload struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Accounts) != 2 {
		t.Fatalf("accounts = %#v, want 2", payload.Accounts)
	}
	if payload.Accounts[0]["fingerprint"] != "1847417fcfce" || payload.Accounts[1]["fingerprint"] != "d25d345d901c" {
		t.Fatalf("accounts = %#v", payload.Accounts)
	}
}

func TestManagementEnrollmentValidatesBeforeSave(t *testing.T) {
	fixture := []byte("{\"type\":\"text-delta\",\"text\":\"pong\"}\n{\"type\":\"finish\",\"finishReason\":\"stop\"}\n")
	script := &hostCallbackScript{streamChunks: [][]byte{fixture}}
	allCalls := make([]hostCallbackRecord, 0)
	var callsMu sync.Mutex
	var saved credential
	withHostCall(t, func(method string, request any, result any) error {
		callsMu.Lock()
		allCalls = append(allCalls, hostCallbackRecord{Method: method, Request: request})
		callsMu.Unlock()
		if method == pluginabi.MethodHostAuthList {
			result.(*hostAuthListResponse).Files = nil
			return nil
		}
		if method == pluginabi.MethodHostAuthSave {
			request := request.(pluginapi.HostAuthSaveRequest)
			if err := json.Unmarshal(request.JSON, &saved); err != nil {
				t.Fatal(err)
			}
			response := result.(*pluginapi.HostAuthSaveResponse)
			response.Name = request.Name
			response.Path = "/auth/" + request.Name
			return nil
		}
		return script.call(method, request, result)
	})
	raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodPost, Path: managementAccountsPath,
			Body: mustJSON(t, map[string]any{"api_key": "user_enroll", "label": "Work", "model": "deepseek/deepseek-v4-pro", "plan": "go", "priority_override": 8}),
		},
		HostCallbackID: "callback",
	})
	var response pluginapi.ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if countRecordedCalls(allCalls, pluginabi.MethodHostHTTPDoStream) != 1 || countRecordedCalls(allCalls, pluginabi.MethodHostHTTPStreamClose) != 1 || countRecordedCalls(allCalls, pluginabi.MethodHostAuthSave) != 1 {
		t.Fatalf("callback order/count = %#v", allCalls)
	}
	if saved.APIKey != "user_enroll" || saved.Label != "Work" || saved.Plan != "go" || saved.PriorityOverride == nil || *saved.PriorityOverride != 8 || saved.Priority != 8 {
		t.Fatalf("saved credential = %#v", saved)
	}
	text := string(response.Body)
	if strings.Contains(text, "user_enroll") || !strings.Contains(text, fingerprint("user_enroll")) {
		t.Fatalf("response = %s", text)
	}
	var payload struct {
		Account map[string]any `json:"account"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Account["plan"] != "go" || payload.Account["priority_override"] != float64(8) || payload.Account["effective_priority"] != float64(8) {
		t.Fatalf("account = %#v", payload.Account)
	}
}

func TestManagementRejectsInvalidRoutingBeforeHostCallbacks(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{name: "plan", body: map[string]any{"api_key": "user_invalid_plan", "plan": "ultra"}},
		{name: "zero override", body: map[string]any{"api_key": "user_zero_override", "priority_override": 0}},
		{name: "large override", body: map[string]any{"api_key": "user_large_override", "priority_override": 11}},
		{name: "fractional override", body: map[string]any{"api_key": "user_fractional_override", "priority_override": 1.5}},
		{name: "string override", body: map[string]any{"api_key": "user_string_override", "priority_override": "8"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			callbacks := 0
			withHostCall(t, func(string, any, any) error {
				callbacks++
				return errors.New("unexpected host callback")
			})
			raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
				ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodPost, Path: managementAccountsPath, Body: mustJSON(t, test.body)},
				HostCallbackID:    "callback",
			})
			var response pluginapi.ManagementResponse
			decodeResult(t, raw, &response)
			if response.StatusCode != http.StatusBadRequest || callbacks != 0 {
				t.Fatalf("status=%d callbacks=%d body=%s", response.StatusCode, callbacks, response.Body)
			}
		})
	}
}

func TestManagementValidationRequestUsesPingAndSingleToken(t *testing.T) {
	fixture := []byte("{\"type\":\"finish\",\"finishReason\":\"stop\"}\n")
	script := &hostCallbackScript{streamChunks: [][]byte{fixture}}
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
	_ = mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodPost, Path: managementAccountsPath, Body: mustJSON(t, map[string]string{"api_key": "user_validation"})},
		HostCallbackID:    "callback",
	})
	request := firstCall(t, script, pluginabi.MethodHostHTTPDoStream).Request.(hostHTTPRequest)
	var body struct {
		Params struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Stream    bool   `json:"stream"`
			Messages  []struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		} `json:"params"`
	}
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	wantModel := snapshotModels()[0].ID
	if body.Params.Model != wantModel || body.Params.MaxTokens != 1 || !body.Params.Stream || len(body.Params.Messages) != 1 || body.Params.Messages[0].Role != "user" || len(body.Params.Messages[0].Content) != 1 || body.Params.Messages[0].Content[0].Text != "ping" {
		t.Fatalf("validation body = %s", request.Body)
	}
}

func TestManagementRejectsDuplicateBeforeNetwork(t *testing.T) {
	script := &hostCallbackScript{}
	withHostCall(t, func(method string, request any, result any) error {
		if method == pluginabi.MethodHostAuthList {
			result.(*hostAuthListResponse).Files = []pluginapi.HostAuthFileEntry{{Name: "commandcode-bridge-" + fingerprint("user_duplicate") + ".json", Provider: pluginID}}
			return nil
		}
		return script.call(method, request, result)
	})
	raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodPost, Path: managementAccountsPath, Body: mustJSON(t, map[string]string{"api_key": "user_duplicate"})},
		HostCallbackID:    "callback",
	})
	var response pluginapi.ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	assertCallCount(t, script, pluginabi.MethodHostHTTPDoStream, 0)
	assertCallCount(t, script, pluginabi.MethodHostAuthSave, 0)
}

func TestManagementValidationFailuresNeverSave(t *testing.T) {
	cases := []struct {
		name   string
		status int
		chunks [][]byte
		err    error
	}{
		{"unauthorized", 401, nil, nil},
		{"forbidden", 403, nil, nil},
		{"server", 500, nil, nil},
		{"protocol", 200, [][]byte{[]byte("{bad}\n")}, nil},
		{"premature eof", 200, [][]byte{[]byte("{\"type\":\"text-delta\",\"text\":\"x\"}\n")}, nil},
		{"callback", 0, nil, errors.New("offline")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			saves := 0
			script := &hostCallbackScript{statusCode: test.status, streamChunks: test.chunks}
			withHostCall(t, func(method string, request any, result any) error {
				if method == pluginabi.MethodHostAuthList {
					result.(*hostAuthListResponse).Files = nil
					return nil
				}
				if method == pluginabi.MethodHostAuthSave {
					saves++
					return nil
				}
				if method == pluginabi.MethodHostHTTPDoStream && test.err != nil {
					return test.err
				}
				return script.call(method, request, result)
			})
			raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
				ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodPost, Path: managementAccountsPath, Body: mustJSON(t, map[string]string{"api_key": "user_failure"})},
				HostCallbackID:    "callback",
			})
			var response pluginapi.ManagementResponse
			decodeResult(t, raw, &response)
			if response.StatusCode < 400 || saves != 0 {
				t.Fatalf("status=%d saves=%d body=%s", response.StatusCode, saves, response.Body)
			}
		})
	}
}

func TestManagementRejectsOversizedBody(t *testing.T) {
	const oversizedBody = 65 * 1024
	raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodPost, Path: managementAccountsPath, Body: []byte(`{"api_key":"user_` + strings.Repeat("x", oversizedBody) + `"}`)},
	})
	var response pluginapi.ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
	}
}

func TestManagementImportUsesOnlyFixedCLIPath(t *testing.T) {
	previousHome := userHomeDir
	previousRead := readLocalFile
	t.Cleanup(func() { userHomeDir = previousHome; readLocalFile = previousRead })
	userHomeDir = func() (string, error) { return "/home/test", nil }
	var readPath string
	readLocalFile = func(path string) ([]byte, error) {
		readPath = path
		return mustJSON(t, map[string]string{"COMMANDCODE_API_KEY": "user_import"}), nil
	}
	fixture := []byte("{\"type\":\"finish\",\"finishReason\":\"stop\"}\n")
	script := &hostCallbackScript{streamChunks: [][]byte{fixture}}
	var saved credential
	withHostCall(t, func(method string, request any, result any) error {
		if method == pluginabi.MethodHostAuthList {
			result.(*hostAuthListResponse).Files = nil
			return nil
		}
		if method == pluginabi.MethodHostAuthSave {
			if err := json.Unmarshal(request.(pluginapi.HostAuthSaveRequest).JSON, &saved); err != nil {
				t.Fatal(err)
			}
			return nil
		}
		return script.call(method, request, result)
	})
	raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodPost, Path: managementImportPath, Body: mustJSON(t, map[string]any{"path": "/tmp/evil", "plan": "go", "priority_override": 8})},
		HostCallbackID:    "callback",
	})
	var response pluginapi.ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != http.StatusCreated || readPath != filepath.Join("/home/test", ".commandcode", "auth.json") {
		t.Fatalf("status=%d path=%q body=%s", response.StatusCode, readPath, response.Body)
	}
	if saved.Plan != "go" || saved.PriorityOverride == nil || *saved.PriorityOverride != 8 || saved.Priority != 8 {
		t.Fatalf("saved credential = %#v", saved)
	}
}

func TestManagementMissingImportFileDoesNotSave(t *testing.T) {
	previousRead := readLocalFile
	t.Cleanup(func() { readLocalFile = previousRead })
	readLocalFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
	saves := 0
	withHostCall(t, func(method string, _ any, _ any) error {
		if method == pluginabi.MethodHostAuthSave {
			saves++
		}
		return nil
	})
	raw := mustHandle(t, pluginabi.MethodManagementHandle, managementRPCRequest{ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodPost, Path: managementImportPath}})
	var response pluginapi.ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != http.StatusNotFound || saves != 0 {
		t.Fatalf("status=%d saves=%d body=%s", response.StatusCode, saves, response.Body)
	}
}

func countRecordedCalls(calls []hostCallbackRecord, method string) int {
	count := 0
	for _, call := range calls {
		if call.Method == method {
			count++
		}
	}
	return count
}
