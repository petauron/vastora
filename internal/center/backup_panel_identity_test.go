package center

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/secret"
)

func TestCenterBackupPreservesDerivedPanelIdentity(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollAccessTestNode(t, s, "panel-owner", "10.0.0.80")
	want, err := s.panelIdentity(ctx, node.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	id := panelIdentityID(node.ID)
	var before []byte
	if err := s.db.QueryRow(`SELECT sealed FROM secrets WHERE id=?`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "panel.vastora")
	if err := s.Backup(ctx, archive, "panel-backup-test-password"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, dir, "panel-backup-test-password"); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err := restored.panelIdentity(ctx, node.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	var after []byte
	if err := restored.db.QueryRow(`SELECT sealed FROM secrets WHERE id=?`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) || !bytes.Equal(before, after) {
		t.Fatal("backup changed panel identity")
	}
}

func TestCenterBackupRejectsUnprovenPanelOwnership(t *testing.T) {
	for _, kind := range []string{"unknown-node", "wrong-authentication-context", "conflicting-owner"} {
		t.Run(kind, func(t *testing.T) {
			s := openOrchestrationStore(t)
			defer s.Close()
			ctx := context.Background()
			node := enrollAccessTestNode(t, s, "panel-owner", "10.0.0.80")
			if _, err := s.panelIdentity(ctx, node.ID, true); err != nil {
				t.Fatal(err)
			}
			id := panelIdentityID(node.ID)
			switch kind {
			case "unknown-node":
				unknown := "panel-" + strings.Repeat("a", 64)
				sealed, err := secret.Seal(s.key, []byte("unknown"), []byte(unknown))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES(?,?,'','')`, unknown, sealed); err != nil {
					t.Fatal(err)
				}
			case "wrong-authentication-context":
				sealed, err := secret.Seal(s.key, []byte("wrong-context"), []byte("not-the-node-panel"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`UPDATE secrets SET sealed=? WHERE id=?`, sealed, id); err != nil {
					t.Fatal(err)
				}
			case "conflicting-owner":
				if _, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES('official_catalog_signing_key',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, id); err != nil {
					t.Fatal(err)
				}
			}
			if err := verifyCenterEncryptedState(ctx, s.db, s.key); err == nil {
				t.Fatal("unproven panel identity accepted")
			}
		})
	}
}
