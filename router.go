package main

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type routeModelRPCRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func handleModelRoute(raw []byte) ([]byte, error) {
	var request routeModelRPCRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return errorEnvelope(&rpcError{Code: "invalid_request", Message: "invalid model route request", HTTPStatus: 400}), nil
	}
	model := strings.TrimSpace(request.RequestedModel)
	if model == "" || !isCommandCodeModel(model) {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	return okEnvelope(pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Reason: "commandcode"})
}

func isCommandCodeModel(model string) bool {
	model = strings.TrimSpace(model)
	if index := strings.IndexByte(model, '('); index >= 0 && strings.HasSuffix(model, ")") {
		model = strings.TrimSpace(model[:index])
	}
	for _, candidate := range snapshotModels() {
		if model == candidate.ID {
			return true
		}
	}
	return false
}
