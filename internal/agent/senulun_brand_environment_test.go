package agent

import (
	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/senulun"
	"slices"
	"strings"
	"testing"
)

func TestSenulunUpgradeEnvironmentPreservesConfiguration(t *testing.T) {
	before := []string{"SENULUN_PUBLIC_URL=https://old.example.invalid", "SENULUN_DATABASE_PATH=/var/lib/senulun/pulse.db", "SENULUN_GITHUB_CLIENT_ID=synthetic-id", "SENULUN_TRAFFIC_KEY_FILE=/var/lib/senulun/key", "SENULUN_RETENTION_DAYS=23"}
	before = append(before, "SENULUN_SETUP_TOKEN_FILE=/pulse-bootstrap/setup-token")
	got, err := senulunUpgradeEnvironment(before, []string{"SENLUN_PUBLIC_URL=https://new.example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range got {
		if strings.Contains(entry, "SETUP_TOKEN") {
			t.Fatal("obsolete bootstrap token was retained")
		}
	}
	for _, want := range []string{"SENLUN_PUBLIC_URL=https://new.example.invalid", "SENLUN_DATABASE_PATH=/var/lib/senlun/senlun-store/senlun.db", "SENLUN_GITHUB_CLIENT_ID=synthetic-id", "SENLUN_TRAFFIC_KEY_FILE=/var/lib/senlun/key", "SENLUN_RETENTION_DAYS=23"} {
		if !slices.Contains(got, want) {
			t.Fatal("configuration was not retained")
		}
	}
	if _, err := senulunUpgradeEnvironment([]string{"SENULUN_DATABASE_PATH=/custom/data.db"}, nil); err == nil {
		t.Fatal("custom data path silently replaced")
	}
}

func TestSenulunEnvironmentConversionDoesNotRelaxThirdPartyRetention(t *testing.T) {
	task := DeploymentTask{AppKey: senulun.AgentKey, Operation: "upgrade", Manifest: catalog.AppManifest{Runtime: &catalog.RuntimeSpec{Kind: "systemd", Systemd: &catalog.SystemdRuntime{Executable: "senlun-agent"}}}}
	if applicationOwnedNativeFileForTask(task, "environment") {
		t.Fatal("converted Senulun environment would be overwritten by legacy bytes")
	}
	if !applicationOwnedNativeFileForTask(task, "credential:node") {
		t.Fatal("credential retention was weakened")
	}
	task.AppKey = "example/third-party"
	if !applicationOwnedNativeFileForTask(task, "environment") {
		t.Fatal("third-party environment retention changed")
	}
}
