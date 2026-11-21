//go:build integration && linux

package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/dockerruntime"
)

const cpaLabRoot = "/var/lib/vastora-cpa-acceptance"
const cpaLabAddress = "10.77.0.1"

type cpaLabState struct {
	Key    string `json:"key"`
	CPA    string `json:"cpa"`
	Keeper string `json:"keeper"`
	Tunnel string `json:"tunnel"`
}

// This is separate from the existing GitHub-hosted integration gate. It can
// only run as mapped root in an explicitly marked disposable unprivileged LXC.
// It never selects a Docker context or reads a production credential.
func cpaLXC(t *testing.T) (*client.Client, context.Context) {
	t.Helper()
	if os.Getenv("VASTORA_CPA_LXC_ACCEPTANCE") != "1" {
		t.Skip("requires explicitly authorized disposable CPA LXC")
	}
	kind, _ := os.ReadFile("/run/systemd/container")
	mapping, _ := os.ReadFile("/proc/self/uid_map")
	fields := strings.Fields(string(mapping))
	marker, err := os.Lstat(filepath.Join(cpaLabRoot, "authorized"))
	if err != nil || !marker.Mode().IsRegular() || marker.Mode().Perm() != 0600 || os.Geteuid() != 0 || strings.TrimSpace(string(kind)) != "lxc" || len(fields) < 3 || fields[0] != "0" || fields[1] == "0" {
		t.Fatal("not the authorized disposable unprivileged LXC")
	}
	if owner, ok := marker.Sys().(*syscall.Stat_t); !ok || owner.Uid != 0 {
		t.Fatal("unowned acceptance marker")
	}
	intent, err := os.ReadFile(filepath.Join(cpaLabRoot, "authorized"))
	if err != nil || strings.TrimSpace(string(intent)) != "issue-919-synthetic-only" {
		t.Fatal("invalid acceptance marker")
	}
	d, err := client.New(client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		t.Fatal("local Docker unavailable")
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	return d, ctx
}

func cpaLabCatalog(t *testing.T) map[string]catalog.AppManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cpaLabRoot, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := catalog.ParseCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]catalog.AppManifest{}
	for _, app := range c.Apps {
		if app.ID == "cpa" || app.ID == "keeper" {
			out[app.ID] = app
		}
	}
	if out["cpa"].PackageRevision != 3 || out["keeper"].PackageRevision != 2 {
		t.Fatal("acceptance requires the exact new CPA/Keeper recipes")
	}
	return out
}

func cpaLabTask(t *testing.T, app catalog.AppManifest, key string) DeploymentTask {
	t.Helper()
	config := map[string]any{"timezone": "UTC"}
	secrets := map[string]string{}
	if app.ID == "cpa" {
		config["debug"] = false
		secrets["management_key"] = key
		secrets["api_key"] = key
	} else {
		config["cpa_base_url"] = "http://" + cpaLabAddress + ":8317"
		secrets["cpa_management_key"] = key
		secrets["login_password"] = key
	}
	raw, _ := json.Marshal(config)
	sealed, _ := json.Marshal(secrets)
	task := DeploymentTask{ID: "lab-install-" + app.ID, ApplicationID: "lab-919-" + app.ID, AppKey: "vastora-official/" + app.ID, Operation: "install", ServiceAddress: cpaLabAddress, Manifest: app, Config: raw, Secrets: sealed, AuthorizedCapabilities: slices.Clone(app.Runtime.RequiredCapabilities)}
	packageTaskDigest(t, &task)
	return task
}

// The official cloudflared image runs only its local Access TCP listener. No
// Cloudflare token or remote tunnel is created. Its network namespace supplies
// synthetic HTTP requests via nsenter, not a claimed Cloudflare wire path.
func cpaLabConnector(t *testing.T, ctx context.Context, d *client.Client) string {
	t.Helper()
	if err := dockerruntime.EnsureNetwork(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := dockerruntime.EnsureBridgeNetwork(ctx, d, tunnelOriginNetwork, tunnelOriginNetworkComponent); err != nil {
		t.Fatal(err)
	}
	n, err := d.NetworkInspect(ctx, tunnelOriginNetwork, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ip, err := tunnelStaticAddress(n.Network)
	if err != nil {
		t.Fatal(err)
	}
	created, err := d.ContainerCreate(ctx, client.ContainerCreateOptions{Name: defaultCloudflaredContainer, Config: &container.Config{Image: deployapi.CloudflaredImage, Cmd: []string{"access", "tcp", "--hostname", "fixture.invalid", "--url", "127.0.0.1:18083"}, Labels: map[string]string{dockerruntime.ManagedLabel: "true", dockerruntime.ComponentLabel: "cloudflare-tunnel"}}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(dockerruntime.NetworkName), RestartPolicy: container.RestartPolicy{Name: "unless-stopped"}}, NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{dockerruntime.NetworkName: {Aliases: []string{dockerruntime.CloudflaredAlias}}, tunnelOriginNetwork: {GwPriority: 1, IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: ip}}}}})
	if err != nil {
		t.Fatal("local simulated connector creation failed")
	}
	if _, err = d.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal("local simulated connector failed to start")
	}
	time.Sleep(time.Second)
	identity, err := (cpaProxyManager{Docker: d, StateDirectory: cpaLabRoot}).tunnel(ctx)
	if err != nil || identity == nil || identity.ContainerID != created.ID {
		t.Fatal("local simulated connector did not remain running with verified ownership")
	}
	return created.ID
}

func cpaLabRequest(t *testing.T, ctx context.Context, d *client.Client, id, key string, headers ...string) int {
	t.Helper()
	x, err := d.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil || x.Container.State == nil || !x.Container.State.Running {
		t.Fatal("request source namespace unavailable")
	}
	args := []string{"-t", strconv.Itoa(x.Container.State.Pid), "-n", "curl", "--noproxy", "*", "-sS", "--max-time", "8", "-o", "/dev/null", "-w", "%{http_code}", "-H", "Authorization: Bearer " + key}
	for _, h := range headers {
		args = append(args, "-H", h)
	}
	args = append(args, "http://"+cpaLabAddress+":8317/v0/management/config")
	cmd := exec.CommandContext(ctx, "nsenter", args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("synthetic namespace request failed")
	}
	status, err := strconv.Atoi(string(out))
	if err != nil {
		t.Fatal("invalid HTTP status")
	}
	return status
}

func cpaLabReset(t *testing.T, ctx context.Context, d *client.Client) {
	t.Helper()
	if err := (cpaProxyManager{Docker: d, StateDirectory: cpaLabRoot}).all(ctx, true); err != nil {
		t.Fatal(err)
	}
}

func cpaLabHealthy(t *testing.T, ctx context.Context, d *client.Client, state cpaLabState) {
	t.Helper()
	identity, err := (cpaProxyManager{Docker: d, StateDirectory: cpaLabRoot}).tunnel(ctx)
	if err != nil || identity == nil || identity.ContainerID != state.Tunnel {
		t.Fatal("persisted connector identity is not running")
	}
	if status := cpaLabRequest(t, ctx, d, state.Tunnel, state.Key, "X-Forwarded-For: 203.0.113.20"); status != http.StatusOK {
		t.Fatalf("trusted client status=%d", status)
	}
	if status := cpaLabRequest(t, ctx, d, state.Keeper, state.Key); status != http.StatusOK {
		t.Fatalf("Keeper private path status=%d", status)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+cpaLabAddress+":8080/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{}}).Do(request)
	if err != nil {
		t.Fatal("Keeper endpoint unavailable")
	}
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		t.Fatalf("Keeper status=%d", response.StatusCode)
	}
	if status := cpaLabRequest(t, ctx, d, state.Tunnel, state.Key, "X-Forwarded-For: 2001:db8::20"); status != http.StatusOK {
		t.Fatalf("forwarded IPv6 client status=%d", status)
	}
}

func cpaLabKeeperDataUpgrade(t *testing.T, ctx context.Context, d *client.Client, state *cpaLabState, app catalog.AppManifest) {
	t.Helper()
	outside := filepath.Join(cpaLabRoot, "outside-control")
	f, err := os.OpenFile(outside, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("outside-data-must-not-change")
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal("outside control creation failed")
	}
	control, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	// Write through the real configured app UID, not Docker's root copy API.
	cmd := exec.CommandContext(ctx, "docker", "--host", "unix:///var/run/docker.sock", "exec", state.Keeper, "/bin/sh", "-c", "umask 077; printf 'before-upgrade\\n' > /data/upgrade-proof.txt")
	if err := cmd.Run(); err != nil {
		t.Fatal("non-root Keeper cannot write its existing data volume")
	}
	// An unrelated legacy owner must not be seized by a recursive ownership fix.
	if _, err := d.CopyToContainer(ctx, state.Keeper, client.CopyToContainerOptions{DestinationPath: "/data", Content: bytes.NewReader(configurationTar("other-owner.txt", []byte("preserve-other-owner"), tar.TypeReg))}); err != nil {
		t.Fatal(err)
	}
	task := cpaLabTask(t, app, state.Key)
	task.Operation = "upgrade"
	task.ID = "lab-keeper-data-upgrade"
	task.Manifest.PackageRevision++
	packageTaskDigest(t, &task)
	r, err := (PackageExecutor{StateDirectory: cpaLabRoot, Backend: &DockerPackageBackend{Docker: d, StateDirectory: cpaLabRoot}}).Deploy(ctx, task)
	if err != nil {
		t.Fatal("Keeper existing-data upgrade failed", err)
	}
	state.Keeper = resourceNamed(r.Resources, "container", "keeper").ID
	x, err := d.ContainerInspect(ctx, state.Keeper, client.ContainerInspectOptions{})
	if err != nil || x.Container.Config.User != "100:101" || x.Container.HostConfig.Privileged || !slices.Contains(x.Container.HostConfig.CapDrop, "ALL") {
		t.Fatal("Keeper privilege boundary changed")
	}
	cmd = exec.CommandContext(ctx, "docker", "--host", "unix:///var/run/docker.sock", "exec", state.Keeper, "/bin/sh", "-c", "printf 'after-upgrade\\n' >> /data/upgrade-proof.txt")
	if err := cmd.Run(); err != nil {
		t.Fatal("upgraded Keeper cannot append to existing app-owned data")
	}
	for _, proof := range []struct {
		name, content string
		uid, gid      int
	}{{"upgrade-proof.txt", "before-upgrade\nafter-upgrade\n", 100, 101}, {"other-owner.txt", "preserve-other-owner", 65532, 65532}} {
		copied, err := d.CopyFromContainer(ctx, state.Keeper, client.CopyFromContainerOptions{SourcePath: "/data/" + proof.name})
		if err != nil {
			t.Fatal(err)
		}
		data, header, err := readSingleConfiguration(copied.Content, proof.name)
		_ = copied.Content.Close()
		if err != nil || string(data) != proof.content || header.Uid != proof.uid || header.Gid != proof.gid {
			t.Fatal("upgrade overwrote existing content or changed its owner")
		}
	}
	cmd = exec.CommandContext(ctx, "docker", "--host", "unix:///var/run/docker.sock", "exec", state.Keeper, "/bin/sh", "-c", "printf denied >> /data/other-owner.txt")
	if err := cmd.Run(); err == nil {
		t.Fatal("Keeper unexpectedly gained write access to unrelated ownership")
	}
	unchanged, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outside)
	oldOwner := control.Sys().(*syscall.Stat_t)
	newOwner := unchanged.Sys().(*syscall.Stat_t)
	if err != nil || string(data) != "outside-data-must-not-change" || unchanged.Mode() != control.Mode() || oldOwner.Uid != newOwner.Uid || oldOwner.Gid != newOwner.Gid || oldOwner.Ctim != newOwner.Ctim {
		t.Fatal("upgrade changed a path outside the data mount")
	}
	cpaLabHealthy(t, ctx, d, *state)
	t.Log("existing app-owned data remains readable/writable; foreign owner and outside path unchanged; non-root/CapDrop=ALL preserved")
}

func TestCPALXCExercise(t *testing.T) {
	d, ctx := cpaLXC(t)
	listed, err := d.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil || len(listed.Items) != 0 {
		t.Fatal("acceptance requires an empty disposable Docker daemon")
	}
	apps := cpaLabCatalog(t)
	for _, ref := range []string{apps["cpa"].Images[0].Reference, apps["keeper"].Images[0].Reference, deployapi.CloudflaredImage} {
		pull, err := d.ImagePull(ctx, ref, client.ImagePullOptions{})
		if err != nil {
			t.Fatal("fixed test image pull failed")
		}
		err = pull.Wait(ctx)
		_ = pull.Close()
		if err != nil {
			t.Fatal("fixed test image pull incomplete")
		}
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	state := cpaLabState{Key: hex.EncodeToString(random[:]), Tunnel: cpaLabConnector(t, ctx, d)}
	var cpaTask DeploymentTask
	for _, name := range []string{"cpa", "keeper"} {
		task := cpaLabTask(t, apps[name], state.Key)
		backend := &DockerPackageBackend{Docker: d, StateDirectory: cpaLabRoot}
		result, err := (PackageExecutor{StateDirectory: cpaLabRoot, Backend: backend}).Deploy(ctx, task)
		if err != nil {
			t.Fatalf("real %s install failed: %v", name, err)
		}
		r := resourceNamed(result.Resources, "container", name)
		if r == nil {
			t.Fatal("real container receipt missing")
		}
		if name == "cpa" {
			state.CPA = r.ID
			cpaTask = task
		} else {
			state.Keeper = r.ID
		}
	}
	cpaLabHealthy(t, ctx, d, state)
	t.Log("real Docker CPA/Keeper installation and private namespace path passed")
	cpaLabKeeperDataUpgrade(t, ctx, d, &state, apps["keeper"])
	for _, clientIP := range []string{"203.0.113.10", "2001:db8::10"} {
		for range 5 {
			status := cpaLabRequest(t, ctx, d, state.Tunnel, "incorrect-lab-key", "X-Forwarded-For: "+clientIP)
			if status != http.StatusUnauthorized {
				t.Fatalf("bad client status=%d", status)
			}
		}
		if status := cpaLabRequest(t, ctx, d, state.Tunnel, state.Key, "X-Forwarded-For: "+clientIP); status != http.StatusForbidden {
			t.Fatalf("banned client status=%d", status)
		}
		if status := cpaLabRequest(t, ctx, d, state.Tunnel, state.Key, "X-Forwarded-For: 203.0.113.20"); status != http.StatusOK {
			t.Fatalf("independent client status=%d", status)
		}
	}
	t.Log("two synthetic client identities including IPv6 headers have isolated bans")
	for range 5 {
		_ = cpaLabRequest(t, ctx, d, state.Keeper, "incorrect-lab-key", "X-Forwarded-For: 203.0.113.30")
	}
	for _, h := range []string{"X-Forwarded-For: 203.0.113.20", "X-Real-IP: 203.0.113.20", "CF-Connecting-IP: 203.0.113.20", "X-Forwarded-For: 203.0.113.10, 203.0.113.20"} {
		if status := cpaLabRequest(t, ctx, d, state.Keeper, state.Key, h); status != http.StatusForbidden {
			t.Fatalf("untrusted spoof status=%d", status)
		}
	}
	if status := cpaLabRequest(t, ctx, d, state.Tunnel, state.Key, "X-Forwarded-For: 203.0.113.20"); status != http.StatusOK {
		t.Fatal("untrusted source poisoned trusted client")
	}
	t.Log("non-trusted source cannot bypass a ban with forwarding headers")
	cpaLabReset(t, ctx, d)
	// A surviving legacy Keeper DNS origin must block CPA before any stop.
	keeperInfo, err := d.ContainerInspect(ctx, state.Keeper, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	legacy := *keeperInfo.Container.Config
	legacy.Labels = applicationResourceLabels(keeperKey, "keeper", "lab-legacy-keeper", "fixture")
	legacy.Env = slices.DeleteFunc(slices.Clone(legacy.Env), func(s string) bool { return strings.HasPrefix(s, "CPA_BASE_URL=") })
	legacy.Env = append(legacy.Env, "CPA_BASE_URL=http://cpa:8317")
	oldKeeper, err := d.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "lab-legacy-keeper", Config: &legacy, HostConfig: &container.HostConfig{NetworkMode: "bridge"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := d.ContainerInspect(ctx, state.CPA, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&DockerPackageBackend{Docker: d, StateDirectory: cpaLabRoot}).Prepare(ctx, cpaTask, &InstanceResources{ApplicationID: cpaTask.ApplicationID}); err == nil {
		t.Fatal("legacy Keeper origin did not block CPA migration")
	}
	after, err := d.ContainerInspect(ctx, state.CPA, client.ContainerInspectOptions{})
	if err != nil || after.Container.State.StartedAt != before.Container.State.StartedAt {
		t.Fatal("preflight changed CPA process")
	}
	if _, err := d.ContainerRemove(ctx, oldKeeper.ID, client.ContainerRemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Log("legacy Keeper origin blocks migration before runtime mutation")
	// Run the actual no-token stopped-state path. It must revoke before removal.
	if err := (DockerTunnelProvisioner{StateDirectory: cpaLabRoot}).apply(ctx, TunnelDesiredState{Revision: 2, Status: "stopped"}, d, defaultCloudflaredContainer); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ContainerInspect(ctx, state.Tunnel, client.ContainerInspectOptions{}); err == nil {
		t.Fatal("old connector remains")
	}
	n, err := d.NetworkInspect(ctx, tunnelOriginNetwork, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	oldIP, err := tunnelStaticAddress(n.Network)
	if err != nil {
		t.Fatal(err)
	}
	foreignCfg := *keeperInfo.Container.Config
	foreignCfg.Labels = map[string]string{"lab.fixture": "old-address-reuse"}
	foreign, err := d.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "lab-old-address-peer", Config: &foreignCfg, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(tunnelOriginNetwork)}, NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{tunnelOriginNetwork: {IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: oldIP}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ContainerStart(ctx, foreign.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		_ = cpaLabRequest(t, ctx, d, foreign.ID, "incorrect-lab-key", "X-Forwarded-For: 203.0.113.30")
	}
	if status := cpaLabRequest(t, ctx, d, foreign.ID, state.Key, "X-Forwarded-For: 203.0.113.20"); status != http.StatusForbidden {
		t.Fatal("reused old IP retained forwarded-header authority")
	}
	if err := (DockerTunnelProvisioner{StateDirectory: cpaLabRoot}).apply(ctx, TunnelDesiredState{Revision: 3, Status: "stopped"}, d, defaultCloudflaredContainer); err == nil {
		t.Fatal("foreign endpoint was adopted or removed")
	}
	if _, err := d.ContainerInspect(ctx, foreign.ID, client.ContainerInspectOptions{}); err != nil {
		t.Fatal("foreign endpoint was modified")
	}
	if _, err := d.ContainerRemove(ctx, foreign.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	state.Tunnel = cpaLabConnector(t, ctx, d)
	cpaLabReset(t, ctx, d)
	cpaLabHealthy(t, ctx, d, state)
	t.Log("real stopped-state revocation, old-IP reuse rejection and new identity binding passed")
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cpaLabRoot, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("restart fixture retained privately; Cloudflare wire path NOT tested")
}

func TestCPALXCRecheck(t *testing.T) {
	d, ctx := cpaLXC(t)
	raw, err := os.ReadFile(filepath.Join(cpaLabRoot, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state cpaLabState
	if json.Unmarshal(raw, &state) != nil || state.Key == "" {
		t.Fatal("private fixture missing")
	}
	ready, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		identity, err := (cpaProxyManager{Docker: d, StateDirectory: cpaLabRoot}).tunnel(ready)
		if err == nil && identity != nil && identity.ContainerID == state.Tunnel {
			break
		}
		select {
		case <-ready.Done():
			t.Fatal("connector did not recover before the readiness deadline")
		case <-time.After(time.Second):
		}
	}
	for _, port := range []int{8317, 8080} {
		if err := waitForServiceEndpoint(ready, cpaLabAddress, port, catalog.Service{Protocol: "http", HealthPath: "/"}); err != nil {
			t.Fatal("application did not become ready after restart")
		}
	}
	// No sync, repair, rewrite, restart or new container before checking this.
	cpaLabHealthy(t, ctx, d, state)
	proof, err := d.CopyFromContainer(ctx, state.Keeper, client.CopyFromContainerOptions{SourcePath: "/data/upgrade-proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	data, header, err := readSingleConfiguration(proof.Content, "upgrade-proof.txt")
	_ = proof.Content.Close()
	if err != nil || string(data) != "before-upgrade\nafter-upgrade\n" || header.Uid != 100 || header.Gid != 101 {
		t.Fatal("Keeper existing data changed across restart")
	}
	t.Log("CPA/Keeper/private ingress survived restart without repair; real Cloudflare remains untested")
}
