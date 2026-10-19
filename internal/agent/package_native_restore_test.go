package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestNativeRestorePreservesPersistentDataAndOwnership(t *testing.T) {
	b, task, receipt, active := nativeLifecycleFixture(t)
	worker := &task.Manifest.Runtime.NativeServices.Services[1]
	worker.Process.StateDirectories = []string{"data"}
	uid, gid := os.Geteuid(), os.Getegid()
	if uid != 0 {
		if gid == 0 {
			t.Skip("managed fixture needs a non-root primary group")
		}
		task.Manifest.Runtime.NativeServices.Users = []string{"worker"}
		worker.ManagedUser, worker.Process.User = "worker", ""
		account := nativeAccountName(task.ApplicationID, "worker")
		read := b.Manager.ReadCommand
		b.Manager.ReadCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "getent" && len(args) == 2 && args[0] == "passwd" && args[1] == account {
				return []byte(fmt.Sprintf("%s:x:%d:%d:Vastora %s:/nonexistent:/usr/sbin/nologin\n", account, uid, gid, task.ApplicationID)), nil
			}
			return read(ctx, name, args...)
		}
		receipt.Resources = append(receipt.Resources, RuntimeResource{Kind: "user", LogicalName: "worker", Name: account, ID: fmt.Sprintf("%d:%d", uid, gid)})
	}
	plan, err := planNativePackage(task.ApplicationID, task.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	state := plan.State["data"]
	path := b.Manager.path(state.Path)
	if err := os.MkdirAll(filepath.Join(path, "history"), 0700); err != nil {
		t.Fatal(err)
	}
	receipt.Resources = append(receipt.Resources, RuntimeResource{Kind: "directory", LogicalName: "data", Name: filepath.Base(state.Path), Path: state.Path, Persistent: true, ID: fmt.Sprintf("%d:%d", uid, gid)})
	file := filepath.Join(path, "history", "records.db")
	if err := os.WriteFile(file, []byte("original-history"), 0600); err != nil {
		t.Fatal(err)
	}
	// Both workers were running before backup. Restore must preserve that set.
	for _, unit := range plan.Units {
		active[unit.Name] = true
	}
	task.ID = "data-snapshot"
	if err := b.Backup(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	selected := append([]RuntimeBackup(nil), receipt.Backups...)
	if err := b.Resume(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("new-history"), 0600); err != nil {
		t.Fatal(err)
	}
	task.ID = "data-restore"
	if err := b.Backup(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	if err := b.Restore(context.Background(), task, receipt, selected, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ path, content string }{
		{file, "original-history"},
		{filepath.Join(path+".retained-"+packageIdentity(task.ID), "history", "records.db"), "new-history"},
	} {
		raw, err := os.ReadFile(check.path)
		if err != nil || string(raw) != check.content {
			t.Fatalf("data or predecessor lost: %s: %v", check.path, err)
		}
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}
		owner := info.Sys().(*syscall.Stat_t)
		if owner.Uid != uint32(uid) || owner.Gid != uint32(gid) || info.Mode().Perm() != 0600 {
			t.Fatal("restored data ownership or mode changed")
		}
	}
	if err := b.Inspect(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	for _, unit := range plan.Units {
		if !active[unit.Name] {
			t.Fatal("previously running worker not resumed")
		}
	}
}

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
