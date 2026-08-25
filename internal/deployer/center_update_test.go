package deployer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileCenterUpdaterQueuesOneVerifiedRelease(t *testing.T) {
	installDir := t.TempDir()
	for _, name := range []string{".update-service-enabled", "update-center.sh"} {
		if err := os.WriteFile(filepath.Join(installDir, name), []byte("ready\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(installDir, "release.env"), []byte("VASTORA_VERSION=0.1.0-alpha.47\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	updater := FileCenterUpdater{InstallDir: installDir}
	initial, err := updater.CenterUpdateStatus(context.Background())
	if err != nil || !initial.Available || initial.State != "idle" {
		t.Fatalf("unexpected initial status: %#v err=%v", initial, err)
	}
	if _, err := updater.StartCenterUpdate(context.Background(), "0.1.0-alpha.47"); err == nil {
		t.Fatal("same-version update was accepted")
	}
	queued, err := updater.StartCenterUpdate(context.Background(), "0.1.0-alpha.48")
	if err != nil || queued.State != "queued" || queued.TargetVersion != "0.1.0-alpha.48" {
		t.Fatalf("unexpected queued status: %#v err=%v", queued, err)
	}
	request, err := os.ReadFile(filepath.Join(installDir, ".update-request"))
	if err != nil || string(request) != "0.1.0-alpha.48\n" {
		t.Fatalf("unexpected update request %q err=%v", request, err)
	}
	persisted, err := updater.CenterUpdateStatus(context.Background())
	if err != nil || persisted.State != "queued" || persisted.TargetVersion != queued.TargetVersion {
		t.Fatalf("unexpected persisted status: %#v err=%v", persisted, err)
	}
	if _, err := updater.StartCenterUpdate(context.Background(), "not-a-version"); err == nil {
		t.Fatal("invalid release version was accepted")
	}
}

func TestFileCenterUpdaterRequiresInstalledHostService(t *testing.T) {
	updater := FileCenterUpdater{InstallDir: t.TempDir()}
	status, err := updater.CenterUpdateStatus(context.Background())
	if err != nil || status.Available || status.State != "idle" {
		t.Fatalf("unexpected unavailable status: %#v err=%v", status, err)
	}
	if _, err := updater.StartCenterUpdate(context.Background(), "0.1.0"); err == nil {
		t.Fatal("update was queued without the host service")
	}
}
