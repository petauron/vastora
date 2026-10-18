package center

import (
	"bufio"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/meridianruntime"
	"golang.org/x/net/proxy"
)

// Run only from the guarded private Linux fixture. Uses the production Glivela
// renderer and subscription acceptance client, official Xray, and HAProxy's
// PROXY v2 path. All credentials are ephemeral and files stay private to the lab.
func privateLabXrayClient(t *testing.T, source, landingAddress string) string {
	t.Helper()
	for _, address := range []string{"127.0.0.1:18080", "127.0.0.1:18443", net.JoinHostPort(source, "10443")} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("test port already in use: %v", err)
		}
		_ = listener.Close()
	}
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	target.EnableHTTP2 = true
	target.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	target.StartTLS()
	t.Cleanup(target.Close)
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nativeID, routeID := uuid.NewString(), uuid.NewString()
	native := glivela.Credential{ID: "native", AccountID: "account", Kind: glivela.NativeCredential, User: "lab-native", Identity: glivela.Identity(nativeID), EntryID: "entry", Enabled: true}
	route := glivela.Credential{ID: "route", AccountID: "account", Kind: glivela.RouteCredential, User: glivela.RouteUser("grant"), Identity: glivela.Identity(routeID), EntryID: "entry", EgressID: "egress", Enabled: true}
	ep := glivela.RealityEndpoint{ID: "endpoint", EntryID: "entry", InboundTag: "lab-entry", ListenAddress: source, ListenPort: 10443, AdvertiseHost: "127.0.0.1", AdvertisePort: 18443, Target: target.Listener.Addr().String(), ServerNames: []string{"example.com"}, PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), ShortIDs: []string{"0940094009400940"}, Fingerprint: "chrome"}
	material := glivela.CredentialMaterial{Credential: route, ProtocolID: routeID}
	plan := glivela.XrayPlan{Revision: 1, RealityEndpoints: []glivela.RealityEndpoint{ep}, Credentials: []glivela.CredentialMaterial{{Credential: native, ProtocolID: nativeID}, material}, Grants: []glivela.RouteGrant{{ID: "grant", AccountID: "account", EntryID: "entry", EgressID: "egress", InboundTag: "lab-entry", Base: native, Route: route, Mode: glivela.FixedMode, Enabled: true, DesiredRev: 1, AppliedRev: 1}}, Peers: []glivela.RoutePeer{{EgressID: "egress", Address: landingAddress, Port: 1080}}}
	serverConfig, err := glivela.RenderXrayConfiguration(plan)
	if err != nil {
		t.Fatal(err)
	}
	ep.PrivateKey = ""
	clientConfig, err := (meridianruntime.AcceptanceClient{Protocol: glivela.VLESSReality, Reality: &ep, Material: material}).Config(18080)
	if err != nil {
		t.Fatal(err)
	}
	// Private lab diagnostics only; configurations and credentials are not logged.
	for _, cfg := range []*[]byte{&serverConfig, &clientConfig} {
		var v map[string]any
		if err = json.Unmarshal(*cfg, &v); err != nil {
			t.Fatal(err)
		}
		v["log"] = map[string]any{"loglevel": "debug"}
		*cfg, err = json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if e := os.WriteFile(path, data, 0600); e != nil {
			t.Fatal(e)
		}
		return path
	}
	start := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
		logfile, e := os.CreateTemp("/opt/vastora940", "xray-process-*.log")
		if e != nil {
			t.Fatal(e)
		}
		cmd.Stdout, cmd.Stderr = logfile, logfile
		if e := cmd.Start(); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = logfile.Close() })
	}
	start("/opt/vastora940/xray", "run", "-config", write("entry.json", serverConfig))
	front := fmt.Sprintf("global\n maxconn 64\ndefaults\n mode tcp\n timeout connect 5s\n timeout client 15s\n timeout server 15s\nfrontend entry\n bind 127.0.0.1:18443\n default_backend xray\nbackend xray\n server lab %s:10443 send-proxy-v2\n", source)
	start("/usr/sbin/haproxy", "-db", "-f", write("haproxy.cfg", []byte(front)))
	start("/opt/vastora940/xray", "run", "-config", write("client.json", clientConfig))
	endpoint := "127.0.0.1:18080"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, e := net.DialTimeout("tcp", endpoint, time.Second)
		if e == nil {
			c.Close()
			return endpoint
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("official Xray client did not start")
	return ""
}

func privateLabExit(endpoint string) (string, error) {
	d, err := proxy.SOCKS5("tcp", endpoint, nil, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		return "", err
	}
	c, err := d.Dial("tcp", "1.1.1.1:80")
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err = io.WriteString(c, "GET / HTTP/1.1\r\nHost: 1.1.1.1\r\nConnection: close\r\n\r\n"); err != nil {
		return "", err
	}
	r, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(b))
	if r.StatusCode != 200 || net.ParseIP(ip) == nil {
		return "", fmt.Errorf("invalid synthetic target source observation")
	}
	return ip, nil
}
