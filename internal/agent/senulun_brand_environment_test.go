package agent

import (
	"slices"
	"testing"
)

func TestSenulunUpgradeEnvironmentPreservesConfiguration(t *testing.T) {
	before := []string{"PULSE_PUBLIC_URL=https://old.example.invalid", "PULSE_DATABASE_PATH=/var/lib/pulse/pulse.db", "PULSE_GITHUB_CLIENT_ID=synthetic-id", "PULSE_TRAFFIC_KEY_FILE=/var/lib/pulse/key", "PULSE_RETENTION_DAYS=23"}
	got, err := senulunUpgradeEnvironment(before, []string{"SENULUN_PUBLIC_URL=https://new.example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SENULUN_PUBLIC_URL=https://new.example.invalid", "SENULUN_DATABASE_PATH=/var/lib/senulun/store/senulun.db", "SENULUN_GITHUB_CLIENT_ID=synthetic-id", "SENULUN_TRAFFIC_KEY_FILE=/var/lib/senulun/key", "SENULUN_RETENTION_DAYS=23"} {
		if !slices.Contains(got, want) {
			t.Fatal("configuration was not retained")
		}
	}
	if _, err := senulunUpgradeEnvironment([]string{"PULSE_DATABASE_PATH=/custom/data.db"}, nil); err == nil {
		t.Fatal("custom data path silently replaced")
	}
}
