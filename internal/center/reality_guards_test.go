package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
)

func TestRealityTargetProofTreatsASNAsAdvisory(t *testing.T) {
	valid := RealityCommandResult{
		TargetHost: "www.example.com", ServerName: "www.example.com", TargetIP: "203.0.113.10",
		NodeASN: 64500, TargetASN: 64500, TLS13: true, X25519: true, HTTP2: true, CertificateValid: true,
	}
	for _, asns := range [][2]int64{{64500, 64500}, {64500, 64501}, {0, 0}, {0, 64501}, {64500, 0}} {
		candidate := valid
		candidate.NodeASN, candidate.TargetASN = asns[0], asns[1]
		if !validRealityTargetProof(candidate) {
			t.Fatalf("advisory ASN rejected a valid proof: node=%d target=%d", asns[0], asns[1])
		}
	}
	for name, mutate := range map[string]func(*RealityCommandResult){
		"CDN or WAF":       func(value *RealityCommandResult) { value.CDNProvider = "cloudflare" },
		"certificate":      func(value *RealityCommandResult) { value.CertificateValid = false },
		"TLS version":      func(value *RealityCommandResult) { value.TLS13 = false },
		"key exchange":     func(value *RealityCommandResult) { value.X25519 = false },
		"HTTP2":            func(value *RealityCommandResult) { value.HTTP2 = false },
		"private IP":       func(value *RealityCommandResult) { value.TargetIP = "10.0.0.1" },
		"loopback":         func(value *RealityCommandResult) { value.TargetIP = "127.0.0.1" },
		"invalid IP":       func(value *RealityCommandResult) { value.TargetIP = "bad" },
		"invalid hostname": func(value *RealityCommandResult) { value.TargetHost = "bad" },
		"invalid SNI":      func(value *RealityCommandResult) { value.ServerName = "bad" },
		"negative ASN":     func(value *RealityCommandResult) { value.TargetASN = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if validRealityTargetProof(candidate) {
				t.Fatal("unsafe proof passed after relaxing the ASN policy")
			}
		})
	}
}

func TestRealityGuardRevalidationWithdrawsPublicationBeforeHardening(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "reality-guard", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.80", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", PublicAddress: "203.0.113.80", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	for _, statement := range []string{
		`INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		 VALUES('reality-guard-app', '3x-ui', '` + node.ID + `', '` + siteID + `', 'vastora-official/3x-ui', '', 'running', 'docker', 'master', '` + now + `', '` + now + `')`,
		`INSERT INTO services(id, application_id, site_id, name, display_name, protocol, container_port, host_port, endpoint, source, app_protocol, status, created_at, updated_at)
		 VALUES('reality-guard-service', 'reality-guard-app', '` + siteID + `', 'inbound-9', 'Guarded', 'tcp', 443, 443, '10.0.0.80:443', 'observed', 'vless/tcp/reality', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO three_x_ui_reality_guards(service_id, target_host, target_ip, server_name, node_asn, target_asn, companion_inbound_id, companion_tag, companion_port, revision, status, verified_at, created_at, updated_at)
		 VALUES('reality-guard-service', 'www.example.com', '203.0.113.90', 'www.example.com', 64500, 64500, 10, 'vastora-test-guard', 21000, 1, 'ready', '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO publications(id, service_id, kind, gateway_node_id, hostname, sni_hostname, dns_provider, desired_revision, applied_revision, status, created_at, updated_at)
		 VALUES('reality-guard-publication', 'reality-guard-service', 'public_shared_443', '` + node.ID + `', 'reality.example.test', 'www.example.com', 'manual', 1, 1, 'ready', '` + now + `', '` + now + `')`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	readyDesired, err := store.desiredGatewayState(ctx, tx, node.ID, 1)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if readyDesired.SharedHTTPS == nil || len(readyDesired.SharedHTTPS.Routes) != 1 || readyDesired.SharedHTTPS.Routes[0].ProxyProtocol != gateway.ProxyProtocolV2 {
		t.Fatalf("managed REALITY route did not request Proxy Protocol v2: %#v", readyDesired.SharedHTTPS)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE three_x_ui_reality_guards SET target_asn = 0 WHERE service_id = 'reality-guard-service'`); err != nil {
		t.Fatal(err)
	}
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 1 || !strings.Contains(services[0].GuardSummary, "ASN unknown (advisory)") {
		t.Fatalf("unknown ASN service summary = %#v, err = %v", services, err)
	}

	if err := store.quarantineReadyRealityGuards(ctx); err != nil {
		t.Fatal(err)
	}
	var guardStatus, guardError, serviceStatus, publicationStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT guard.status, guard.last_error, service.status, publication.status
		FROM three_x_ui_reality_guards guard JOIN services service ON service.id = guard.service_id
		JOIN publications publication ON publication.service_id = service.id
		WHERE guard.service_id = 'reality-guard-service'`).Scan(&guardStatus, &guardError, &serviceStatus, &publicationStatus); err != nil {
		t.Fatal(err)
	}
	if guardStatus != "action_required" || serviceStatus != "degraded" || publicationStatus != "stopped" || !strings.Contains(guardError, "revalidation") {
		t.Fatalf("quarantined guard=%q error=%q service=%q publication=%q", guardStatus, guardError, serviceStatus, publicationStatus)
	}
	var desiredJSON []byte
	if err := store.db.QueryRowContext(ctx, `SELECT desired_json FROM gateway_states WHERE gateway_node_id = ?`, node.ID).Scan(&desiredJSON); err != nil {
		t.Fatal(err)
	}
	var desired gateway.DesiredState
	if json.Unmarshal(desiredJSON, &desired) != nil {
		t.Fatalf("invalid gateway state: %s", desiredJSON)
	}
	if desired.SharedHTTPS != nil {
		for _, route := range desired.SharedHTTPS.Routes {
			if route.Hostname == "www.example.com" {
				t.Fatalf("quarantined REALITY SNI remained published: %#v", desired.SharedHTTPS.Routes)
			}
		}
	}
}
