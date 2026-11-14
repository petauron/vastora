//go:build linux

package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/nodesite"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPublic443SharedWebsiteLAB(t *testing.T) {
	if os.Getenv("VASTORA_PUBLIC443_LAB") != "1" {
		t.Skip("explicit isolated public443 lab required")
	}
	marker, e := os.ReadFile("/run/glivela-public443-lab.allowed")
	current, e2 := os.Readlink("/proc/self/ns/net")
	initial, e3 := os.Readlink("/proc/1/ns/net")
	if e != nil || string(marker) != "synthetic-no-nic-only\n" || e2 != nil || e3 != nil || current == initial || os.Geteuid() != 0 {
		t.Fatal("refusing public443 outside isolated no-NIC lab")
	}
	binary := os.Getenv("VASTORA_MIGRATION_CADDY_BINARY")
	raw, e := os.ReadFile(binary)
	sum := sha256.Sum256(raw)
	if e != nil || hex.EncodeToString(sum[:]) != os.Getenv("VASTORA_MIGRATION_CADDY_SHA256") {
		t.Fatal("Caddy digest mismatch")
	}
	for _, profile := range []string{"ipv6-only", "dual-stack"} {
		t.Run(profile, func(t *testing.T) { public443SharedWebsiteProfile(t, binary, profile) })
	}
}
func public443SharedWebsiteProfile(t *testing.T, binary, profile string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	ip := func(args ...string) {
		t.Helper()
		if e := exec.CommandContext(ctx, "ip", args...).Run(); e != nil {
			t.Fatal("synthetic interface setup", e)
		}
	}
	ip("link", "set", "lo", "up")
	for _, a := range []string{"1.1.1.1/32", "8.8.4.4/32", "9.9.9.9/32"} {
		ip("address", "replace", a, "dev", "lo")
	}
	ip("-6", "address", "replace", "2001:4860:4860::8888/128", "dev", "lo", "nodad")
	root, e := os.MkdirTemp("/tmp", "p443-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(root)
	stateDir := filepath.Join(root, "state")
	s, e := Open(stateDir)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { s.Close() }()
	key, private := nodeSiteTestKey(t)
	chain := nodeSiteTestChain(t, key, "site.example.test")
	certificate, e := nodeSiteCertificate("site.example.test", chain, private)
	if e != nil {
		t.Fatal(e)
	}
	public := "[2001:4860:4860::8888]:443"
	if profile == "dual-stack" {
		public = "1.1.1.1:443"
	}
	site := CaddySite{ApplicationID: "public443-owned-site", Hostname: "site.example.test", Address: "127.0.0.1", HTTPPort: 24481, TLSPort: 24483, PublicListen: public, HTML: "synthetic owned website", Certificate: certificate}
	task := nodesite.Task{ApplicationID: site.ApplicationID, AppKey: "vastora-official/daylight", Hostname: site.Hostname, Port: site.TLSPort, Revision: 1, Action: "apply", CertificatePEM: chain, PrivateKeyPEM: private}
	if e = s.saveCaddySite(ctx, caddySiteJournal{ApplicationID: site.ApplicationID, State: "applied", Site: &site}); e != nil {
		t.Fatal(e)
	}
	if e = s.saveNodeSiteJournal(ctx, nodeSiteJournal{Runtime: 2, Task: task, State: "applied"}); e != nil {
		t.Fatal(e)
	}
	driver, e := NewCaddyGatewayDriver("unix://" + filepath.Join(root, "caddy.sock"))
	if e != nil {
		t.Fatal(e)
	}
	base, _ := json.Marshal(map[string]any{"admin": map[string]any{"listen": driver.AdminListen}, "apps": map[string]any{"http": map[string]any{"servers": map[string]any{}}}})
	initial, e := composeCaddySites(base, gateway.DesiredState{}, []CaddySite{site}, false)
	if e != nil {
		t.Fatal(e)
	}
	config := filepath.Join(root, "initial.json")
	if e = os.WriteFile(config, initial, 0600); e != nil {
		t.Fatal(e)
	}
	var process *exec.Cmd
	stop := func() {
		if process != nil {
			_ = process.Process.Kill()
			_ = process.Wait()
			process = nil
		}
	}
	defer stop()
	start := func(resume bool) {
		args := []string{"run", "--config", config}
		if resume {
			args = append(args, "--resume")
		}
		process = exec.CommandContext(ctx, binary, args...)
		process.Env = append(os.Environ(), "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"))
		process.Stdout = io.Discard
		process.Stderr = io.Discard
		if e = process.Start(); e != nil {
			t.Fatal(e)
		}
		deadline := time.Now().Add(5 * time.Second)
		for probeCaddySite(ctx, site) != nil {
			if time.Now().After(deadline) {
				t.Fatal("Caddy not ready")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	attach := func() {
		s.sharedCaddy = driver
		s.reloadSharedCaddy = func(ctx context.Context) error {
			sites, e := s.SharedCaddySites(ctx)
			if e != nil {
				return e
			}
			raw, e := composeCaddySites(base, gateway.DesiredState{}, sites, false)
			if e != nil {
				return e
			}
			return driver.load(ctx, raw)
		}
	}
	start(false)
	attach()
	probePublic := func() {
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM([]byte(chain))
		transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: site.Hostname, MinVersion: tls.VersionTLS13}, DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", public)
		}}
		defer transport.CloseIdleConnections()
		response, e := (&http.Client{Timeout: 5 * time.Second, Transport: transport}).Get("https://" + site.Hostname + "/")
		if e != nil {
			t.Fatal("public website failed", e)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || response.ProtoMajor != 2 || string(body) != site.HTML {
			t.Fatal("public TLS/H2 content changed")
		}
	}
	probePublic()
	// Inject failure after a real Caddy reload. Only the exact old owned site is
	// restored; no database rewrite or generated-file hand edit is used.
	load := s.reloadSharedCaddy
	fail := true
	s.reloadSharedCaddy = func(ctx context.Context) error {
		if e := load(ctx); e != nil {
			return e
		}
		if fail {
			fail = false
			return errors.New("controlled post-load failure")
		}
		return nil
	}
	task.Revision = 2
	task.TLSLoopbackOnly = true
	if _, e = s.executeNodeSiteOnHost(ctx, task, nodeSiteHost{site: site}); e == nil {
		t.Fatal("partial handoff failure accepted")
	}
	probePublic()
	s.reloadSharedCaddy = load
	task.Revision = 3
	if _, e = s.executeNodeSiteOnHost(ctx, task, nodeSiteHost{site: site}); e != nil {
		t.Fatal("managed loopback handoff", e)
	}
	// Persisted mode and Caddy resume must keep443 released after a cold restart.
	stop()
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(stateDir)
	if e != nil {
		t.Fatal(e)
	}
	attach()
	start(true)
	state, e := s.readCaddySite(ctx, site.ApplicationID)
	if e != nil || !state.Site.TLSLoopbackOnly {
		t.Fatal("lost mode across restart", e)
	}
	ca := filepath.Join(root, "site-ca.pem")
	if e = os.WriteFile(ca, []byte(chain), 0600); e != nil {
		t.Fatal(e)
	}
	ready, resume := filepath.Join(root, "ready"), filepath.Join(root, "resume")
	command := exec.CommandContext(ctx, os.Getenv("VASTORA_PUBLIC443_GLIVELA_TEST"), "-test.run", "^TestPublic443CoreLAB$", "-test.v", "-test.timeout", "100s")
	dual := "0"
	if profile == "dual-stack" {
		dual = "1"
	}
	command.Env = append(os.Environ(), "GLIVELA_PUBLIC443_LAB=1", "GLIVELA_PUBLIC443_DUAL="+dual, "GLIVELA_PUBLIC443_CA="+ca, "GLIVELA_PUBLIC443_READY="+ready, "GLIVELA_PUBLIC443_RESUME="+resume)
	output := filepath.Join(root, "child.log")
	log, e := os.Create(output)
	if e != nil {
		t.Fatal(e)
	}
	defer log.Close()
	command.Stdout = log
	command.Stderr = log
	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() { _ = command.Process.Kill() }()
	deadline := time.Now().Add(25 * time.Second)
	for {
		if _, e = os.Stat(ready); e == nil {
			break
		}
		select {
		case e := <-done:
			b, _ := os.ReadFile(output)
			t.Fatalf("child proxy acceptance ended early: %v\n%s", e, b)
		default:
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(output)
			t.Fatalf("child readiness timeout\n%s", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e = s.syncSharedDaylightHost(ctx, AppliedInstallation{ApplicationID: site.ApplicationID}, nodeSiteHost{site: site}); e != nil {
		t.Fatal("content refresh reclaimed443", e)
	}
	if e = os.WriteFile(resume, []byte("continue"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		b, _ := os.ReadFile(output)
		t.Fatalf("real proxy acceptance: %v\n%s", e, b)
	}
	child, _ := os.ReadFile(output)
	t.Log(string(child))
	task.Revision = 4
	task.TLSLoopbackOnly = false
	if _, e = s.executeNodeSiteOnHost(ctx, task, nodeSiteHost{site: site}); e != nil {
		t.Fatal("restore public Caddy", e)
	}
	probePublic()
	t.Log("PASS owned website443 handoff, partial-load recovery, cold resume, live content refresh, real subscription/proxy child acceptance and forward public restore")
}
