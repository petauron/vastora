package agent

import (
	"encoding/json"
	"testing"
)

func TestInspectXrayConfigurationReportsOnlyStructuralDifferences(t *testing.T) {
	state := testXrayWorkerState()
	active, err := renderXrayWorkerConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	matching, err := inspectXrayConfiguration(state, active)
	if err != nil || !matching.Matches || !matching.RuntimeImportable || len(matching.Differences) != 0 {
		t.Fatalf("matching inspection=%#v err=%v", matching, err)
	}
	var config map[string]any
	if json.Unmarshal(active, &config) != nil {
		t.Fatal("invalid fixture")
	}
	inbounds := config["inbounds"].([]any)
	inbound := inbounds[1].(map[string]any)
	settings := inbound["settings"].(map[string]any)
	settings["clients"] = []any{}
	changed, _ := json.Marshal(config)
	inspection, err := inspectXrayConfiguration(state, changed)
	if err != nil || inspection.Matches || len(inspection.Differences) != 1 || inspection.Differences[0].Field != "clients" {
		t.Fatalf("changed inspection=%#v err=%v", inspection, err)
	}
}

func TestXrayWorkerStateFromRuntimeRoundTripsTheEffectiveConfiguration(t *testing.T) {
	state := testXrayWorkerState()
	active, err := renderXrayWorkerConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := xrayWorkerStateFromRuntime(state, active)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := renderXrayWorkerConfig(recovered)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := canonicalXrayConfigSHA256(active)
	after, _ := canonicalXrayConfigSHA256(rendered)
	if before != after || recovered.NextInboundID != len(recovered.Inbounds)+1 {
		t.Fatalf("runtime round trip changed configuration: before=%s after=%s state=%#v", before, after, recovered)
	}
}
