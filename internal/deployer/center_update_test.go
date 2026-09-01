package deployer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/petauron/vastora/internal/deployapi"
)

func centerUpdateRequest(version string) deployapi.CenterUpdateRequest {
	return deployapi.CenterUpdateRequest{Version: version, InstallerBaseURL: "https://releases.example.com", InstallerHost: "releases.example.com", InstallerPort: "443", InstallerAddress: "203.0.113.10"}
}

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
	if _, err := updater.StartCenterUpdate(context.Background(), centerUpdateRequest("0.1.0-alpha.47")); err == nil {
		t.Fatal("same-version update was accepted")
	}
	queued, err := updater.StartCenterUpdate(context.Background(), centerUpdateRequest("0.1.0-alpha.48"))
	if err != nil || queued.State != "queued" || queued.TargetVersion != "0.1.0-alpha.48" {
		t.Fatalf("unexpected queued status: %#v err=%v", queued, err)
	}
	request, err := os.ReadFile(filepath.Join(installDir, ".update-request"))
	if err != nil || string(request) != "0.1.0-alpha.48\nhttps://releases.example.com\nreleases.example.com\n443\n203.0.113.10\n" {
		t.Fatalf("unexpected update request %q err=%v", request, err)
	}
	persisted, err := updater.CenterUpdateStatus(context.Background())
	if err != nil || persisted.State != "queued" || persisted.TargetVersion != queued.TargetVersion {
		t.Fatalf("unexpected persisted status: %#v err=%v", persisted, err)
	}
	if _, err := updater.StartCenterUpdate(context.Background(), centerUpdateRequest("not-a-version")); err == nil {
		t.Fatal("invalid release version was accepted")
	}
	invalidPin := centerUpdateRequest("0.1.0-alpha.49")
	invalidPin.InstallerAddress = "not-an-address"
	if _, err := updater.StartCenterUpdate(context.Background(), invalidPin); err == nil {
		t.Fatal("invalid release DNS pin was accepted")
	}
}

func TestFileCenterUpdaterRequiresInstalledHostService(t *testing.T) {
	updater := FileCenterUpdater{InstallDir: t.TempDir()}
	status, err := updater.CenterUpdateStatus(context.Background())
	if err != nil || status.Available || status.State != "idle" {
		t.Fatalf("unexpected unavailable status: %#v err=%v", status, err)
	}
	if _, err := updater.StartCenterUpdate(context.Background(), centerUpdateRequest("0.1.0")); err == nil {
		t.Fatal("update was queued without the host service")
	}
}

func TestFileCenterUpdaterDoesNotLeaveAStuckQueueWhenRequestWriteFails(t *testing.T) {
	installDir := t.TempDir()
	for _, name := range []string{".update-service-enabled", "update-center.sh"} {
		if err := os.WriteFile(filepath.Join(installDir, name), []byte("ready\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(installDir, "release.env"), []byte("VASTORA_VERSION=0.1.0-alpha.47\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(installDir, ".update-request"), 0o700); err != nil {
		t.Fatal(err)
	}

	updater := FileCenterUpdater{InstallDir: installDir}
	if _, err := updater.StartCenterUpdate(context.Background(), centerUpdateRequest("0.1.0-alpha.48")); err == nil {
		t.Fatal("request write failure was accepted")
	}
	status, err := updater.CenterUpdateStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "failed" || status.TargetVersion != "0.1.0-alpha.48" {
		t.Fatalf("queue failure left an invalid status: %#v", status)
	}
}

func TestFileCenterUpdaterRecreatesConsumedActiveRequest(t *testing.T) {
	installDir := t.TempDir()
	for _, name := range []string{".update-service-enabled", "update-center.sh"} {
		if err := os.WriteFile(filepath.Join(installDir, name), []byte("ready\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(installDir, "release.env"), []byte("VASTORA_VERSION=0.1.0-alpha.48\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := []byte(`{"state":"applying","targetVersion":"0.1.0-alpha.48","message":"applying","updatedAt":"2026-08-30T00:00:00Z"}` + "\n")
	if err := os.WriteFile(filepath.Join(installDir, ".update-status.json"), status, 0o600); err != nil {
		t.Fatal(err)
	}

	updater := FileCenterUpdater{InstallDir: installDir}
	recovered, err := updater.StartCenterUpdate(context.Background(), centerUpdateRequest("0.1.0-alpha.48"))
	if err != nil || recovered.State != "applying" {
		t.Fatalf("active update was not recovered: %#v err=%v", recovered, err)
	}
	request, err := os.ReadFile(filepath.Join(installDir, ".update-request"))
	if err != nil || string(request) != "0.1.0-alpha.48\nhttps://releases.example.com\nreleases.example.com\n443\n203.0.113.10\n" {
		t.Fatalf("recovered request = %q err=%v", request, err)
	}
}
