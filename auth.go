package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginID            = "commandcode-bridge"
	planUnspecified     = "unspecified"
	minPriorityOverride = 1
	maxPriorityOverride = 10
)

var (
	errInvalidCredential   = errors.New("CommandCode API key must start with user_")
	errInvalidPlan         = errors.New("unsupported CommandCode plan")
	errInvalidPriority     = errors.New("CommandCode priority override must be an integer from 1 to 10")
	errInvalidModelSet     = errors.New("invalid model set")
	errUnrelatedCredential = errors.New("credential belongs to another provider")
	modelNamePattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	modelAliasPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$`)
	planPriorities         = map[string]int{
		"go": 7, "goat": 6, "pro": 5, "team": 4,
		"max-10x": 3, "max-20x": 2, "provider": 1,
		planUnspecified: 0,
	}
)

type credentialModel struct {
	Name  string `json:"name"`
	Alias string `json:"alias,omitempty"`
}

type credential struct {
	Type             string            `json:"type"`
	APIKey           string            `json:"api_key"`
	Label            string            `json:"label,omitempty"`
	Plan             string            `json:"plan"`
	PriorityOverride *int              `json:"priority_override,omitempty"`
	Priority         int               `json:"priority"`
	Models           []credentialModel `json:"models,omitempty"`
}

func normalizePlan(raw string) (string, int, error) {
	plan := strings.ToLower(strings.TrimSpace(raw))
	if plan == "" {
		plan = planUnspecified
	}
	priority, ok := planPriorities[plan]
	if !ok {
		return "", 0, errInvalidPlan
	}
	return plan, priority, nil
}

func optionalInteger(source map[string]json.RawMessage, name string, min, max int) (*int, error) {
	raw, present := source[name]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < min || value > max {
		return nil, errInvalidPriority
	}
	return &value, nil
}

func applyCredentialRouting(value credential, plan string, override *int) (credential, error) {
	normalized, preset, err := normalizePlan(plan)
	if err != nil {
		return credential{}, err
	}
	if override != nil && (*override < minPriorityOverride || *override > maxPriorityOverride) {
		return credential{}, errInvalidPriority
	}
	value.Plan = normalized
	value.PriorityOverride = override
	value.Priority = preset
	if override != nil {
		value.Priority = *override
	}
	return value, nil

}
func normalizeCredential(raw []byte) (credential, error) {
	var source map[string]json.RawMessage
	if err := json.Unmarshal(raw, &source); err != nil {
		return credential{}, errInvalidCredential
	}
	var typeName string
	if value, ok := source["type"]; ok {
		_ = json.Unmarshal(value, &typeName)
	}
	typeName = strings.ToLower(strings.TrimSpace(typeName))
	if typeName != "" && typeName != pluginID {
		return credential{}, errUnrelatedCredential
	}
	var apiKey string
	for _, name := range []string{"api_key", "apiKey", "COMMAND_CODE_API_KEY", "COMMANDCODE_API_KEY"} {
		if value, ok := source[name]; ok {
			_ = json.Unmarshal(value, &apiKey)
			apiKey = strings.TrimSpace(apiKey)
			if apiKey != "" {
				break
			}
		}
	}
	if !strings.HasPrefix(apiKey, "user_") || len(apiKey) <= len("user_") {
		return credential{}, errInvalidCredential
	}
	var label string
	if value, ok := source["label"]; ok {
		_ = json.Unmarshal(value, &label)
	}
	var plan string
	if rawPlan, present := source["plan"]; present && !bytes.Equal(bytes.TrimSpace(rawPlan), []byte("null")) {
		if err := json.Unmarshal(rawPlan, &plan); err != nil {
			return credential{}, errInvalidPlan
		}
	}
	override, err := optionalInteger(source, "priority_override", minPriorityOverride, maxPriorityOverride)
	if err != nil {
		return credential{}, err
	}
	if _, planPresent := source["plan"]; !planPresent && override == nil {
		legacy, legacyErr := optionalInteger(source, "priority", 0, maxPriorityOverride)
		if legacyErr != nil {
			return credential{}, legacyErr
		}
		if legacy != nil && *legacy > 0 {
			override = legacy
		}
	}
	value, err := applyCredentialRouting(credential{
		Type: pluginID, APIKey: apiKey, Label: strings.TrimSpace(label),
	}, plan, override)
	if err != nil {
		return credential{}, err
	}
	if rawModels, present := source["models"]; present {
		if bytes.Equal(bytes.TrimSpace(rawModels), []byte("null")) {
			return credential{}, errInvalidModelSet
		}
		if err := json.Unmarshal(rawModels, &value.Models); err != nil {
			return credential{}, errInvalidModelSet
		}
		seenNames := make(map[string]struct{}, len(value.Models))
		seenAliases := make(map[string]struct{}, len(value.Models))
		for index := range value.Models {
			model := &value.Models[index]
			model.Name = strings.TrimSpace(model.Name)
			model.Alias = strings.TrimSpace(model.Alias)
			if !modelNamePattern.MatchString(model.Name) {
				return credential{}, errInvalidModelSet
			}
			if _, duplicate := seenNames[model.Name]; duplicate {
				return credential{}, errInvalidModelSet
			}
			seenNames[model.Name] = struct{}{}
			if model.Alias == "" {
				continue
			}
			if !modelAliasPattern.MatchString(model.Alias) {
				return credential{}, errInvalidModelSet
			}
			if _, duplicate := seenAliases[model.Alias]; duplicate {
				return credential{}, errInvalidModelSet
			}
			seenAliases[model.Alias] = struct{}{}
		}
	}
	return value, nil
}

func fingerprint(apiKey string) string {
	digest := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(digest[:])[:12]
}

func credentialAuthData(value credential) pluginapi.AuthData {
	fingerprint := fingerprint(value.APIKey)
	storage, _ := json.Marshal(value)
	metadata := map[string]any{"type": pluginID, "plan": value.Plan, "priority": value.Priority}
	if value.PriorityOverride != nil {
		metadata["priority_override"] = *value.PriorityOverride
	}
	return pluginapi.AuthData{
		Provider:    pluginID,
		ID:          pluginID + "-" + fingerprint,
		FileName:    pluginID + "-" + fingerprint + ".json",
		Label:       value.Label,
		StorageJSON: storage,
		Metadata:    metadata,
		Attributes:  map[string]string{"priority": strconv.Itoa(value.Priority)},
	}
}

func handleAuthParse(raw []byte) ([]byte, error) {
	var request pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return errorEnvelope(&rpcError{Code: "invalid_request", Message: "invalid auth parse request", HTTPStatus: 400}), nil
	}
	value, err := normalizeCredential(request.RawJSON)
	if errors.Is(err, errUnrelatedCredential) {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	if errors.Is(err, errInvalidPlan) || errors.Is(err, errInvalidPriority) || errors.Is(err, errInvalidModelSet) {
		return errorEnvelope(&rpcError{Code: "invalid_routing", Message: err.Error(), HTTPStatus: 400}), nil
	}
	if err != nil {
		return errorEnvelope(&rpcError{Code: "invalid_credentials", Message: errInvalidCredential.Error(), HTTPStatus: 401}), nil
	}
	return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: credentialAuthData(value)})
}

func handleAuthRefresh(raw []byte) ([]byte, error) {
	var request pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return errorEnvelope(&rpcError{Code: "invalid_request", Message: "invalid auth refresh request", HTTPStatus: 400}), nil
	}
	value, err := normalizeCredential(request.StorageJSON)
	if errors.Is(err, errInvalidPlan) || errors.Is(err, errInvalidPriority) || errors.Is(err, errInvalidModelSet) {
		return errorEnvelope(&rpcError{Code: "invalid_routing", Message: err.Error(), HTTPStatus: 400}), nil
	}
	if err != nil {
		return errorEnvelope(&rpcError{Code: "invalid_credentials", Message: errInvalidCredential.Error(), HTTPStatus: 401}), nil
	}
	return okEnvelope(pluginapi.AuthRefreshResponse{Auth: credentialAuthData(value)})
}
