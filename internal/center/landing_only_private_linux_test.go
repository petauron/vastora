package center

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
	"golang.org/x/net/proxy"
)

// The disposable control guest runs Headscale natively, not in the production
// Docker wrapper. Apply the same policy renderer to the real server and check
// and install it using Headscale's CLI; this is not a successful no-op mock.
type privateLabInfrastructure struct {
	deployapi.InfrastructureManager
}

func (privateLabInfrastructure) ApplyLandingPolicy(ctx context.Context, input deployapi.LandingPolicyRequest) error {
	base := []byte(`{"tagOwners":{"tag:vastora-agent":["vastora@"],"tag:vastora-gateway":["vastora@"],"tag:vastora-center":["vastora@"]},"grants":[{"src":["tag:vastora-gateway"],"dst":["tag:vastora-agent"],"ip":["*"]},{"src":["tag:vastora-agent"],"dst":["tag:vastora-center"],"ip":["443"]}]}`)
	policy, err := landing.ExtendHeadscalePolicy(base, input.Rules)
	if err != nil {
		return err
	}
	path := "/opt/vastora940/policy.hujson"
	if err = os.WriteFile(path, policy, 0600); err != nil {
		return err
	}
	for _, verb := range []string{"check", "set"} {
		if out, e := exec.CommandContext(ctx, "/opt/vastora940/headscale", "policy", verb, "--file", path, "--config", "/etc/headscale/config.yaml").CombinedOutput(); e != nil {
			return fmt.Errorf("Headscale policy %s: %w: %s", verb, e, out)
		}
	}
	return nil
}

// This opt-in harness uses real Headscale, the production enrollment API,
// real Agent heartbeats and the Center landing selection/task pipeline. The
// installer is delivered by the lab operator to a separate disposable guest.
// No credentials or production database fixtures are included in the repo.
func TestLandingOnlyPrivateLinux(t *testing.T) {
	if os.Getenv("VASTORA_DISPOSABLE_PRIVATE_LAB") != "issue-940" {
		t.Skip("disposable private-network lab only")
	}
	host, _ := os.Hostname()
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || host != "vastora-940-control-test" {
		t.Fatal("unexpected lab host")
	}
	ctx := context.Background()
	privateLabSyntheticTargets(t)
	root := "/opt/vastora940"
	s, err := Open(filepath.Join(root, "center-state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	Version = os.Getenv("VASTORA_LAB_TARGET_VERSION")
	if Version == "" {
		Version = "0.1.1-940lab.2"
	}
	dist := filepath.Join(root, "distribution")
	if err = os.MkdirAll(dist, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "v2"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dist, "linux-amd64"), b, 0755); err != nil {
		t.Fatal(err)
	}
	app := NewServer(s, "", false).WithInfrastructureManager(privateLabInfrastructure{}).WithAgentBinaries(dist).Handler()
	u, _ := url.Parse("http://127.0.0.1:8081")
	proxy := httputil.NewSingleHostReverseProxy(u)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSuffix(r.Host, ":443") == "headscale.vastora940.test" && !strings.HasPrefix(r.URL.Path, "/install/") && !strings.HasPrefix(r.URL.Path, "/api/v1/agent") {
			proxy.ServeHTTP(w, r)
			return
		}
		app.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp", ":443")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	go func() { _ = server.ServeTLS(listener, filepath.Join(root, "lab.crt"), filepath.Join(root, "lab.key")) }()
	s.builtinHeadscaleDialAddress = "127.0.0.1:443"
	key, err := os.ReadFile(filepath.Join(root, "headscale-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	var keys []struct {
		ID         uint64 `json:"id"`
		Prefix     string `json:"prefix"`
		Expiration struct {
			Seconds int64 `json:"seconds"`
		} `json:"expiration"`
	}
	b, err = os.ReadFile(filepath.Join(root, "headscale-api-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &keys); err != nil || len(keys) != 1 {
		t.Fatal("invalid lab API key metadata")
	}
	if _, err = s.ConfigureBuiltinHeadscale(ctx, deployapi.HeadscaleInstallResult{Endpoint: "https://headscale.vastora940.test", APIKey: strings.TrimSpace(string(key)), APIKeyID: keys[0].ID, APIKeyPrefix: strings.TrimSuffix(keys[0].Prefix, "***"), APIKeyExpiresAt: time.Unix(keys[0].Expiration.Seconds, 0)}, deployapi.HeadscaleDNSPolicySystem, nil); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "landing-install.sh")); os.IsNotExist(statErr) {
		password, err := randomToken(32)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = s.CreateFirstAdmin(ctx, "lab", password); err != nil {
			t.Fatal(err)
		}
		setup, err := s.CompleteInitialSetup(ctx, InitialSetupInput{Site: SiteInput{Name: "Private Lab", Code: "private-lab", Timezone: "UTC"}, Network: CenterNetworkInput{AgentConnectionMode: "public", AgentConnectURL: "https://center.vastora940.test"}})
		if err != nil {
			t.Fatal(err)
		}
		e, err := s.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: setup.Site.ID, Name: "landing-private-lab", CenterURL: "https://center.vastora940.test", LandingOnly: true, UseHeadscale: true})
		if err != nil {
			t.Fatal(err)
		}
		// Only root can read this one-time enrollment script. Never log its contents.
		script := "#!/bin/sh\nset -eu\ncurl -fsSL https://center.vastora940.test/install/agent.sh -o /opt/vastora940/loader.sh\nsh /opt/vastora940/loader.sh " + shellQuote(e.Token) + "\n"
		if err = os.WriteFile(filepath.Join(root, "landing-install.sh"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("READY: separate landing guest may run the enrollment script")
	wait := func(label string, f func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Minute)
		for time.Now().Before(deadline) {
			if f() {
				t.Log(label)
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("timeout: %s", label)
	}
	var id, address string
	wait("real managed private heartbeat", func() bool {
		return s.db.QueryRow(`SELECT agent.id,c.address FROM agents agent JOIN agent_network_candidates c ON c.agent_id=agent.id WHERE agent.name='landing-private-lab' AND agent.tailscale_ownership='managed' AND json_extract(agent.capabilities_json,'$.landingOnly')=1 AND c.kind='headscale' AND agent.last_seen_at>?`, time.Now().Add(-20*time.Second).UTC().Format(time.RFC3339Nano)).Scan(&id, &address) == nil
	})
	var installed string
	if err = s.db.QueryRow(`SELECT version FROM agents WHERE id=?`, id).Scan(&installed); err != nil {
		t.Fatal(err)
	}
	if installed != Version {
		update, e := s.QueueAgentUpdate(ctx, id, Version)
		if e != nil {
			t.Fatal(e)
		}
		wait("managed Agent update and successful receipt", func() bool {
			var n int
			return s.db.QueryRow(`SELECT COUNT(*) FROM agents a JOIN agent_updates u ON u.agent_id=a.id WHERE a.id=? AND a.version=? AND u.id=? AND u.state='succeeded'`, id, Version, update.ID).Scan(&n) == nil && n == 1
		})
	}
	if _, err = s.ConfirmNetworkProfile(ctx, id, networking.Profile{ServiceAddress: address, HeadscaleAddress: address, EnabledKinds: []string{networking.KindHeadscale}}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := readLandingSelection(ctx, tx)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.NodeIDs) == 0 {
		if err = s.SelectMeridianLanding(ctx, LandingSelection{Revision: selection.Revision, NodeIDs: []string{id}, LandingRegionCodes: map[string]string{id: "US"}}); err != nil {
			t.Fatal(err)
		}
	}
	wait("Center native Dante task acknowledged", func() bool {
		var n int
		return s.db.QueryRow(`SELECT COUNT(*) FROM landing_server_states WHERE node_id=? AND status='ready' AND desired_revision=applied_revision`, id).Scan(&n) == nil && n == 1
	})
	if err = os.WriteFile(filepath.Join(root, "landing-address"), []byte(address), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("PRIVATE ENROLLMENT AND CENTER DEPLOYMENT PASSED")
	join, err := s.CreateHeadscaleBootstrap(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	// A dedicated traffic-generator peer, not a synthetic Center heartbeat.
	joinCtx, cancel := context.WithTimeout(ctx, time.Minute)
	_, err = exec.CommandContext(joinCtx, "sh", "-c", strings.Replace(strings.TrimPrefix(join.Command, "sudo "), "tailscale ", "/usr/local/bin/tailscale ", 1)).CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("traffic peer join: %v", err)
	}
	ip, err := exec.Command("/usr/local/bin/tailscale", "ip", "-4").Output()
	if err != nil {
		t.Fatal(err)
	}
	source := strings.TrimSpace(string(ip))
	endpoint := net.JoinHostPort(address, "1080")
	apply := func(sources []landing.AuthorizedNode) {
		t.Helper()
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if e = s.queueLandingServer(ctx, tx, id, &landing.ServerPlan{Address: address, Sources: sources}); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		wait("native source policy applied", func() bool {
			var n int
			return s.db.QueryRow(`SELECT COUNT(*) FROM landing_server_states WHERE node_id=? AND status='ready' AND desired_revision=applied_revision`, id).Scan(&n) == nil && n == 1
		})
		wait("fresh healthy native runtime", func() bool {
			var raw []byte
			var received string
			var revision uint64
			if s.db.QueryRow(`SELECT health_json,health_received_at,desired_revision FROM landing_server_states WHERE node_id=?`, id).Scan(&raw, &received, &revision) != nil {
				return false
			}
			return nativeLandingHealthy(raw, received, revision, time.Now())
		})
	}
	// Source fixture exercises Center policy/task dispatch and the actual native
	// firewall. Account-to-source derivation is covered by the grant regression
	// tests; this fixture does not fabricate application/credential DB rows.
	apply(nil)
	if err = privateLabTCP(endpoint, "1.1.1.1:80"); err == nil {
		t.Fatal("unauthorized source was accepted")
	}
	t.Log("unauthorized source blocked")
	apply([]landing.AuthorizedNode{{Address: source, TCPOnly: true}})
	xrayClient := privateLabXrayClient(t, source, address)
	if err = privateLabTCP(xrayClient, "1.1.1.1:80"); err != nil {
		t.Fatal("VLESS/REALITY through HAProxy and landing", err)
	}
	directExit, e := privateLabExit(endpoint)
	if e != nil {
		t.Fatal("native exit observation", e)
	}
	routedExit, e := privateLabExit(xrayClient)
	if e != nil || routedExit != directExit {
		t.Fatal("VLESS exit does not match native landing", e)
	}
	t.Log("official Xray VLESS/REALITY traverses HAProxy/private Dante; synthetic target observes the same landing source")
	if err = privateLabTCP(endpoint, "1.1.1.1:80"); err != nil {
		t.Fatal("authorized TCP", err)
	}
	if err = privateLabUDP(endpoint); err == nil {
		t.Fatal("TCP-only source accepted UDP")
	}
	t.Log("TCP-only source: TCP accepted, UDP denied")
	apply([]landing.AuthorizedNode{{Address: source}})
	if err = privateLabTCP(endpoint, "1.1.1.1:80"); err != nil {
		t.Fatal("TCP", err)
	}
	if err = privateLabUDP(endpoint); err != nil {
		t.Fatal("UDP DNS roundtrip", err)
	}
	if err = privateLabTCP(endpoint, "127.0.0.1:80"); err == nil {
		t.Fatal("loopback destination accepted")
	}
	controlIPs, lookupErr := net.LookupIP("center.vastora940.test")
	if lookupErr != nil || len(controlIPs) == 0 {
		t.Fatal("resolve lab control address", lookupErr)
	}
	if err = privateLabTCP(endpoint, net.JoinHostPort(controlIPs[0].String(), "443")); err == nil {
		t.Fatal("LAN destination accepted")
	}
	t.Log("authorized TCP and UDP payload roundtrips passed; private destinations denied")
	apply(nil)
	if err = privateLabTCP(endpoint, "1.1.1.1:80"); err == nil {
		t.Fatal("revoked source accepted TCP")
	}
	if err = privateLabUDP(endpoint); err == nil {
		t.Fatal("revoked source accepted UDP")
	}
	t.Log("revocation blocked TCP and UDP")
	if err = privateLabTCP(xrayClient, "1.1.1.1:80"); err == nil {
		t.Fatal("VLESS route fell back after source revocation")
	}
	apply([]landing.AuthorizedNode{{Address: source}})
	if err = os.Remove(filepath.Join(root, "fault-injected")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "fault-requested"), []byte("stop native Dante on the disposable landing guest"), 0600); err != nil {
		t.Fatal(err)
	}
	wait("operator stopped native Dante", func() bool { _, e := os.Stat(filepath.Join(root, "fault-injected")); return e == nil })
	if err = privateLabTCP(endpoint, "1.1.1.1:80"); err == nil {
		t.Fatal("stopped service still forwarded TCP")
	}
	if err = privateLabUDP(endpoint); err == nil {
		t.Fatal("stopped service still forwarded UDP")
	}
	t.Log("stopped service fails closed for TCP and UDP")
	if err = privateLabTCP(xrayClient, "1.1.1.1:80"); err == nil {
		t.Fatal("VLESS route fell back after service failure")
	}
	wait("Center reports native service failure", func() bool {
		view, e := s.Landing(ctx)
		return e == nil && len(view.Servers) == 1 && view.Servers[0].Status == "failed"
	})
	apply([]landing.AuthorizedNode{{Address: source}})
	if err = os.Remove(filepath.Join(root, "firewall-fault-injected")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "firewall-fault-requested"), []byte("remove only the current owned native landing firewall table on the disposable landing guest"), 0600); err != nil {
		t.Fatal(err)
	}
	wait("operator removed owned native firewall", func() bool { _, e := os.Stat(filepath.Join(root, "firewall-fault-injected")); return e == nil })
	wait("Center reports missing firewall", func() bool {
		view, e := s.Landing(ctx)
		return e == nil && len(view.Servers) == 1 && view.Servers[0].Status == "failed"
	})
	if err = privateLabTCP(endpoint, "1.1.1.1:80"); err == nil {
		t.Fatal("firewall failure still forwarded TCP")
	}
	if err = privateLabUDP(endpoint); err == nil {
		t.Fatal("firewall failure still forwarded UDP")
	}
	t.Log("missing firewall stopped owned Dante and blocked TCP/UDP")
	apply(nil)
	if err = os.WriteFile(filepath.Join(root, "traffic-accepted"), []byte("passed"), 0600); err != nil {
		t.Fatal(err)
	}
}

// The lab operator routes only this /32 from the disposable landing guest to
// this control guest. No host, shared network or public route is changed.
// Its public-shaped address exercises the unmodified destination restrictions.
func privateLabSyntheticTargets(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("ip", "address", "add", "1.1.1.1/32", "dev", "lo").CombinedOutput(); err != nil {
		t.Fatalf("isolated synthetic address: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "address", "del", "1.1.1.1/32", "dev", "lo").Run() })
	l, err := net.Listen("tcp4", "1.1.1.1:80")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		_, _ = io.WriteString(w, host)
	})}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(l) }()
	u, err := net.ListenPacket("udp4", "1.1.1.1:53")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, peer, err := u.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			reply := append([]byte(nil), buf[:n]...)
			reply[2], reply[3], reply[6], reply[7] = 0x81, 0x80, 0, 1
			reply = append(reply, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 1)
			_, _ = u.WriteTo(reply, peer)
		}
	}()
}

func privateLabTCP(endpoint, destination string) error {
	d, e := proxy.SOCKS5("tcp", endpoint, nil, &net.Dialer{Timeout: 4 * time.Second})
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	c, e := d.(proxy.ContextDialer).DialContext(ctx, "tcp", destination)
	if e != nil {
		return e
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	if _, e = io.WriteString(c, "GET / HTTP/1.1\r\nHost: 1.1.1.1\r\nConnection: close\r\n\r\n"); e != nil {
		return e
	}
	b := make([]byte, 5)
	_, e = io.ReadFull(c, b)
	if e != nil {
		return e
	}
	if string(b) != "HTTP/" {
		return fmt.Errorf("unexpected HTTP payload")
	}
	return nil
}

func privateLabUDP(endpoint string) error {
	c, e := net.DialTimeout("tcp", endpoint, 4*time.Second)
	if e != nil {
		return e
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(6 * time.Second))
	if _, e = c.Write([]byte{5, 1, 0}); e != nil {
		return e
	}
	g := make([]byte, 2)
	if _, e = io.ReadFull(c, g); e != nil {
		return e
	}
	if g[0] != 5 || g[1] != 0 {
		return fmt.Errorf("SOCKS authentication denied")
	}
	if _, e = c.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); e != nil {
		return e
	}
	r := make([]byte, 10)
	if _, e = io.ReadFull(c, r); e != nil {
		return e
	}
	if r[1] != 0 || r[3] != 1 {
		return fmt.Errorf("UDP association denied: %d", r[1])
	}
	port := int(binary.BigEndian.Uint16(r[8:]))
	if port < landing.UDPRelayFirst || port > landing.UDPRelayLast {
		return fmt.Errorf("unexpected relay port")
	}
	u, e := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IP(r[4:8]), Port: port})
	if e != nil {
		return e
	}
	defer u.Close()
	_ = u.SetDeadline(time.Now().Add(6 * time.Second))
	// DNS query ID 0x0940, A example.com, transported through SOCKS UDP.
	dns := []byte{9, 64, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
	packet := append([]byte{0, 0, 0, 1, 1, 1, 1, 1, 0, 53}, dns...)
	if _, e = u.Write(packet); e != nil {
		return e
	}
	b := make([]byte, 2048)
	n, e := u.Read(b)
	if e != nil {
		return e
	}
	if n < 22 || b[3] != 1 || b[10] != 9 || b[11] != 64 || b[12]&128 == 0 || binary.BigEndian.Uint16(b[16:18]) == 0 {
		return fmt.Errorf("invalid DNS response through relay")
	}
	return nil
}
