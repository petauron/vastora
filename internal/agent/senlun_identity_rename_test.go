package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/petauron/catalog/catalog"
)

func TestSenlunNativeIdentityRenameRetainsOwnedStateWithoutBackup(t *testing.T) {
	var original catalog.AppManifest
	for _, app := range officialContractFixture(t).Apps {
		if app.ID == "pulse-agent" {
			original = app
		}
	}
	if original.Runtime == nil || original.Runtime.Systemd == nil {
		t.Fatal("missing agent fixture")
	}
	original.Runtime.Systemd.Executable = "senlun-agent"
	for i := range original.Runtime.Systemd.Files {
		if original.Runtime.Systemd.Files[i].Path == "pulse-agent" {
			original.Runtime.Systemd.Files[i].Path = "senlun-agent"
		}
	}
	encoded, _ := json.Marshal(original)
	var target catalog.AppManifest
	if err := json.Unmarshal(encoded, &target); err != nil {
		t.Fatal(err)
	}
	target.ID, target.PackageRevision = "senlun-agent", original.PackageRevision+1
	target.Runtime.Systemd.Artifact = "senlun-agent"
	for i := range target.Artifacts {
		target.Artifacts[i].Name = "senlun-agent"
	}
	canonical, err := catalog.CanonicalAppManifest(original)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(canonical)
	manifestSHA := sha256.Sum256(encoded)
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	installation := AppliedInstallation{InstanceID: "original-task", ApplicationID: "same-instance", AppKey: "vastora-official/pulse-agent", Version: original.Version, Manifest: canonical, Config: json.RawMessage(`{"node":"same-node"}`), Secrets: json.RawMessage(`{"token":"fixture-private-value"}`)}
	if _, err := store.RecordApplied(ctx, installation); err != nil {
		t.Fatal(err)
	}
	receipt := &InstanceResources{Version: 1, ApplicationID: installation.ApplicationID, AppKey: installation.AppKey, Runtime: "systemd", State: "ready", TaskID: installation.InstanceID, PackageVersion: original.Version, PackageRevision: original.PackageRevision, ManifestSHA256: hex.EncodeToString(manifestSHA[:]), Resources: []RuntimeResource{
		{Kind: "unit", LogicalName: "service", Name: "vastora-senlun-agent.service"},
		{Kind: "file", LogicalName: "artifact:senlun-agent", Name: "senlun-agent"},
		{Kind: "directory", LogicalName: "state", Name: "same-node-state", Persistent: true},
	}}
	packages := PackageExecutor{StateDirectory: store.dataDir}
	if err := packages.save(receipt); err != nil {
		t.Fatal(err)
	}
	rawReceipt, err := os.ReadFile(packages.receiptPath(installation.ApplicationID))
	if err != nil {
		t.Fatal(err)
	}
	receiptSHA := sha256.Sum256(rawReceipt)
	backend := &fakePackageBackend{}
	executor := ApplicationExecutor{Store: store, PackageBackend: backend}
	if _, err := executor.renameSenlunNativeIdentity(ctx, installation.ApplicationID, "wrong-digest", target); err == nil {
		t.Fatal("accepted changed source receipt")
	}
	if len(backend.calls) != 0 {
		t.Fatal("invalid identity reached resource inspection")
	}
	result, err := executor.renameSenlunNativeIdentity(ctx, installation.ApplicationID, hex.EncodeToString(receiptSHA[:]), target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backend.calls, []string{"inspect"}) || result.AppKey != "vastora-official/senlun-agent" || !reflect.DeepEqual(result.Resources, receipt.Resources) {
		t.Fatal("identity change modified the running resources")
	}
	updated, err := store.AppliedInstallation(ctx, result.AppKey)
	if err != nil || updated.ApplicationID != installation.ApplicationID || updated.InstanceID != installation.InstanceID || string(updated.Config) != string(installation.Config) || string(updated.Secrets) != string(installation.Secrets) {
		t.Fatal("identity change lost the original application or credentials")
	}
	if _, err := store.AppliedInstallation(ctx, installation.AppKey); !errors.Is(err, errApplicationNotInstalled) {
		t.Fatal("old applied identity remains active")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(packages.receiptPath(installation.ApplicationID)), "backups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("identity change created a backup")
	}
	if _, err := packages.ReadResources(installation.ApplicationID); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(packages.receiptPath(installation.ApplicationID)), "senlun-identity.pending")
	if err := os.WriteFile(marker, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := packages.ReadResources(installation.ApplicationID); err == nil {
		t.Fatal("interrupted migration did not block further package actions")
	}
}
