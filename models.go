package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const modelCatalogURL = "https://api.commandcode.ai/provider/v1/models"

//go:embed internal/modelsnapshot/snapshot.json
var modelSnapshotJSON []byte

type catalogResponse struct {
	Object string         `json:"object"`
	Data   []catalogModel `json:"data"`
}

type catalogModel struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Created       int64  `json:"created"`
	OwnedBy       string `json:"owned_by"`
	Name          string `json:"name"`
	ContextLength int64  `json:"context_length"`
}

type authModelRPCRequest struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostHTTPRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method,omitempty"`
	URL            string      `json:"url,omitempty"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

var hostCall = callHost

func fetchModelCatalog(hostCallbackID, apiKey string) ([]catalogModel, error) {
	var response pluginapi.HTTPResponse
	if err := hostCall(pluginabi.MethodHostHTTPDo, hostHTTPRequest{
		HostCallbackID: hostCallbackID,
		Method:         http.MethodGet,
		URL:            modelCatalogURL,
		Headers: http.Header{
			"Accept":        []string{"application/json"},
			"Authorization": []string{"Bearer " + apiKey},
		},
	}, &response); err != nil || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("unable to fetch model catalog")
	}
	var catalog catalogResponse
	if err := json.Unmarshal(response.Body, &catalog); err != nil {
		return nil, errors.New("invalid model catalog")
	}
	for _, model := range catalog.Data {
		if strings.TrimSpace(model.ID) != "" {
			return catalog.Data, nil
		}
	}
	return nil, errors.New("empty model catalog")
}

func parseModelCatalog(raw []byte) ([]pluginapi.ModelInfo, error) {
	var catalog catalogResponse
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, errors.New("invalid model catalog")
	}
	seen := make(map[string]struct{}, len(catalog.Data))
	models := make([]pluginapi.ModelInfo, 0, len(catalog.Data))
	for _, item := range catalog.Data {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			continue
		}
		if _, exists := seen[item.ID]; exists {
			continue
		}
		seen[item.ID] = struct{}{}
		models = append(models, pluginapi.ModelInfo{
			ID:                         item.ID,
			Object:                     strings.TrimSpace(item.Object),
			Created:                    item.Created,
			OwnedBy:                    strings.TrimSpace(item.OwnedBy),
			DisplayName:                strings.TrimSpace(item.Name),
			Name:                       strings.TrimSpace(item.Name),
			ContextLength:              item.ContextLength,
			SupportedGenerationMethods: []string{"chat"},
			SupportedInputModalities:   []string{"text"},
			SupportedOutputModalities:  []string{"text"},
		})
	}
	if len(models) == 0 {
		return nil, errors.New("empty model catalog")
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func snapshotModels() []pluginapi.ModelInfo {
	models, err := parseSnapshotModels(modelSnapshotJSON)
	if err != nil {
		return nil
	}
	return models
}

func parseSnapshotModels(raw []byte) ([]pluginapi.ModelInfo, error) {
	var items []catalogModel
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	catalog, err := json.Marshal(catalogResponse{Object: "list", Data: items})
	if err != nil {
		return nil, err
	}
	return parseModelCatalog(catalog)
}

func handleModelStatic() ([]byte, error) {
	return okEnvelope(pluginapi.ModelResponse{Provider: pluginID, Models: []pluginapi.ModelInfo{}})
}

func handleModelForAuth(raw []byte) ([]byte, error) {
	var request authModelRPCRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return errorEnvelope(&rpcError{Code: "invalid_request", Message: "invalid model discovery request", HTTPStatus: http.StatusBadRequest}), nil
	}
	value, err := normalizeCredential(request.StorageJSON)
	if err != nil {
		return errorEnvelope(&rpcError{Code: "invalid_credentials", Message: errInvalidCredential.Error(), HTTPStatus: http.StatusUnauthorized}), nil
	}
	models := make([]pluginapi.ModelInfo, 0, len(value.Models))
	if len(value.Models) == 0 {
		return okEnvelope(pluginapi.ModelResponse{Provider: pluginID, Models: models})
	}
	var contextLengths map[string]int64
	if catalog, err := fetchModelCatalog(request.HostCallbackID, value.APIKey); err == nil && len(catalog) > 0 {
		contextLengths = make(map[string]int64, len(catalog))
		for _, model := range catalog {
			if id := strings.TrimSpace(model.ID); id != "" {
				contextLengths[id] = model.ContextLength
			}
		}
	}
	for _, model := range value.Models {
		displayName := model.Alias
		if displayName == "" {
			displayName = model.Name
		}
		models = append(models, pluginapi.ModelInfo{
			ID:                         model.Name,
			Name:                       model.Name,
			DisplayName:                displayName,
			ContextLength:              contextLengths[model.Name],
			SupportedGenerationMethods: []string{"chat"},
			SupportedInputModalities:   []string{"text"},
			SupportedOutputModalities:  []string{"text"},
		})
	}
	return okEnvelope(pluginapi.ModelResponse{Provider: pluginID, Models: models})
}
