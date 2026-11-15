package senulun

import (
	"reflect"
	"testing"
)

func TestMigrateEnvironmentPreservesValuesAndRejectsConflicts(t *testing.T) {
	before := []string{"SENULUN_SERVICE_URL=https://example.invalid/", "SENULUN_NODE_REGION=JP", "SENULUN_GEOIP_PROVIDER=disabled", "SENULUN_CREDENTIALS_PATH=/var/lib/senulun/agent-credentials.json", "SENULUN_INTERVAL_SECONDS=30", "SENULUN_TRAFFIC_KEY_FILE=/keys/unchanged", "LANG=C.UTF-8"}
	want := []string{"SENLUN_SERVICE_URL=https://example.invalid/", "SENLUN_NODE_REGION=JP", "SENLUN_GEOIP_PROVIDER=disabled", "SENLUN_CREDENTIALS_PATH=/var/lib/senlun/agent-credentials.json", "SENLUN_INTERVAL_SECONDS=30", "SENLUN_TRAFFIC_KEY_FILE=/keys/unchanged", "LANG=C.UTF-8"}
	got, err := MigrateEnvironment(before)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("migration differs: %v", err)
	}
	again, err := MigrateEnvironment(got)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatal("migration is not idempotent", err)
	}
	if _, err := MigrateEnvironment([]string{"SENULUN_NODE_REGION=JP", "SENLUN_NODE_REGION=US"}); err == nil {
		t.Fatal("conflicting configuration accepted")
	}
}
