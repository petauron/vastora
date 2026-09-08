package recovery

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/deployer"
	"github.com/petauron/vastora/internal/secret"
	"gopkg.in/yaml.v3"
)

func openDatabase(path string) (*sql.DB, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("recovery: source database is unavailable or unsafe")
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// SQLite's backup snapshot includes committed WAL state without copying a live
// database file. Temporary plaintext is restricted to a 0700 staging directory.
func snapshotDatabase(ctx context.Context, source, staging, name string) ([]byte, error) {
	db, err := openDatabase(source)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	path := filepath.Join(staging, name)
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return nil, errors.New("recovery: consistent database snapshot failed")
	}
	return ReadRegularFile(path, maxArtifactBytes)
}

func databaseIntegrity(ctx context.Context, db *sql.DB) (int, error) {
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return 0, errors.New("recovery: database integrity check failed")
	}
	var schema int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return 0, err
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return 0, errors.New("recovery: database relationship check failed")
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		return 0, errors.New("recovery: database contains broken relationships")
	}
	return schema, nil
}

func ExportAgent(ctx context.Context, dataDir, tailscaleState, release, output, password string) (Artifact, error) {
	if err := ValidatePassword(password); err != nil {
		return Artifact{}, err
	}
	if !filepath.IsAbs(dataDir) || release == "" {
		return Artifact{}, errors.New("recovery: Agent source directory and release are required")
	}
	staging, err := os.MkdirTemp("", "vastora-agent-backup-*")
	if err != nil {
		return Artifact{}, err
	}
	defer os.RemoveAll(staging)
	key, err := secret.LoadKey(filepath.Join(dataDir, "agent.key"))
	if err != nil {
		return Artifact{}, errors.New("recovery: Agent key is unavailable or unsafe")
	}
	snapshot, err := snapshotDatabase(ctx, filepath.Join(dataDir, "agent.db"), staging, "agent.db")
	if err != nil {
		return Artifact{}, err
	}
	files := map[string][]byte{"agent.db": snapshot, "agent.key": key}
	files[agent.HostInstallStateName], err = ReadRegularFile(filepath.Join(dataDir, agent.HostInstallStateName), 4096)
	if err != nil {
		return Artifact{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, agent.HostInstallStateName), files[agent.HostInstallStateName], 0o600); err != nil {
		return Artifact{}, err
	}
	if tailscaleState != "" {
		files["tailscaled.state"], err = ReadRegularFile(tailscaleState, 16<<20)
		if err != nil {
			return Artifact{}, err
		}
	}
	manifest, err := inspectAgent(ctx, staging, files)
	if err != nil {
		return Artifact{}, err
	}
	after, err := secret.LoadKey(filepath.Join(dataDir, "agent.key"))
	if err != nil || !bytes.Equal(key, after) {
		return Artifact{}, errors.New("recovery: Agent identity changed during export")
	}
	for name, source := range map[string]string{agent.HostInstallStateName: filepath.Join(dataDir, agent.HostInstallStateName), "tailscaled.state": tailscaleState} {
		if source == "" {
			continue
		}
		current, err := ReadRegularFile(source, 16<<20)
		if err != nil || !bytes.Equal(current, files[name]) {
			return Artifact{}, errors.New("recovery: Agent ownership or Tailscale identity changed during export; retry after enrollment settles")
		}
	}
	manifest.Release, manifest.ComponentVersion = release, release
	return Write(output, password, manifest, files)
}

func inspectAgent(ctx context.Context, staging string, files map[string][]byte) (Manifest, error) {
	if len(files) < 3 || len(files) > 4 || len(files["agent.key"]) != secret.KeySize || len(files["agent.db"]) == 0 || len(files[agent.HostInstallStateName]) == 0 {
		return Manifest{}, errors.New("recovery: incomplete Agent backup")
	}
	for name := range files {
		if name != "agent.key" && name != "agent.db" && name != "tailscaled.state" && name != agent.HostInstallStateName {
			return Manifest{}, errors.New("recovery: unexpected Agent backup member")
		}
	}
	hostState, err := agent.ReadHostInstallState(staging)
	if err != nil {
		return Manifest{}, errors.New("recovery: Agent ownership provenance is invalid")
	}
	if hostState.TailscaleEnrolled && !json.Valid(files["tailscaled.state"]) {
		return Manifest{}, errors.New("recovery: enrolled Agent requires the original Tailscale state; use replacement enrollment instead if it was lost")
	}
	db, err := openDatabase(filepath.Join(staging, "agent.db"))
	if err != nil {
		return Manifest{}, err
	}
	defer db.Close()
	schema, err := databaseIntegrity(ctx, db)
	if err != nil || schema != agent.CurrentSchemaVersion() {
		return Manifest{}, errors.New("recovery: Agent database requires its exact compatible release; no downgrade is supported")
	}
	var binding int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM storage_key_binding WHERE id = 1`).Scan(&binding); err != nil || binding != 1 {
		return Manifest{}, errors.New("recovery: Agent key binding is unavailable")
	}
	if agent.VerifyRecoveryDatabaseKey(ctx, db, files["agent.key"]) != nil {
		return Manifest{}, errors.New("recovery: Agent database key verification failed")
	}
	var id string
	var sealed []byte
	if err := db.QueryRowContext(ctx, `SELECT agent_id, sealed_private_key FROM control_plane_connection WHERE id = 1`).Scan(&id, &sealed); err != nil {
		return Manifest{}, errors.New("recovery: Agent enrollment is unavailable")
	}
	privateKey, err := secret.Open(files["agent.key"], sealed, []byte("agent-control-plane-key:"+id))
	if err != nil {
		return Manifest{}, errors.New("recovery: Agent identity cannot be decrypted")
	}
	publicKey, err := controlplane.PublicKey(privateKey)
	if err != nil {
		return Manifest{}, errors.New("recovery: Agent identity is invalid")
	}
	return Manifest{Kind: "agent", ComponentID: id, SchemaVersion: schema, IdentityHash: Digest(publicKey), Dependencies: []string{"center", "headscale"}}, nil
}

// ExportHeadscale supports the bundled, pinned SQLite layout only. External
// Headscale deployments must use their own documented backup procedure.
func ExportHeadscale(ctx context.Context, dataDir, configDir, release, output, password string) (Artifact, error) {
	if err := ValidatePassword(password); err != nil {
		return Artifact{}, err
	}
	if !filepath.IsAbs(dataDir) || !filepath.IsAbs(configDir) || release == "" {
		return Artifact{}, errors.New("recovery: absolute Headscale source directories and release are required")
	}
	containerID, err := verifyBundledHeadscaleSource(ctx, dataDir, configDir)
	if err != nil {
		return Artifact{}, err
	}
	staging, err := os.MkdirTemp("", "vastora-headscale-backup-*")
	if err != nil {
		return Artifact{}, err
	}
	defer os.RemoveAll(staging)
	files := map[string][]byte{}
	for _, name := range []string{"config.yaml", "policy.hujson", "derp.yaml"} {
		files[name], err = ReadRegularFile(filepath.Join(configDir, name), 4<<20)
		if err != nil {
			return Artifact{}, err
		}
	}
	for _, name := range []string{"noise_private.key", "derp_server_private.key"} {
		files[name], err = ReadRegularFile(filepath.Join(dataDir, name), 1<<20)
		if err != nil {
			return Artifact{}, err
		}
	}
	files["db.sqlite"], err = snapshotDatabase(ctx, filepath.Join(dataDir, "db.sqlite"), staging, "db.sqlite")
	if err != nil {
		return Artifact{}, err
	}
	manifest, err := inspectHeadscale(ctx, staging, files)
	if err != nil {
		return Artifact{}, err
	}
	for name, original := range files {
		if name == "db.sqlite" {
			continue
		}
		directory := configDir
		if strings.HasSuffix(name, ".key") {
			directory = dataDir
		}
		current, err := ReadRegularFile(filepath.Join(directory, name), 4<<20)
		if err != nil || !bytes.Equal(current, original) {
			return Artifact{}, errors.New("recovery: Headscale configuration or identity changed during export")
		}
	}
	manifest.Release = release
	afterID, err := verifyBundledHeadscaleSource(ctx, dataDir, configDir)
	if err != nil || afterID != containerID {
		return Artifact{}, errors.New("recovery: bundled Headscale changed during export")
	}
	return Write(output, password, manifest, files)
}

func inspectHeadscale(ctx context.Context, staging string, files map[string][]byte) (Manifest, error) {
	if len(files) != 6 {
		return Manifest{}, errors.New("recovery: incomplete bundled Headscale backup")
	}
	for _, name := range []string{"config.yaml", "policy.hujson", "derp.yaml", "noise_private.key", "derp_server_private.key", "db.sqlite"} {
		if len(files[name]) == 0 {
			return Manifest{}, errors.New("recovery: incomplete bundled Headscale backup")
		}
	}
	// The bundled renderer writes JSON ACLs. Rich external HUJSON policies are
	// not silently reinterpreted as a supported built-in recovery contract.
	if !json.Valid(files["policy.hujson"]) {
		return Manifest{}, errors.New("recovery: unsupported or invalid bundled Headscale policy")
	}
	var derp map[string]any
	if yaml.Unmarshal(files["derp.yaml"], &derp) != nil || derp["regions"] == nil {
		return Manifest{}, errors.New("recovery: invalid bundled DERP map")
	}
	noisePublic, err := headscaleMachinePublic(files["noise_private.key"])
	if err != nil {
		return Manifest{}, err
	}
	derpPublic, err := headscaleMachinePublic(files["derp_server_private.key"])
	if err != nil || bytes.Equal(noisePublic, derpPublic) {
		return Manifest{}, errors.New("recovery: Headscale Noise and DERP identities must be valid and distinct")
	}
	var config struct {
		URL   string `yaml:"server_url"`
		Noise struct {
			Key string `yaml:"private_key_path"`
		} `yaml:"noise"`
		Database struct {
			Type   string `yaml:"type"`
			SQLite struct {
				Path string `yaml:"path"`
			} `yaml:"sqlite"`
		} `yaml:"database"`
		Policy struct {
			Mode string `yaml:"mode"`
			Path string `yaml:"path"`
		} `yaml:"policy"`
		DERP struct {
			Server struct {
				Enabled bool   `yaml:"enabled"`
				Key     string `yaml:"private_key_path"`
			} `yaml:"server"`
		} `yaml:"derp"`
	}
	if yaml.Unmarshal(files["config.yaml"], &config) != nil || config.Database.Type != "sqlite" || config.Database.SQLite.Path != "/var/lib/headscale/db.sqlite" || config.Noise.Key != "/var/lib/headscale/noise_private.key" || !config.DERP.Server.Enabled || config.DERP.Server.Key != "/var/lib/headscale/derp_server_private.key" || config.Policy.Mode != "file" || config.Policy.Path != "/etc/headscale/policy.hujson" {
		return Manifest{}, errors.New("recovery: Headscale layout is not the supported bundled configuration")
	}
	endpoint, err := url.Parse(config.URL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return Manifest{}, errors.New("recovery: invalid Headscale identity endpoint")
	}
	db, err := openDatabase(filepath.Join(staging, "db.sqlite"))
	if err != nil {
		return Manifest{}, err
	}
	defer db.Close()
	schema, err := databaseIntegrity(ctx, db)
	if err != nil {
		return Manifest{}, err
	}
	var requiredTables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('users', 'nodes')`).Scan(&requiredTables); err != nil || requiredTables != 2 {
		return Manifest{}, errors.New("recovery: Headscale database is missing its identity tables")
	}
	return Manifest{Kind: "headscale", ComponentID: strings.TrimSuffix(config.URL, "/"), ComponentVersion: deployer.DefaultHeadscaleImage, SchemaVersion: schema, IdentityHash: Digest(noisePublic), Dependencies: []string{"center"}}, nil
}

// Headscale v0.29.3 persists Tailscale MachinePrivate.MarshalText (privkey:
// followed by 32 hex-encoded bytes). Crypto stays in the existing X25519 helper.
func headscaleMachinePublic(encoded []byte) ([]byte, error) {
	value := strings.TrimSpace(string(encoded))
	if !strings.HasPrefix(value, "privkey:") {
		return nil, errors.New("recovery: unsupported Headscale key encoding")
	}
	private, err := hex.DecodeString(strings.TrimPrefix(value, "privkey:"))
	if err != nil || len(private) != 32 || bytes.Equal(private, make([]byte, 32)) {
		return nil, errors.New("recovery: invalid Headscale identity")
	}
	public, err := controlplane.PublicKey(private)
	if err != nil {
		return nil, errors.New("recovery: invalid Headscale identity")
	}
	return public, nil
}

// Inspect authenticates the archive then checks database/key/identity binding
// in isolated storage. It never opens the normal Agent or Center initializer.
func Inspect(ctx context.Context, input, password, release string) (Artifact, error) {
	artifact, files, err := Read(input, password)
	if err != nil {
		return Artifact{}, err
	}
	staging, err := stageFiles(files)
	if err != nil {
		return Artifact{}, err
	}
	defer os.RemoveAll(staging)
	if err := validateComponent(ctx, artifact.Manifest, staging, files, release); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func validateComponent(ctx context.Context, expected Manifest, staging string, files map[string][]byte, release string) error {
	if expected.Release != release {
		return errors.New("recovery: restore requires the exact artifact release; upgrade only after recovery")
	}
	var current Manifest
	var err error
	if expected.Kind == "agent" {
		current, err = inspectAgent(ctx, staging, files)
	} else {
		current, err = inspectHeadscale(ctx, staging, files)
	}
	if err != nil {
		return err
	}
	if current.ComponentID != expected.ComponentID || current.IdentityHash != expected.IdentityHash || current.SchemaVersion != expected.SchemaVersion || expected.Kind == "headscale" && current.ComponentVersion != expected.ComponentVersion {
		return errors.New("recovery: component identity or schema does not match its manifest")
	}
	return nil
}

func stageFiles(files map[string][]byte) (string, error) {
	directory, err := os.MkdirTemp("", "vastora-recovery-inspect-*")
	if err != nil {
		return "", err
	}
	for name, data := range files {
		if !safeName(name) {
			os.RemoveAll(directory)
			return "", errors.New("recovery: unsafe member path")
		}
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			os.RemoveAll(directory)
			return "", err
		}
	}
	return directory, nil
}

// Restore publishes one fresh directory. Headscale config and data members are
// staged together; the runbook mounts/copies them before starting the service.
func Restore(ctx context.Context, input, destination, password, release, expectedID, expectedIdentity string) (Artifact, error) {
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || filepath.Dir(destination) == destination || expectedID == "" || expectedIdentity == "" {
		return Artifact{}, errors.New("recovery: clean destination and expected component identity are required")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return Artifact{}, errors.New("recovery: restore destination must not exist")
	}
	artifact, files, err := Read(input, password)
	if err != nil {
		return Artifact{}, err
	}
	if artifact.Manifest.ComponentID != expectedID || artifact.Manifest.IdentityHash != expectedIdentity {
		return Artifact{}, errors.New("recovery: restore identity does not match the requested component")
	}
	parent := filepath.Dir(destination)
	staging, err := os.MkdirTemp(parent, ".vastora-restore-*")
	if err != nil {
		return Artifact{}, errors.New("recovery: restore parent must already exist")
	}
	defer os.RemoveAll(staging)
	for name, data := range files {
		if err := PublishPrivateFile(filepath.Join(staging, name), data); err != nil {
			return Artifact{}, err
		}
	}
	if err := validateComponent(ctx, artifact.Manifest, staging, files, release); err != nil {
		return Artifact{}, err
	}
	if err := SyncDirectory(staging); err != nil {
		return Artifact{}, err
	}
	if err := renameExclusive(staging, destination); err != nil {
		return Artifact{}, errors.New("recovery: cannot publish restored directory")
	}
	if err := SyncDirectory(parent); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}
