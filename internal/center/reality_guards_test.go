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
		 VALUES('reality-guard-service', 'reality-guard-app', '` + siteID + `', 'inbound-9', 'Guarded', 'tcp', 20000, 20000, '10.0.0.80:20000', 'observed', 'vless/tcp/reality', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO three_x_ui_reality_guards(service_id, target_host, target_ip, server_name, node_asn, target_asn, companion_inbound_id, companion_tag, companion_port, revision, status, verified_at, created_at, updated_at)
		 VALUES('reality-guard-service', 'www.example.com', '203.0.113.90', 'www.example.com', 64500, 64500, 10, 'vastora-test-guard', 21000, 1, 'ready', '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO publications(id, service_id, kind, gateway_node_id, hostname, sni_hostname, dns_provider, desired_revision, applied_revision, status, created_at, updated_at)
		 VALUES('reality-guard-publication', 'reality-guard-service', 'public_shared_443', '` + node.ID + `', 'reality.example.test', 'www.example.com', 'manual', 1, 1, 'ready', '` + now + `', '` + now + `')`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
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
