package deployer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/deployapi"
)

func TestHostBackupLockPreventsConfigurationDuringSnapshot(t *testing.T) {
	manager := &FileHostBackupManager{InstallDir: t.TempDir()}
	lock, err := manager.lockHost()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	_, err = manager.ConfigureHostBackup(context.Background(), deployapi.HostBackupInput{})
	if err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("configuration did not reject active host lock: %v", err)
	}
}

func TestHostBackupPasswordNotReturnedAndScopePreserved(t *testing.T) {
	ctx := context.Background()
	manager := &FileHostBackupManager{InstallDir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(manager.InstallDir, ".backup-service-enabled"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(manager.directory(), 0700); err != nil {
		t.Fatal(err)
	}
	scope := []byte(`{"reviewed":true,"containers":["fixture-app"]}`)
	scopePath := filepath.Join(manager.directory(), "scope.json")
	if err := os.WriteFile(scopePath, scope, 0600); err != nil {
		t.Fatal(err)
	}
	input := deployapi.HostBackupInput{HostBackupSettings: deployapi.HostBackupSettings{Directory: "/srv/fixture-backups", TimeUTC: "03:00", IntervalDays: 1, KeepLast: 7}, Password: "fixture-password-only"}
	result, err := manager.ConfigureHostBackup(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.PasswordSet || result.Settings.Enabled {
		t.Fatal("password or schedule state incorrect")
	}
	config, err := os.ReadFile(filepath.Join(manager.directory(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), input.Password) {
		t.Fatal("password leaked to public configuration")
	}
	actual, err := os.ReadFile(scopePath)
	if err != nil || string(actual) != string(scope) {
		t.Fatal("host scope overwritten")
	}
	info, err := os.Stat(filepath.Join(manager.directory(), "password"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("password permissions too broad")
	}
	input.Password = "different-fixture-password"
	if _, err := manager.ConfigureHostBackup(ctx, input); err == nil {
		t.Fatal("password replacement accepted")
	}
}

func TestHostBackupDoesNotQueueWithoutSavedSettings(t *testing.T) {
	manager := &FileHostBackupManager{InstallDir: t.TempDir()}
	if _, err := manager.StartHostBackup(context.Background()); err == nil {
		t.Fatal("unconfigured backup queued")
	}
	if regularFile(filepath.Join(manager.directory(), "request")) {
		t.Fatal("unconfigured request created")
	}
}
