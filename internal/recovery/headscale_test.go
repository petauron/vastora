package recovery

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeadscaleArtifactBindsKeysDatabaseAndCompatibleImage(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(directory, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`CREATE TABLE users(id INTEGER PRIMARY KEY)`, `CREATE TABLE nodes(id INTEGER PRIMARY KEY, user_id INTEGER)`, `INSERT INTO users VALUES(1)`, `INSERT INTO nodes VALUES(7,1)`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"db.sqlite":               data,
		"noise_private.key":       []byte("privkey:" + strings.Repeat("11", 32)),
		"derp_server_private.key": []byte("privkey:" + strings.Repeat("22", 32)),
		"policy.hujson":           []byte(`{"grants":[]}`),
		"derp.yaml":               []byte("regions: {}\n"),
		"config.yaml":             []byte("server_url: https://headscale.example.com\nnoise:\n  private_key_path: /var/lib/headscale/noise_private.key\ndatabase:\n  type: sqlite\n  sqlite:\n    path: /var/lib/headscale/db.sqlite\npolicy:\n  mode: file\n  path: /etc/headscale/policy.hujson\nderp:\n  server:\n    enabled: true\n    private_key_path: /var/lib/headscale/derp_server_private.key\n"),
	}
	manifest, err := inspectHeadscale(ctx, directory, files)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Release = "test-release"
	password := "protected-headscale-backup"
	output := filepath.Join(t.TempDir(), "headscale.vastora")
	artifact, err := Write(output, password, manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, output, password, manifest.Release); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(ctx, output, destination, password, manifest.Release, manifest.ComponentID, artifact.Manifest.IdentityHash); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(destination, "noise_private.key"))
	if err != nil || string(restored) != string(files["noise_private.key"]) {
		t.Fatal("Noise identity was not preserved")
	}
	manifest.ComponentVersion = "different-headscale-release"
	incompatible := filepath.Join(t.TempDir(), "incompatible.vastora")
	if _, err := Write(incompatible, password, manifest, files); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, incompatible, password, manifest.Release); err == nil {
		t.Fatal("incompatible Headscale image accepted")
	}
	files["derp_server_private.key"] = files["noise_private.key"]
	if _, err := inspectHeadscale(ctx, directory, files); err == nil {
		t.Fatal("shared Noise/DERP identity accepted")
	}
	delete(files, "policy.hujson")
	if _, err := inspectHeadscale(ctx, directory, files); err == nil {
		t.Fatal("incomplete Headscale backup accepted")
	}
}
