package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	managementAccountsPath        = "/plugins/commandcode-bridge/accounts"
	managementModelsPath          = managementAccountsPath + "/models"
	managementModelsFetchPath     = managementModelsPath + "/fetch"
	managementImportPath          = "/plugins/commandcode-bridge/import-local"
	managementValidatePath        = "/plugins/commandcode-bridge/validate"
	resourceAccountsPath          = "/accounts"
	managementAccountsFullPath    = "/v0/management" + managementAccountsPath
	managementModelsFullPath      = "/v0/management" + managementModelsPath
	managementModelsFetchFullPath = "/v0/management" + managementModelsFetchPath
	resourceAccountsFullPath      = "/v0/resource/plugins/commandcode-bridge" + resourceAccountsPath
	maxManagementBodyBytes        = 64 * 1024
	accountsPageCSP               = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:"
)

var (
	userHomeDir   = os.UserHomeDir
	readLocalFile = os.ReadFile
)

//go:embed web/accounts.html
var accountsPage []byte

type managementRPCRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

type hostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type routingRequest struct {
	Plan             string `json:"plan,omitempty"`
	PriorityOverride *int   `json:"priority_override"`
}

type managementAccount struct {
	Filename          string            `json:"filename"`
	Fingerprint       string            `json:"fingerprint"`
	Label             string            `json:"label,omitempty"`
	Plan              string            `json:"plan"`
	PriorityOverride  *int              `json:"priority_override"`
	EffectivePriority int               `json:"effective_priority"`
	Status            string            `json:"status,omitempty"`
	Disabled          bool              `json:"disabled,omitempty"`
	Unavailable       bool              `json:"unavailable,omitempty"`
	Models            []credentialModel `json:"models,omitempty"`
	Editable          bool              `json:"editable"`
}

type enrollmentRequest struct {
	APIKey string `json:"api_key"`
	Label  string `json:"label,omitempty"`
	Model  string `json:"model,omitempty"`
	routingRequest
}

type modelsRequest struct {
	Fingerprint string            `json:"fingerprint"`
	Models      []credentialModel `json:"models"`
}

func handleManagementRegister() ([]byte, error) {
	return okEnvelope(managementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: managementAccountsPath, Description: "List redacted CommandCode accounts"},
			{Method: http.MethodPost, Path: managementAccountsPath, Description: "Validate and enroll a CommandCode API key"},
			{Method: http.MethodPut, Path: managementModelsPath, Description: "Save an account model set"},
			{Method: http.MethodPost, Path: managementModelsFetchPath, Description: "Fetch an account model catalog"},
			{Method: http.MethodPost, Path: managementImportPath, Description: "Import and validate the local CommandCode CLI credential"},
			{Method: http.MethodPost, Path: managementValidatePath, Description: "Validate a CommandCode API key without saving it"},
		},
		Resources: []pluginapi.ResourceRoute{{Path: "/accounts", Menu: "CommandCode Bridge Accounts", Description: "Enroll and inspect CommandCode accounts"}},
	})
}

func handleManagement(raw []byte) ([]byte, error) {
	var request managementRPCRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return errorEnvelope(&rpcError{Code: "invalid_request", Message: "invalid management request", HTTPStatus: 400}), nil
	}
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	path := strings.TrimRight(strings.TrimSpace(request.Path), "/")
	if len(request.Body) > maxManagementBodyBytes {
		return okEnvelope(managementJSON(http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"}))
	}
	var response pluginapi.ManagementResponse
	switch {
	case method == http.MethodGet && (path == resourceAccountsPath || path == resourceAccountsFullPath):
		response = accountsPageResponse()
	case method == http.MethodGet && (path == managementAccountsPath || path == managementAccountsFullPath):
		response = listManagementAccounts(request.HostCallbackID)
	case method == http.MethodPost && (path == managementAccountsPath || path == managementAccountsFullPath):
		response = enrollManagementAccount(request.HostCallbackID, request.Body, true)
	case method == http.MethodPut && (path == managementModelsPath || path == managementModelsFullPath):
		response = handleModelsSave(request.HostCallbackID, request.Body)
	case method == http.MethodPost && (path == managementModelsFetchPath || path == managementModelsFetchFullPath):
		response = handleModelsFetch(request.HostCallbackID, request.Body)
	case method == http.MethodPost && (path == managementValidatePath || path == "/v0/management"+managementValidatePath):
		response = enrollManagementAccount(request.HostCallbackID, request.Body, false)
	case method == http.MethodPost && (path == managementImportPath || path == "/v0/management"+managementImportPath):
		response = importManagementAccount(request.HostCallbackID, request.Body)
	default:
		response = managementJSON(http.StatusNotFound, map[string]string{"error": "route not found"})
	}
	return okEnvelope(response)
}

func accountsPageResponse() pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":            []string{"text/html; charset=utf-8"},
			"Content-Security-Policy": []string{accountsPageCSP},
			"Referrer-Policy":         []string{"no-referrer"},
		},
		Body: append([]byte(nil), accountsPage...),
	}
}

func listManagementAccounts(hostCallbackID string) pluginapi.ManagementResponse {
	files, err := listHostAccounts(hostCallbackID)
	if err != nil {
		return managementJSON(http.StatusBadGateway, map[string]string{"error": "unable to list accounts"})
	}
	accounts := make([]managementAccount, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if !isCommandCodeBridgeAccount(file) {
			continue
		}
		key := strings.TrimSpace(file.AuthIndex)
		if key == "" {
			// Fall back to filename when the host omits the runtime index.
			key = filepath.Base(strings.TrimSpace(file.Name))
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		filename := filepath.Base(strings.TrimSpace(file.Name))
		account := managementAccount{
			Filename:          filename,
			Fingerprint:       fingerprintFromFilename(file.Name),
			Label:             file.Label,
			Plan:              planUnspecified,
			EffectivePriority: file.Priority,
			Status:            file.Status,
			Disabled:          file.Disabled,
			Unavailable:       file.Unavailable,
		}
		if authIndex := strings.TrimSpace(file.AuthIndex); authIndex != "" && !file.RuntimeOnly {
			physical, err := getHostAccount(authIndex)
			if err == nil && strings.TrimSpace(physical.Name) != "" && filepath.Base(strings.TrimSpace(physical.Name)) == filename {
				if value, err := normalizeCredential(physical.JSON); err == nil {
					account.Label = value.Label
					account.Plan = value.Plan
					account.PriorityOverride = value.PriorityOverride
					account.EffectivePriority = value.Priority
					account.Models = value.Models
					account.Editable = true
				}
			}
		}
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Filename < accounts[j].Filename })
	return managementJSON(http.StatusOK, map[string]any{"accounts": accounts})
}

func enrollManagementAccount(hostCallbackID string, raw []byte, save bool) pluginapi.ManagementResponse {
	var request enrollmentRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return managementJSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
	}
	return validateAndMaybeSave(hostCallbackID, request, save)
}

func importManagementAccount(hostCallbackID string, requestBody []byte) pluginapi.ManagementResponse {
	var routing routingRequest
	if len(requestBody) > 0 && json.Unmarshal(requestBody, &routing) != nil {
		return managementJSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
	}
	home, err := userHomeDir()
	if err != nil {
		return managementJSON(http.StatusInternalServerError, map[string]string{"error": "unable to resolve home directory"})
	}
	raw, err := readLocalFile(filepath.Join(home, ".commandcode", "auth.json"))
	if errors.Is(err, os.ErrNotExist) {
		return managementJSON(http.StatusNotFound, map[string]string{"error": "local CommandCode credential not found"})
	}
	if err != nil {
		return managementJSON(http.StatusBadRequest, map[string]string{"error": "unable to read local CommandCode credential"})
	}
	var local map[string]json.RawMessage
	if json.Unmarshal(raw, &local) != nil {
		return managementJSON(http.StatusBadRequest, map[string]string{"error": "invalid local CommandCode credential"})
	}
	var apiKey string
	for _, key := range []string{"COMMANDCODE_API_KEY", "api_key", "apiKey"} {
		if value := local[key]; len(value) > 0 && json.Unmarshal(value, &apiKey) == nil && strings.TrimSpace(apiKey) != "" {
			break
		}
	}
	return validateAndMaybeSave(hostCallbackID, enrollmentRequest{APIKey: apiKey, Label: "Imported from CommandCode CLI", routingRequest: routing}, true)
}

func credentialForEnrollment(request enrollmentRequest) (credential, error) {
	raw, _ := json.Marshal(map[string]any{
		"type": pluginID, "api_key": request.APIKey, "label": request.Label,
		"plan": request.Plan, "priority_override": request.PriorityOverride,
	})
	return normalizeCredential(raw)
}

func validateAndMaybeSave(hostCallbackID string, request enrollmentRequest, save bool) pluginapi.ManagementResponse {
	value, err := credentialForEnrollment(request)
	if err != nil {
		return managementJSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	apiKey := value.APIKey
	name := pluginID + "-" + fingerprint(apiKey) + ".json"
	files, err := listHostAccounts(hostCallbackID)
	if err != nil {
		return managementJSON(http.StatusBadGateway, map[string]string{"error": "unable to check existing accounts"})
	}
	for _, file := range files {
		if strings.EqualFold(filepath.Base(file.Name), name) {
			return managementJSON(http.StatusConflict, map[string]string{"error": "account already enrolled", "filename": name})
		}
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		models := snapshotModels()
		if len(models) == 0 {
			return managementJSON(http.StatusServiceUnavailable, map[string]string{"error": "no validation model is available"})
		}
		model = models[0].ID
	}
	if validationErr := validateCredentialLive(hostCallbackID, apiKey, model); validationErr != nil {
		status := validationErr.HTTPStatus
		if status < 400 {
			status = http.StatusBadGateway
		}
		return managementJSON(status, map[string]any{"error": validationErr.Message, "retryable": validationErr.Retryable})
	}
	account := managementAccount{
		Filename:          name,
		Fingerprint:       fingerprint(apiKey),
		Label:             value.Label,
		Plan:              value.Plan,
		PriorityOverride:  value.PriorityOverride,
		EffectivePriority: value.Priority,
		Status:            "validated",
	}
	if !save {
		return managementJSON(http.StatusOK, map[string]any{"valid": true, "account": account})
	}
	storage, _ := json.Marshal(value)
	var saved pluginapi.HostAuthSaveResponse
	if err := hostCall(pluginabi.MethodHostAuthSave, pluginapi.HostAuthSaveRequest{Name: name, JSON: storage}, &saved); err != nil {
		return managementJSON(http.StatusBadGateway, map[string]string{"error": "unable to save account"})
	}
	return managementJSON(http.StatusCreated, map[string]any{"account": account})
}

func validateCredentialLive(hostCallbackID, apiKey, model string) *rpcError {
	requestBody, _, rpcErr := translateOpenAIRequest([]byte(`{"messages":[{"role":"user","content":"ping"}],"max_tokens":1}`), model, time.Now(), newUUID())
	if rpcErr != nil {
		return rpcErr
	}
	stream, _, rpcErr := openUpstreamStream(executorRPCRequest{HostCallbackID: hostCallbackID}, apiKey, requestBody)
	if rpcErr != nil {
		return rpcErr
	}
	defer stream.Close()
	state := newResponseState("validation", time.Now().Unix(), model, false)
	for {
		chunk, err := stream.Read()
		if err != nil {
			return &rpcError{Code: "upstream_error", Message: "credential validation stream failed", Retryable: true, HTTPStatus: 502}
		}
		if chunk.Done {
			break
		}
		if _, protocolErr := state.Feed(chunk.Payload); protocolErr != nil {
			return protocolErr
		}
	}
	if _, protocolErr := state.Finish(); protocolErr != nil {
		return protocolErr
	}
	return nil
}

func getHostAccount(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	var response pluginapi.HostAuthGetResponse
	err := hostCall(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex}, &response)
	return response, err
}

func handleModelsSave(hostCallbackID string, body []byte) pluginapi.ManagementResponse {
	var request modelsRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return managementJSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
	}
	file, physical, value, status := resolveManagementAccount(hostCallbackID, request.Fingerprint)
	if status != nil {
		return *status
	}
	value.Models = request.Models
	raw, _ := json.Marshal(value)
	if _, err := normalizeCredential(raw); err != nil {
		return managementJSON(http.StatusBadRequest, map[string]string{"error": errInvalidModelSet.Error()})
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(physical.JSON, &source); err != nil {
		return managementJSON(http.StatusBadGateway, map[string]string{"error": "unable to read account"})
	}
	models, _ := json.Marshal(request.Models)
	source["models"] = models
	storage, err := json.Marshal(source)
	if err != nil {
		return managementJSON(http.StatusInternalServerError, map[string]string{"error": "unable to save account"})
	}
	var saved pluginapi.HostAuthSaveResponse
	if err := hostCall(pluginabi.MethodHostAuthSave, pluginapi.HostAuthSaveRequest{Name: filepath.Base(file.Name), JSON: storage}, &saved); err != nil {
		return managementJSON(http.StatusBadGateway, map[string]string{"error": "unable to save account"})
	}
	return managementJSON(http.StatusOK, map[string]any{"models": request.Models})
}

func handleModelsFetch(hostCallbackID string, body []byte) pluginapi.ManagementResponse {
	var request modelsRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return managementJSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
	}
	_, _, value, status := resolveManagementAccount(hostCallbackID, request.Fingerprint)
	if status != nil {
		return *status
	}
	var response pluginapi.HTTPResponse
	if err := hostCall(pluginabi.MethodHostHTTPDo, hostHTTPRequest{
		HostCallbackID: hostCallbackID,
		Method:         http.MethodGet,
		URL:            modelCatalogURL,
		Headers: http.Header{
			"Accept":        []string{"application/json"},
			"Authorization": []string{"Bearer " + value.APIKey},
		},
	}, &response); err != nil || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return managementJSON(http.StatusBadGateway, map[string]string{"error": "unable to fetch model catalog"})
	}
	var catalog catalogResponse
	if err := json.Unmarshal(response.Body, &catalog); err != nil {
		return managementJSON(http.StatusBadGateway, map[string]string{"error": "unable to fetch model catalog"})
	}
	models := make([]map[string]string, 0, len(catalog.Data))
	for _, model := range catalog.Data {
		if id := strings.TrimSpace(model.ID); id != "" {
			models = append(models, map[string]string{"id": id, "name": strings.TrimSpace(model.Name)})
		}
	}
	return managementJSON(http.StatusOK, map[string]any{"models": models})
}

func resolveManagementAccount(hostCallbackID, accountFingerprint string) (pluginapi.HostAuthFileEntry, pluginapi.HostAuthGetResponse, credential, *pluginapi.ManagementResponse) {
	accountFingerprint = strings.TrimSpace(accountFingerprint)
	if accountFingerprint == "" {
		response := managementJSON(http.StatusBadRequest, map[string]string{"error": "account fingerprint is required"})
		return pluginapi.HostAuthFileEntry{}, pluginapi.HostAuthGetResponse{}, credential{}, &response
	}
	files, err := listHostAccounts(hostCallbackID)
	if err != nil {
		response := managementJSON(http.StatusBadGateway, map[string]string{"error": "unable to list accounts"})
		return pluginapi.HostAuthFileEntry{}, pluginapi.HostAuthGetResponse{}, credential{}, &response
	}
	for _, file := range files {
		if !isCommandCodeBridgeAccount(file) || file.RuntimeOnly || fingerprintFromFilename(file.Name) != accountFingerprint {
			continue
		}
		authIndex := strings.TrimSpace(file.AuthIndex)
		if authIndex == "" {
			continue
		}
		physical, err := getHostAccount(authIndex)
		if err != nil || filepath.Base(strings.TrimSpace(physical.Name)) != filepath.Base(strings.TrimSpace(file.Name)) {
			break
		}
		value, err := normalizeCredential(physical.JSON)
		if err != nil {
			break
		}
		return file, physical, value, nil
	}
	response := managementJSON(http.StatusNotFound, map[string]string{"error": "account not found"})
	return pluginapi.HostAuthFileEntry{}, pluginapi.HostAuthGetResponse{}, credential{}, &response
}

func listHostAccounts(hostCallbackID string) ([]pluginapi.HostAuthFileEntry, error) {
	var response hostAuthListResponse
	request := map[string]string{}
	if hostCallbackID != "" {
		request["host_callback_id"] = hostCallbackID
	}
	if err := hostCall(pluginabi.MethodHostAuthList, request, &response); err != nil {
		return nil, err
	}
	return response.Files, nil
}

func isCommandCodeBridgeAccount(file pluginapi.HostAuthFileEntry) bool {
	return strings.EqualFold(strings.TrimSpace(file.Provider), pluginID) || strings.EqualFold(strings.TrimSpace(file.Type), pluginID) || strings.HasPrefix(strings.ToLower(filepath.Base(file.Name)), pluginID+"-")
}

func fingerprintFromFilename(name string) string {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(strings.TrimSpace(name))), ".json")
	return strings.TrimPrefix(base, pluginID+"-")
}

func managementJSON(status int, payload any) pluginapi.ManagementResponse {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{"error":"unable to encode response"}`)
		status = http.StatusInternalServerError
	}
	return pluginapi.ManagementResponse{StatusCode: status, Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, Body: body}
}
