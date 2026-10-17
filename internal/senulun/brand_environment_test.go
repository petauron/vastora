package senulun

import (
	"reflect"
	"testing"
)

func TestMigrateEnvironmentPreservesValuesAndRejectsConflicts(t *testing.T) {
	before := []string{"PULSE_SERVICE_URL=https://example.invalid/", "PULSE_NODE_REGION=JP", "PULSE_GEOIP_PROVIDER=disabled", "PULSE_CREDENTIALS_PATH=/var/lib/pulse/agent-credentials.json", "PULSE_INTERVAL_SECONDS=30", "PULSE_TRAFFIC_KEY_FILE=/keys/unchanged", "LANG=C.UTF-8"}
	want := []string{"SENULUN_SERVICE_URL=https://example.invalid/", "SENULUN_NODE_REGION=JP", "SENULUN_GEOIP_PROVIDER=disabled", "SENULUN_CREDENTIALS_PATH=/var/lib/senulun/agent-credentials.json", "SENULUN_INTERVAL_SECONDS=30", "SENULUN_TRAFFIC_KEY_FILE=/keys/unchanged", "LANG=C.UTF-8"}
	got, err := MigrateEnvironment(before)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("migration differs: %v", err)
	}
	again, err := MigrateEnvironment(got)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatal("migration is not idempotent", err)
	}
	if _, err := MigrateEnvironment([]string{"PULSE_NODE_REGION=JP", "SENULUN_NODE_REGION=US"}); err == nil {
		t.Fatal("conflicting configuration accepted")
	}
}
