//go:build linux

package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
)

// Explicit acceptance in an existing disposable Linux CT. All endpoints and
// synthetic subscription credentials are local. No production configuration is
// read, and downloaded binaries are never selected by this test.
func TestSharedCaddyRuntimeLab(t *testing.T) {
	for _, scenario := range []struct {
		name string
		fail bool
	}{{"rollback", true}, {"handover", false}} {
		t.Run(scenario.name, func(t *testing.T) { runSharedCaddyRuntimeLab(t, scenario.fail) })
	}
}

func runSharedCaddyRuntimeLab(t *testing.T, failHandover bool) {
	if os.Getenv("VASTORA_SHARED_CADDY_LAB") != "1" {
		t.Skip("requires an explicitly authorized disposable CT")
	}
	marker, err := os.ReadFile("/run/vastora-shared-caddy-lab.allowed")
	if err != nil || string(marker) != "disposable-test-only\n" || os.Geteuid() != 0 {
		t.Fatal("missing disposable test boundary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	root := t.TempDir()
	id := "shared-caddy-isolated-acceptance"
	for _, path := range []string{filepath.Dir(nativeCaddyBinary), nativeCaddyUnitPath, filepath.Dir(nativeCaddyBootstrap), "/var/lib/vastora-caddy", "/var/lib/private/vastora-caddy", filepath.Join("/opt/vastora/packages", packageIdentity(id))} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("refuse existing runtime resource: %s", path)
		}
	}
	var collection catalog.Catalog
	raw, err := os.ReadFile(os.Getenv("VASTORA_SHARED_CADDY_CATALOG"))
	if err != nil || json.Unmarshal(raw, &collection) != nil {
		t.Fatal("missing public recipe fixture")
	}
	var manifest catalog.AppManifest
	for _, app := range collection.Apps {
		if app.ID == "daylight" {
			manifest = app
		}
	}
	if manifest.Runtime == nil {
		t.Fatal("missing Daylight fixture")
	}
	manifest.Runtime.NativeServices.Namespace = "shared-caddy-lab"
	launcher, err := os.ReadFile(os.Getenv("VASTORA_SHARED_CADDY_DAYLIGHT"))
	if err != nil {
		t.Fatal(err)
	}
	core, err := os.ReadFile(os.Getenv("VASTORA_SHARED_CADDY_BINARY"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(os.Getenv("VASTORA_SHARED_CADDY_PAGE"))
	if err != nil {
		t.Fatal(err)
	}
	archive := func(content []byte, separate bool) []byte {
		var buf bytes.Buffer
		compressed := gzip.NewWriter(&buf)
		writer := tar.NewWriter(compressed)
		files := map[string][]byte{"daylight": launcher, "caddy": core, "LICENSE": []byte("isolated fixture"), "README.md": []byte("isolated fixture"), "CADDY-README.md": []byte("isolated fixture"), "examples/site.json": []byte("{}")}
		if separate {
			files["site/index.html"] = content
		}
		for path, data := range files {
			mode := int64(0644)
			if path == "daylight" || path == "caddy" {
				mode = 0755
			}
			if err := writer.WriteHeader(&tar.Header{Name: path, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	oldArchive, newArchive := archive(page, false), archive([]byte("<!doctype html><title>shared website</title>shared page"), true)
	artifacts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/new" {
			_, _ = w.Write(newArchive)
		} else {
			_, _ = w.Write(oldArchive)
		}
	}))
	defer artifacts.Close()
	manager := SystemdHostApplicationManager{HTTPClient: artifacts.Client()}
	s, err := Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	port := freeNodeSitePort(t)
	config, _ := json.Marshal(map[string]any{"hostname": "us1.example.com", "http_port": port})
	manifest.Artifacts = []catalog.Artifact{{Name: "caddy", OperatingSystem: "linux", Architecture: "amd64", URL: artifacts.URL + "/old", SHA256: sha256String(oldArchive), Format: "tar.gz"}}
	task := DeploymentTask{ID: "shared-lab-install", ApplicationID: id, AppKey: "vastora-official/daylight", Operation: "install", Config: config, Manifest: manifest, ServiceAddress: "127.0.0.1", AuthorizedCapabilities: []string{"native-services"}}
	packageTaskDigest(t, &task)
	plan, err := planNativePackage(id, task.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	unit := plan.Units[0]
	for _, path := range []string{unit.Path, filepath.Dir(nativeInstallationPath(id))} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("refuse existing website fixture resource: %s", path)
		}
	}
	for _, state := range plan.State {
		if _, err := os.Lstat(state.Path); !os.IsNotExist(err) {
			t.Fatal("website fixture state already exists")
		}
	}
	for _, name := range plan.Users {
		if _, exists, err := readNativeAccount(ctx, manager, name, id); err != nil || exists {
			t.Fatal("website fixture user is unavailable", err)
		}
	}
	// All cleanup targets were absent above or have this exact synthetic owner.
	t.Cleanup(func() {
		cleanup := context.Background()
		_ = manager.run(cleanup, "systemctl", "stop", unit.Name)
		_ = manager.run(cleanup, "systemctl", "disable", unit.Name)
		_ = manager.run(cleanup, "systemctl", "stop", "vastora-caddy.service")
		_ = manager.run(cleanup, "systemctl", "disable", "vastora-caddy.service")
		for _, path := range []string{unit.Path, filepath.Dir(nativeInstallationPath(id)), filepath.Join("/opt/vastora/packages", packageIdentity(id)), nativeCaddyUnitPath, filepath.Dir(nativeCaddyBootstrap), filepath.Dir(nativeCaddyBinary), "/var/lib/vastora-caddy", "/var/lib/private/vastora-caddy"} {
			_ = os.RemoveAll(path)
		}
		for _, state := range plan.State {
			_ = os.RemoveAll(state.Path)
		}
		for _, name := range plan.Users {
			if _, owned, err := readNativeAccount(cleanup, manager, name, id); err == nil && owned {
				_ = manager.run(cleanup, "userdel", name)
				_ = manager.run(cleanup, "groupdel", name)
			}
		}
		matches, _ := filepath.Glob(filepath.Join("/opt/vastora/packages", packageIdentity(id), "artifacts.shared-caddy-*"))
		for _, path := range matches {
			_ = os.RemoveAll(path)
		}
		_ = manager.run(cleanup, "systemctl", "daemon-reload")
	})
	oldBackend := &NativeServicesPackageBackend{Manager: manager, StateDirectory: s.dataDir}
	executor := PackageExecutor{StateDirectory: s.dataDir, Backend: oldBackend}
	if _, err := executor.Deploy(ctx, task); err != nil {
		t.Fatal("old native installation", err)
	}
	if _, err := s.RecordApplied(ctx, AppliedInstallation{InstanceID: task.ID, ApplicationID: id, AppKey: task.AppKey, Version: task.Manifest.Version, Config: config, Secrets: json.RawMessage(`{}`), Manifest: task.Manifest, ServiceAddress: task.ServiceAddress}); err != nil {
		t.Fatal(err)
	}
	if err := probeCaddySite(ctx, CaddySite{Hostname: "us1.example.com", Address: "127.0.0.1", HTTPPort: port, HTML: string(page)}); err != nil {
		t.Fatal("old website", err)
	}
	runtime := NativeCaddySiteRuntime{StateDirectory: s.dataDir}
	configure := func() {
		driver, err := NewCaddyGatewayDriver("unix://" + nativeCaddySocket)
		if err != nil {
			t.Fatal(err)
		}
		driver.SiteRuntime, driver.SiteIdle = runtime.ReconcileWebsites, runtime.Idle
		if err := s.ConfigureSharedCaddy(driver); err != nil {
			t.Fatal(err)
		}
	}
	configure()
	task.Operation, task.ID, task.Manifest.Version = "upgrade", "shared-lab-upgrade", "1.0.0"
	task.Manifest.Artifacts[0].URL, task.Manifest.Artifacts[0].SHA256 = artifacts.URL+"/new", sha256String(newArchive)
	task.Manifest.Runtime.RequiredCapabilities = []string{"native-services", "shared-caddy"}
	service := &task.Manifest.Runtime.NativeServices.Services[0]
	service.Start = "manual"
	versionArgument := json.RawMessage(`"version"`)
	service.Process.Arguments = []catalog.Value{{Literal: &versionArgument}}
	service.Process.Health = nil
	service.Process.Files = append(service.Process.Files, catalog.ArtifactFile{Path: "site/index.html"})
	task.AuthorizedCapabilities = []string{"native-services", "shared-caddy"}
	packageTaskDigest(t, &task)
	backend := &NativeServicesPackageBackend{Store: s, Manager: manager, StateDirectory: s.dataDir}
	executor.Backend = backend
	if failHandover {
		normal := s.sharedCaddy.SiteRuntime
		injected := false
		s.sharedCaddy.SiteRuntime = func(ctx context.Context, state gateway.DesiredState, sites []CaddySite) error {
			if !injected && len(sites) > 0 {
				injected = true
				return fmt.Errorf("controlled fixture runtime failure")
			}
			return normal(ctx, state, sites)
		}
	}
	_, deployErr := executor.Deploy(ctx, task)
	if failHandover {
		if deployErr == nil {
			t.Fatal("controlled failure was accepted")
		}
		if err := probeCaddySite(ctx, CaddySite{Hostname: "us1.example.com", Address: "127.0.0.1", HTTPPort: port, HTML: string(page)}); err != nil {
			t.Fatal("failed handover did not restore the real old website", err, deployErr)
		}
		receipt, err := executor.ReadResources(id)
		if err != nil || receipt.State != "review-required" || receipt.PackageVersion != manifest.Version || receipt.TaskID != task.ID {
			t.Fatal("failed handover lost the source receipt or review fence", err)
		}
		if err := inspectNativeOwnership(ctx, manager, id, plan, receipt); err != nil {
			t.Fatal("old program or unit ownership was not restored", err)
		}
		state, err := s.readCaddySite(ctx, id)
		if err != nil || state.Site != nil || state.Handover == nil || state.Handover.State != "rolled_back" {
			t.Fatal("failed handover was not durably rolled back", err)
		}
		// Reproduce an already-rolled-back receipt from the affected release.
		// Startup recovery repairs only the exact journal-owned task identity.
		receipt.TaskID = state.Handover.BeforeReceipt.TaskID
		if err := executor.save(receipt); err != nil {
			t.Fatal(err)
		}
		if err := s.recoverDaylightHandovers(ctx); err != nil {
			t.Fatal("recover rolled-back review ownership", err)
		}
		receipt, err = executor.ReadResources(id)
		if err != nil || receipt.TaskID != task.ID || receipt.State != "review-required" {
			t.Fatal("rollback recovery lost the review fence", err)
		}
		if _, err := executor.Deploy(ctx, task); err == nil {
			t.Fatal("unreviewed retry was accepted")
		}
		task.Kind, task.Attempt = "application.apply", 2
		task.Authorization = controlplane.ExecutionAuthorization{ID: "new-lab-execution", Protocol: controlplane.ExecutionProtocol, Digest: strings.Repeat("a", 64)}
		task.PackageRecovery = &controlplane.PackageRecovery{ExecutionID: "reviewed-lab-execution", TaskID: task.ID, Attempt: 1}
		if _, err := executor.Deploy(ctx, task); err != nil {
			t.Fatal("operator-reviewed retry of rolled-back handover", err)
		}
		if err := probeCaddySite(ctx, CaddySite{Hostname: "us1.example.com", Address: "127.0.0.1", HTTPPort: port, HTML: "<!doctype html><title>shared website</title>shared page"}); err != nil {
			t.Fatal("reviewed retry website", err)
		}
		t.Log("PASS failure restores the old website and ownership; startup retains the review fence; only an explicitly reviewed retry completes the handover")
		return
	}
	if deployErr != nil {
		t.Fatal("shared content handover", deployErr)
	}
	if _, err := s.RecordApplied(ctx, AppliedInstallation{InstanceID: task.ID, ApplicationID: id, AppKey: task.AppKey, Version: task.Manifest.Version, Config: config, Secrets: json.RawMessage(`{}`), Manifest: task.Manifest, ServiceAddress: task.ServiceAddress}); err != nil {
		t.Fatal(err)
	}
	if err := s.completeSharedDaylight(ctx, task); err != nil {
		t.Fatal(err)
	}
	certificateTask := nodeSiteTestTask()
	certificateTask.ApplicationID = id
	certificateTask.Port = freeNodeSitePort(t)
	key, private := nodeSiteTestKey(t)
	certificateTask.CertificatePEM = nodeSiteTestChain(t, key, certificateTask.Hostname)
	certificateTask.PrivateKeyPEM = private
	if _, err := s.executeNodeSite(ctx, certificateTask); err != nil {
		t.Fatal("real certificate activation", err)
	}
	first, err := s.readCaddySite(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	other := *first.Site
	other.ApplicationID, other.Hostname, other.HTTPPort, other.TLSPort, other.HTML = "shared-other", "hk1.example.com", freeNodeSitePort(t), freeNodeSitePort(t), "second site"
	otherKey, otherPrivate := nodeSiteTestKey(t)
	other.Certificate, err = nodeSiteCertificate(other.Hostname, nodeSiteTestChain(t, otherKey, other.Hostname), otherPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.updateCaddySite(ctx, other.ApplicationID, &other, func(ctx context.Context) error { return probeCaddySite(ctx, other) }); err != nil {
		t.Fatal("second shared website", err)
	}
	before, err := manager.readPackageUnit(ctx, "vastora-caddy.service")
	if err != nil || before.MainPID == 0 {
		t.Fatal("shared process missing", err)
	}
	certificateTask.Revision++
	renewedKey, renewedPrivate := nodeSiteTestKey(t)
	certificateTask.CertificatePEM, certificateTask.PrivateKeyPEM = nodeSiteTestChain(t, renewedKey, certificateTask.Hostname), renewedPrivate
	if _, err := s.executeNodeSite(ctx, certificateTask); err != nil {
		t.Fatal("real certificate renewal", err)
	}
	first, err = s.readCaddySite(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bad := *first.Site
	bad.TLSPort = blocked.Addr().(*net.TCPAddr).Port
	if err := s.updateCaddySite(ctx, id, &bad, nil); err == nil {
		t.Fatal("conflicting listener accepted")
	}
	_ = blocked.Close()
	for _, site := range []CaddySite{*first.Site, other} {
		if err := probeCaddySite(ctx, site); err != nil {
			t.Fatal("failed update damaged an existing site", err)
		}
	}
	after, err := manager.readPackageUnit(ctx, "vastora-caddy.service")
	if err != nil || after.MainPID != before.MainPID {
		t.Fatal("configuration load restarted shared Caddy", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.run(ctx, "systemctl", "restart", "vastora-caddy.service"); err != nil {
		t.Fatal(err)
	}
	if err := waitForCaddyAdminSocket(ctx, nativeCaddySocket); err != nil {
		t.Fatal(err)
	}
	s, err = Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	configure()
	if err := s.recoverNodeSites(ctx); err != nil {
		t.Fatal(err)
	}
	for _, site := range []CaddySite{*first.Site, other} {
		if err := probeCaddySite(ctx, site); err != nil {
			t.Fatal("resume lost a website", err)
		}
	}
	t.Run("subscription-reality", func(t *testing.T) { sharedCaddyRealityLab(t, ctx, root, *first.Site) })
	if err := s.updateCaddySite(ctx, other.ApplicationID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := probeCaddySite(ctx, *first.Site); err != nil {
		t.Fatal("removing another website damaged the first", err)
	}
	if err := s.removeSharedDaylight(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	t.Log("PASS real standalone handover, shared TLS/H2, failed-load recovery, PID reuse, restart persistence, synthetic subscription/REALITY and site removal")
}

func sharedCaddyRealityLab(t *testing.T, ctx context.Context, root string, site CaddySite) {
	t.Helper()
	binary := os.Getenv("VASTORA_SHARED_CADDY_XRAY")
	keys, err := exec.CommandContext(ctx, binary, "x25519").Output()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(keys), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			values[k] = strings.TrimSpace(v)
		}
	}
	public := values["Password (PublicKey)"]
	if public == "" {
		public = values["Password"]
	}
	if public == "" {
		public = values["PublicKey"]
	}
	if public == "" || values["PrivateKey"] == "" {
		t.Fatal("official core key format unavailable")
	}
	entry, socks := freeNodeSitePort(t), freeNodeSitePort(t)
	uuid := "8ee5cd30-2ddf-4b42-b92b-6bebf70316a2"
	link := fmt.Sprintf("vless://%s@127.0.0.1:%d?security=reality&sni=%s&pbk=%s&sid=aabbccdd&flow=xtls-rprx-vision#isolated-site", uuid, entry, site.Hostname, url.QueryEscape(public))
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, link+"\n") }))
	defer subscription.Close()
	response, err := http.Get(subscription.URL)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	node, err := url.Parse(strings.TrimSpace(string(raw)))
	if err != nil || node.Scheme != "vless" || node.User.Username() != uuid {
		t.Fatal("synthetic subscription lost identity")
	}
	server := map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": entry, "protocol": "vless", "settings": map[string]any{"clients": []any{map[string]any{"id": uuid, "flow": "xtls-rprx-vision"}}, "decryption": "none"}, "streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"target": fmt.Sprintf("127.0.0.1:%d", site.TLSPort), "serverNames": []string{site.Hostname}, "privateKey": values["PrivateKey"], "shortIds": []string{"aabbccdd"}}}}}, "outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "network": "tcp", "ip": []string{"127.0.0.1/32"}, "port": fmt.Sprint(site.TLSPort)}}}}}}
	client := map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": socks, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": node.Hostname(), "port": entry, "users": []any{map[string]any{"id": node.User.Username(), "flow": node.Query().Get("flow"), "encryption": "none"}}}}}, "streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"serverName": node.Query().Get("sni"), "fingerprint": "chrome", "publicKey": node.Query().Get("pbk"), "shortId": node.Query().Get("sid")}}}}}
	for _, cfg := range []map[string]any{server, client} {
		raw, _ := json.Marshal(cfg)
		command := exec.CommandContext(ctx, binary, "run", "-config", "stdin:")
		command.Stdin = bytes.NewReader(raw)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	}
	certificate := filepath.Join(root, "synthetic.pem")
	if err := os.WriteFile(certificate, []byte(site.Certificate.CertificatePEM), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		command := exec.CommandContext(ctx, "curl", "--silent", "--show-error", "--max-time", "8", "--cacert", certificate, "--resolve", fmt.Sprintf("%s:%d:127.0.0.1", site.Hostname, site.TLSPort), "--noproxy", "", "--socks5", fmt.Sprintf("127.0.0.1:%d", socks), fmt.Sprintf("https://%s:%d/", site.Hostname, site.TLSPort))
		body, err := command.Output()
		if err == nil && string(body) == site.HTML {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("synthetic subscription REALITY HTTPS request failed")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestSharedCaddyDockerLab(t *testing.T) {
	if os.Getenv("VASTORA_SHARED_CADDY_LAB") != "1" {
		t.Skip("requires an explicitly authorized disposable CT")
	}
	marker, err := os.ReadFile("/run/vastora-shared-caddy-lab.allowed")
	if err != nil || string(marker) != "disposable-test-only\n" || os.Geteuid() != 0 {
		t.Fatal("missing disposable test boundary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	root := t.TempDir()
	name := "shared-caddy-lab-" + packageIdentity(filepath.Base(root))
	command := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	}
	if _, err := command("inspect", name); err == nil {
		t.Fatal("refuse existing fixture container")
	}
	_, networkBeforeErr := command("network", "inspect", dockerruntime.NetworkName, "--format", "{{.ID}}")
	createdNetworkID := ""
	s, err := Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	driver, err := NewCaddyGatewayDriver("unix://" + filepath.Join(root, "admin", "admin.sock"))
	if err != nil {
		t.Fatal(err)
	}
	provisioner := DockerGatewayProvisioner{Image: defaultCaddyImage, Container: name, DataVolume: name + "-data", ConfigVolume: name + "-config", AdminSocketPath: driver.AdminSocketPath, AdminListen: driver.AdminListen, Sites: s.SharedCaddySites}
	driver.SiteRuntime, driver.SiteContainer, driver.SystemGateway = provisioner.ReconcileWebsites, true, provisioner
	if err := s.ConfigureSharedCaddy(driver); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		_ = exec.Command("docker", "volume", "rm", name+"-data", name+"-config").Run()
		if networkBeforeErr != nil && createdNetworkID != "" {
			// Docker refuses network removal if another workload attached meanwhile.
			_ = exec.Command("docker", "network", "rm", createdNetworkID).Run()
		}
	})
	docker, err := provisioner.client()
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
		t.Fatal(err)
	}
	if networkBeforeErr != nil {
		id, err := command("network", "inspect", dockerruntime.NetworkName, "--format", "{{.ID}}")
		if err != nil {
			t.Fatal(err)
		}
		createdNetworkID = strings.TrimSpace(string(id))
	}
	gatewayIP, err := command("network", "inspect", dockerruntime.NetworkName, "--format", "{{(index .IPAM.Config 0).Gateway}}")
	if err != nil {
		t.Fatal("bridge inspection failed")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(strings.TrimSpace(string(gatewayIP)), "0"))
	if err != nil {
		t.Fatal(err)
	}
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "unrelated gateway app") })}
	go upstream.Serve(listener)
	defer upstream.Close()
	httpPort, httpsPort := freeNodeSitePort(t), freeNodeSitePort(t)
	desired := gateway.DesiredState{Revision: 1, Listeners: []gateway.Listener{{Kind: "lan", Address: "127.0.0.1", HTTPPort: httpPort, HTTPSPort: httpsPort}}, Routes: []gateway.Route{{ID: "fixture-app", Hostname: "control.example.com", ListenerKind: "lan", Protocol: "http", Upstreams: []gateway.Upstream{{Address: strings.TrimSpace(string(gatewayIP)), Port: listener.Addr().(*net.TCPAddr).Port}}}}}
	if err := provisioner.Reconcile(ctx, desired); err != nil {
		t.Fatal("gateway runtime", err)
	}
	if err := driver.ApplyConfiguration(ctx, desired, nil); err != nil {
		t.Fatal(err)
	}
	key, private := nodeSiteTestKey(t)
	certificate, err := nodeSiteCertificate("us1.example.com", nodeSiteTestChain(t, key, "us1.example.com"), private)
	if err != nil {
		t.Fatal(err)
	}
	site := CaddySite{ApplicationID: "docker-site", Hostname: "us1.example.com", Address: "127.0.0.1", HTTPPort: freeNodeSitePort(t), TLSPort: freeNodeSitePort(t), HTML: "docker shared site", Certificate: certificate}
	verify := func(stage string) {
		if err := probeCaddySite(ctx, site); err != nil {
			t.Fatal(stage, "website", err)
		}
		if err := probeCaddySite(ctx, CaddySite{Hostname: "control.example.com", Address: "127.0.0.1", HTTPPort: httpPort, HTML: "unrelated gateway app"}); err != nil {
			t.Fatal(stage, "gateway app", err)
		}
	}
	if err := s.updateCaddySite(ctx, site.ApplicationID, &site, func(ctx context.Context) error { return probeCaddySite(ctx, site) }); err != nil {
		t.Fatal("shared website", err)
	}
	verify("initial shared load")
	before, err := command("inspect", name, "--format", "{{.ID}}")
	if err != nil {
		t.Fatal(err)
	}
	site.HTML = "updated shared site"
	if err := s.updateCaddySite(ctx, site.ApplicationID, &site, func(ctx context.Context) error { return probeCaddySite(ctx, site) }); err != nil {
		t.Fatal(err)
	}
	after, err := command("inspect", name, "--format", "{{.ID}}")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("content refresh replaced gateway")
	}
	verify("content reload")
	if _, err := command("restart", name); err != nil {
		t.Fatal("fixture restart failed")
	}
	if err := waitForCaddyAdminSocket(ctx, driver.AdminSocketPath); err != nil {
		t.Fatal(err)
	}
	// Docker restart returns before Caddy has resumed its HTTP listeners. A
	// stale Unix socket pathname is not a readiness signal for those listeners.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := probeCaddySite(ctx, site); err == nil {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := command("logs", "--tail", "12", name)
			t.Fatalf("resume did not restore website: %s", logs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	verify("container resume")
	if err := provisioner.Remove(ctx); err == nil {
		t.Fatal("shared gateway removal accepted while website exists")
	}
	if err := s.updateCaddySite(ctx, site.ApplicationID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := probeCaddySite(ctx, CaddySite{Hostname: "control.example.com", Address: "127.0.0.1", HTTPPort: httpPort, HTML: "unrelated gateway app"}); err != nil {
		t.Fatal("website removal damaged gateway", err)
	}
	t.Log("PASS actual Docker shared configuration, unrelated gateway route, content refresh identity, restart persistence and dependency/removal protection")
}
