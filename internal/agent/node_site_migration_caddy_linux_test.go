package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/nodesite"
)

// Optional real-engine acceptance uses only existing reviewed binaries, loopback
// high ports, an owned Unix socket and synthetic credentials. It never uses a
// platform service, existing Caddy admin endpoint or production enrollment.
func TestNodeSiteHostnameMigrationRealCaddy(t *testing.T) {
	if os.Getenv("VASTORA_HOSTNAME_MIGRATION_LAB") != "1" {
		t.Skip("requires explicitly authorized isolated Caddy acceptance")
	}
	binary := os.Getenv("VASTORA_MIGRATION_CADDY_BINARY")
	for _, tool := range []struct{ path, sha string }{{binary, os.Getenv("VASTORA_MIGRATION_CADDY_SHA256")}, {os.Getenv("VASTORA_SHARED_CADDY_XRAY"), os.Getenv("VASTORA_MIGRATION_XRAY_SHA256")}} {
		data, err := os.ReadFile(tool.path)
		if err != nil || !filepath.IsAbs(tool.path) {
			t.Fatal("reviewed existing engine unavailable")
		}
		sum := sha256.Sum256(data)
		if tool.sha == "" || hex.EncodeToString(sum[:]) != tool.sha {
			t.Fatal("existing engine digest mismatch")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	s, err := Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	port, httpPort := freeNodeSitePort(t), freeNodeSitePort(t)
	owned := func(host string) (CaddySite, nodesite.Task) {
		key, private := nodeSiteTestKey(t)
		chain := nodeSiteTestChain(t, key, host)
		certificate, err := nodeSiteCertificate(host, chain, private)
		if err != nil {
			t.Fatal(err)
		}
		return CaddySite{ApplicationID: "hostname-migration-lab", Hostname: host, Address: "127.0.0.1", HTTPPort: httpPort, TLSPort: port, HTML: "synthetic owned website", Certificate: certificate}, nodesite.Task{ApplicationID: "hostname-migration-lab", AppKey: "vastora-official/daylight", Hostname: host, Port: port, Revision: 1, Action: "apply", CertificatePEM: chain, PrivateKeyPEM: private}
	}
	old, oldTask := owned("us1.example.com")
	next, task := owned("us-random.example.com")
	if err = s.saveCaddySite(ctx, caddySiteJournal{ApplicationID: old.ApplicationID, State: "applied", Site: &old}); err != nil {
		t.Fatal(err)
	}
	if err = s.saveNodeSiteJournal(ctx, nodeSiteJournal{Runtime: 2, Task: oldTask, State: "applied"}); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "caddy.sock")
	driver, err := NewCaddyGatewayDriver("unix://" + socket)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := json.Marshal(map[string]any{"admin": map[string]any{"listen": driver.AdminListen}, "apps": map[string]any{"http": map[string]any{"servers": map[string]any{}}}})
	initial, err := composeCaddySites(base, gateway.DesiredState{}, []CaddySite{old}, false)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "initial.json")
	if err = os.WriteFile(config, initial, 0600); err != nil {
		t.Fatal(err)
	}
	var process *exec.Cmd
	stop := func() {
		if process != nil && process.Process != nil {
			_ = process.Process.Kill()
			_ = process.Wait()
			process = nil
		}
	}
	t.Cleanup(stop)
	start := func(resume bool) {
		args := []string{"run", "--config", config}
		if resume {
			args = append(args, "--resume")
		}
		process = exec.CommandContext(ctx, binary, args...)
		process.Env = append(os.Environ(), "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"))
		process.Stdout, process.Stderr = io.Discard, io.Discard
		if err = process.Start(); err != nil {
			t.Fatal("standalone Caddy start failed", err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for probeCaddySite(ctx, old) != nil {
			if time.Now().After(deadline) {
				t.Fatal("standalone Caddy did not become ready")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	start(false)
	attach := func() {
		s.sharedCaddy = driver
		s.reloadSharedCaddy = func(ctx context.Context) error {
			sites, err := s.SharedCaddySites(ctx)
			if err != nil {
				return err
			}
			raw, err := composeCaddySites(base, gateway.DesiredState{}, sites, false)
			if err != nil {
				return err
			}
			return driver.load(ctx, raw)
		}
	}
	attach()
	normalLoad := s.reloadSharedCaddy
	failed := false
	s.reloadSharedCaddy = func(ctx context.Context) error {
		if err := normalLoad(ctx); err != nil {
			return err
		}
		if !failed {
			failed = true
			return errors.New("controlled failure after real Caddy load")
		}
		return nil
	}
	task.Action, task.PreviousHostname, task.Revision = "prepare-migration", old.Hostname, 2
	if _, err = s.executeWebsiteMigration(ctx, task, nodeSiteHost{site: old}); err == nil {
		t.Fatal("partial real load failure was accepted")
	}
	if err = probeCaddySite(ctx, old); err != nil {
		t.Fatal("old website was not recovered")
	}
	if err = probeCaddySite(ctx, next); err == nil {
		t.Fatal("failed candidate remained published")
	}
	s.reloadSharedCaddy = normalLoad
	if _, err = s.executeWebsiteMigration(ctx, task, nodeSiteHost{site: old}); err != nil {
		t.Fatal("real candidate preparation failed")
	}
	if err = probeCaddySite(ctx, old); err != nil {
		t.Fatal("old website lost during preparation")
	}
	if err = probeCaddySite(ctx, next); err != nil {
		t.Fatal("new certificate/H2 website unavailable")
	}
	stop()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	attach()
	start(true)
	if err = probeCaddySite(ctx, next); err != nil {
		t.Fatal("prepared candidate lost across restart")
	}
	prepared, err := s.preparedWebsiteBinding(ctx, old.ApplicationID, next.Hostname)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.updateCaddySite(ctx, old.ApplicationID, &prepared, func(ctx context.Context) error { return probeCaddySite(ctx, prepared) }); err != nil {
		t.Fatal("prepared installed binding failed")
	}
	sharedCaddyRealityLab(t, ctx, root, prepared)
	accept := task
	accept.Action = "accept-migration"
	accept.CertificatePEM, accept.PrivateKeyPEM = "", ""
	for range 2 {
		if _, err = s.executeWebsiteMigration(ctx, accept, nodeSiteHost{site: prepared}); err != nil {
			t.Fatal("real migration acceptance/replay failed")
		}
	}
	if err = probeCaddySite(ctx, prepared); err != nil {
		t.Fatal("new website lost after acceptance")
	}
	if err = probeCaddySite(ctx, old); err == nil {
		t.Fatal("accepted old certificate binding was not retired")
	}
	t.Log("PASS real Caddy TLS1.3/H2 coexistence, partial-load recovery, cold restart, synthetic subscription/REALITY, acceptance and replay; only temporary loopback processes used")
}
