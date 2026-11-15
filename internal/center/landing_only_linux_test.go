package center

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Run only inside a disposable root-owned Linux lab, never a developer or
// production host. This isolates Agent lifecycle from Headscale provisioning;
// it does not certify private-network enrollment or real egress traffic.
func TestLandingOnlyLinuxLifecycle(t *testing.T) {
	if os.Getenv("VASTORA_DISPOSABLE_LIFECYCLE_LAB") != "issue-940" {
		t.Skip("disposable Linux lab only")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || os.Geteuid() != 0 {
		t.Fatal("requires root on Linux amd64")
	}
	hostname, _ := os.Hostname()
	if hostname != "vastora-940-landing-test" {
		t.Fatal("unexpected lab host")
	}
	for _, p := range []string{"/usr/local/bin/vastora", "/var/lib/vastora/agent/agent.db", "/var/run/docker.sock"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("lab not clean: %s", p)
		}
	}
	run := func(name string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", name, err, out)
		}
		return string(out)
	}
	t.Log("baseline", run("df", "-B1", "/"))
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	waitHeartbeat := func(version string) {
		t.Helper()
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE version=? AND json_extract(capabilities_json,'$.landingOnly')=1 AND COALESCE(json_extract(capabilities_json,'$.docker'),0)=0`, version).Scan(&count); err == nil && count == 1 {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("Center did not receive landing-only heartbeat for %s", version)
	}
	distribution := t.TempDir()
	publish := func(source string) {
		b, e := os.ReadFile(source)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(distribution, "linux-amd64"), b, 0755); e != nil {
			t.Fatal(e)
		}
	}
	Version = "0.1.0-940lab.1"
	publish("/opt/vastora940/v1")
	server := grpcTestHTTPServer(NewServer(store, "", false).WithAgentBinaries(distribution).Handler())
	defer server.Close()
	password, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateFirstAdmin(context.Background(), "lab", password); err != nil {
		t.Fatal(err)
	}
	setup, err := store.CompleteInitialSetup(context.Background(), InitialSetupInput{Site: SiteInput{Name: "Lab", Code: "lab", Timezone: "UTC"}, Network: CenterNetworkInput{AgentConnectionMode: "public", AgentConnectURL: server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	caps, _ := json.Marshal(NodeCapabilities{LandingOnly: true, NativeEgress: true})
	enrollment, err := store.createAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: setup.Site.ID, Name: "lifecycle-lab", CenterURL: server.URL}, []byte(`["worker"]`), caps, "", "")
	if err != nil {
		t.Fatal(err)
	}
	loader := filepath.Join(t.TempDir(), "install.sh")
	if err = os.WriteFile(loader, []byte(renderAgentInstallLoader(server.URL)), 0700); err != nil {
		t.Fatal(err)
	}
	// Never log the enrollment token or full command arguments.
	run("sh", loader, enrollment.Token)
	waitHeartbeat(Version)
	if strings.TrimSpace(run("systemctl", "is-active", "vastora-agent")) != "active" {
		t.Fatal("inactive Agent")
	}
	run("systemctl", "restart", "vastora-agent")
	time.Sleep(3 * time.Second)
	if strings.TrimSpace(run("systemctl", "is-active", "vastora-agent")) != "active" {
		t.Fatal("restart failed")
	}
	t.Log("installed", run("df", "-B1", "/"))
	Version = "0.1.0-940lab.2"
	publish("/opt/vastora940/v2")
	var nodeID string
	if err := store.db.QueryRow(`SELECT id FROM agents WHERE name='lifecycle-lab'`).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	update, err := store.QueueAgentUpdate(context.Background(), nodeID, Version)
	if err != nil {
		t.Fatal(err)
	}
	waitHeartbeat(Version)
	var updateState string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if err := store.db.QueryRow(`SELECT state FROM agent_updates WHERE id=?`, update.ID).Scan(&updateState); err != nil {
			t.Fatal(err)
		}
		if updateState == "succeeded" {
			break
		}
		time.Sleep(time.Second)
	}
	if updateState != "succeeded" {
		t.Fatalf("update not acknowledged: %s", updateState)
	}
	if got := strings.TrimSpace(run("/usr/local/bin/vastora", "version")); got != Version {
		t.Fatalf("wrong version %s", got)
	}
	waitHeartbeat(Version)
	time.Sleep(3 * time.Second)
	if strings.TrimSpace(run("systemctl", "is-active", "vastora-agent")) != "active" {
		t.Fatal("updated Agent failed")
	}
	t.Log("updated", run("df", "-B1", "/"))
	run("/usr/local/bin/vastora", "agent", "uninstall", "--purge", "--delete-data")
	for _, p := range []string{"/usr/local/bin/vastora", "/var/lib/vastora/agent/agent.db", "/etc/systemd/system/vastora-agent.service", "/var/run/docker.sock"} {
		if _, e := os.Stat(p); !os.IsNotExist(e) {
			t.Fatalf("unexpected remaining path %s", p)
		}
	}
	for _, name := range []string{"docker", "containerd"} {
		if _, e := exec.LookPath(name); e == nil {
			t.Fatalf("unexpected runtime %s", name)
		}
	}
	t.Log("uninstalled", run("df", "-B1", "/"))
}
