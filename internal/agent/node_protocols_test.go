package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/nodeprotocol"
)

func hy2CertificateTask(t *testing.T) nodeprotocol.Task {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"node.example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return nodeprotocol.Task{Selection: nodeprotocol.Selection{VLESS: true, HY2: true}, Hostname: "node.example.com", InboundID: 9, InboundTag: "vastora-node", DisplayName: "Test node", CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))}
}

func TestHY2NativeTLSAndSubscription(t *testing.T) {
	task := hy2CertificateTask(t)
	stream, _ := json.Marshal(hy2StreamSettings(task))
	inbound := threeXUIRealityInbound{ID: 10, Tag: nodeprotocol.HY2Tag(task.InboundTag), Protocol: "hysteria", Port: 443, Enable: true, StreamSettings: stream, Remark: "Test node · HY2", Settings: json.RawMessage(`{"version":2,"clients":[{"email":"alice","auth":"client-secret"}]}`)}
	if !validHY2Runtime(inbound, task.Hostname) {
		t.Fatal("native HY2 TLS configuration rejected")
	}
	if validHY2Runtime(inbound, "wrong.example.com") {
		t.Fatal("wrong certificate hostname accepted")
	}
	if strings.Contains(string(stream), "realitySettings") || strings.Contains(string(stream), `"proxy"`) || strings.Contains(string(stream), `"insecure":true`) {
		t.Fatal("unsafe transport override")
	}
	link, err := hy2ClientLinkFromInbound(inbound, task.Hostname, "alice")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil || parsed.Scheme != "hysteria2" || parsed.Host != "node.example.com:443" || parsed.Query().Get("sni") != task.Hostname || parsed.Query().Has("insecure") {
		t.Fatal("invalid HY2 link")
	}
	if _, err := hy2ClientLinkFromInbound(inbound, task.Hostname, "unassigned"); err == nil {
		t.Fatal("unassigned client received a link")
	}
	primary := threeXUIRealityInbound{ID: 1, Tag: "n7-vastora-node", Protocol: "vless", Enable: true}
	task.TargetNodeID = 7
	if !nodeProtocolsMatch([]threeXUIRealityInbound{primary, inbound}, task) {
		t.Fatal("local IDs and remote tag prefixes prevented convergence")
	}
	inbound.Enable = false
	if nodeProtocolsMatch([]threeXUIRealityInbound{primary, inbound}, task) {
		t.Fatal("disabled HY2 confirmed as enabled")
	}
}

func TestProtocolClientAssociationsRemainOneNode(t *testing.T) {
	inbounds := []ThreeXUIClientInbound{{ID: 9, HY2InboundID: 10}, {ID: 20, HY2InboundID: 21}}
	if got := expandProtocolInboundIDs(inbounds, []int{9}); !slices.Equal(got, []int{9, 10}) {
		t.Fatalf("wrong assignments: %v", got)
	}
	if got := collapseProtocolInboundIDs(inbounds, []int{9, 10, 20, 21}); !slices.Equal(got, []int{9, 20}) {
		t.Fatalf("duplicate nodes: %v", got)
	}
}

func TestAddingHY2DoesNotBypassVLESSNodeQuota(t *testing.T) {
	inbound := threeXUIRealityInbound{Total: 100, Up: 60, Down: 40}
	if enabled, err := nodeProtocolVLESSRunState(inbound, true); err != nil || enabled {
		t.Fatal("exhausted VLESS quota was bypassed")
	}
	inbound.Total = 0
	if enabled, err := nodeProtocolVLESSRunState(inbound, false); err != nil || enabled {
		t.Fatal("explicitly disabled VLESS was enabled")
	}
	if enabled, err := nodeProtocolVLESSRunState(inbound, true); err != nil || !enabled {
		t.Fatal("unlimited selected VLESS was disabled")
	}
}

func TestConfigureHY2RetryPreservesClientsAndVLESS(t *testing.T) {
	task := hy2CertificateTask(t)
	state := map[int]map[string]any{9: {"id": 9, "tag": task.InboundTag, "protocol": "vless", "enable": true, "port": 443, "settings": map[string]any{}, "streamSettings": map[string]any{"network": "raw", "security": "reality"}}}
	creates, attaches := 0, 0
	assigned := []int{9}
	groups := []threeXUIHostGroup{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result any = map[string]any{}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/panel/api/inbounds/list":
			all := []map[string]any{}
			for _, id := range []int{9, 10} {
				if value, ok := state[id]; ok {
					all = append(all, value)
				}
			}
			result = all
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/panel/api/inbounds/get/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/panel/api/inbounds/get/"))
			result = state[id]
		case r.Method == http.MethodPost && r.URL.Path == "/panel/api/inbounds/add":
			creates++
			var value map[string]any
			if json.NewDecoder(r.Body).Decode(&value) != nil {
				t.Error("invalid inbound JSON")
			}
			value["id"] = 10
			state[10] = value
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/panel/api/inbounds/update/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/panel/api/inbounds/update/"))
			var value map[string]any
			if json.NewDecoder(r.Body).Decode(&value) != nil {
				t.Error("invalid update JSON")
			}
			value["id"] = id
			state[id] = value
		case r.URL.Path == "/panel/api/clients/list/paged":
			result = map[string]any{"total": 2, "items": []any{map[string]any{"email": "alice", "inboundIds": assigned}, map[string]any{"email": "other-node", "inboundIds": []int{99}}}}
		case r.URL.Path == "/panel/api/clients/alice/attach":
			attaches++
			assigned = []int{9, 10}
			state[10]["settings"] = map[string]any{"version": 2, "clients": []any{map[string]any{"email": "alice", "auth": "unchanged-client-auth"}}}
		case r.URL.Path == "/panel/api/hosts/byInbound/10":
			result = groups
		case r.Method == http.MethodPost && r.URL.Path == "/panel/api/hosts/add":
			var group threeXUIHostGroup
			if json.NewDecoder(r.Body).Decode(&group) != nil {
				t.Error("invalid host JSON")
			}
			groups = append(groups, group)
		default:
			t.Errorf("unexpected API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": result})
	}))
	defer server.Close()
	ctx := context.Background()
	for range 2 {
		if id, err := configureThreeXUIProtocols(ctx, server.URL, "token", task); err != nil || id != 10 {
			t.Fatalf("configure: %d %v", id, err)
		}
	}
	if creates != 1 || attaches != 1 || state[9]["enable"] != true || state[10]["enable"] != true {
		t.Fatal("retry duplicated or disabled existing services")
	}
	before, _ := json.Marshal(state[10]["settings"])
	task.HY2 = false
	if _, err := configureThreeXUIProtocols(ctx, server.URL, "token", task); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(state[10]["settings"])
	if state[10]["enable"] != false || string(before) != string(after) {
		t.Fatal("disabling HY2 destroyed client credentials")
	}
	task.HY2 = true
	task.VLESS = false
	if _, err := configureThreeXUIProtocols(ctx, server.URL, "token", task); err != nil {
		t.Fatal(err)
	}
	if state[9]["enable"] != false || state[10]["enable"] != true {
		t.Fatal("HY2-only selection not applied")
	}
}
