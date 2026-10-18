package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestNativeRestoreStagesAllConfigurationAndRetainsPredecessors(t *testing.T) {
	b, task, receipt, active := nativeLifecycleFixture(t)
	task.ID = "original-snapshot"
	if err := b.Backup(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	selected := append([]RuntimeBackup(nil), receipt.Backups...)
	if err := b.Resume(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	for _, unit := range b.prepared.Plan.Units {
		if err := os.WriteFile(b.Manager.path(unit.ConfigDirectory+"/environment"), []byte("APPLICATION_SETTING=new\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	task.ID = "restore-operation"
	if err := b.Backup(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	if err := b.Restore(context.Background(), task, receipt, selected, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, unit := range b.prepared.Plan.Units {
		path := b.Manager.path(unit.ConfigDirectory + "/environment")
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != "INITIAL=1\n" {
			t.Fatal("configuration not restored", err)
		}
		old, err := os.ReadFile(path + ".retained-" + packageIdentity(task.ID))
		if err != nil || string(old) != "APPLICATION_SETTING=new\n" {
			t.Fatal("predecessor lost", err)
		}
		if active[unit.Name] != unit.Automatic {
			t.Fatal("restore changed pre-operation running set")
		}
	}
}

func TestNativeRestoreRejectsCorruptionAndVersionBeforeReplacement(t *testing.T) {
	for _, failure := range []string{"corrupt", "version", "missing"} {
		t.Run(failure, func(t *testing.T) {
			b, task, receipt, _ := nativeLifecycleFixture(t)
			task.ID = "snapshot"
			if err := b.Backup(context.Background(), task, receipt); err != nil {
				t.Fatal(err)
			}
			selected := append([]RuntimeBackup(nil), receipt.Backups...)
			for i := range selected {
				if strings.HasPrefix(selected[i].Resource, "file:worker:") {
					switch failure {
					case "corrupt":
						if err := os.WriteFile(selected[i].Path, []byte("corrupt"), 0600); err != nil {
							t.Fatal(err)
						}
					case "version":
						selected[i].PackageVersion = "older"
					case "missing":
						selected = append(selected[:i], selected[i+1:]...)
					}
					break
				}
			}
			task.ID = "restore"
			for _, unit := range b.prepared.Plan.Units {
				if err := os.WriteFile(b.Manager.path(unit.ConfigDirectory+"/environment"), []byte("UNCHANGED=yes\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := b.Backup(context.Background(), task, receipt); err != nil {
				t.Fatal(err)
			}
			if err := b.Restore(context.Background(), task, receipt, selected, func() error { return nil }); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			for _, unit := range b.prepared.Plan.Units {
				raw, err := os.ReadFile(b.Manager.path(unit.ConfigDirectory + "/environment"))
				if err != nil || string(raw) != "UNCHANGED=yes\n" {
					t.Fatal("partial preflight changed live configuration", err)
				}
			}
		})
	}
}

func TestNativeRestoreRefusesLiveWriters(t *testing.T) {
	b, task, receipt, _ := nativeLifecycleFixture(t)
	if err := b.Restore(context.Background(), task, receipt, nil, func() error { return nil }); err == nil {
		t.Fatal("restoring under live writer was allowed")
	}
}
