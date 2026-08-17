package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestABIRegistrationEnvelopeUsesCurrentContract(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodPluginRegister, mustJSON(t, lifecycleRequest{SchemaVersion: pluginabi.SchemaVersion}))
	if err != nil {
		t.Fatalf("handleMethod() error = %v", err)
	}
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		t.Fatalf("response = %s, err=%v", raw, err)
	}
	var registration registrationResponse
	if err := json.Unmarshal(envelope.Result, &registration); err != nil {
		t.Fatalf("registration decode: %v", err)
	}
	if registration.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("schema = %d, want %d", registration.SchemaVersion, pluginabi.SchemaVersion)
	}
}
