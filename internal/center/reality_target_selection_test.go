package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

// The orchestration tests start with an authenticated, completed node probe.
// Network/TLS transport itself is tested separately, never by contacting sites.
func createVerifiedRealityCommand(t *testing.T, store *Store, ctx context.Context, input RealityCommandInput) (ApplicationCommandView, error) {
	t.Helper()
	input = seedVerifiedRealityInput(t, store, ctx, input)
	return store.CreateRealityCommand(ctx, input)
}

func TestRealitySelectionRequiresCurrentNodeProof(t *testing.T) {
	for _, scenario := range []string{"current", "expired", "future", "identity", "private address", "public address", "different IP", "unfinished", "different application"} {
		t.Run(scenario, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			profile := networking.Profile{ServiceAddress: "10.0.0.61", LANAddress: "10.0.0.61", PublicAddress: "203.0.113.61", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true}
			node := enrollOrchestrationNode(t, store, "target-proof", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: profile.ServiceAddress, Interface: "eth0"}, {Address: profile.PublicAddress, Interface: "eth0"}}, profile)
			now := store.now().UTC().Format(time.RFC3339Nano)
			if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
				VALUES('verified-app', '3x-ui', ?, ?, ?, '', 'running', 'docker', 'master', ?, ?)`, node.ID, testSiteID(t, store), threeXUIAppKey, now, now); err != nil {
				t.Fatal(err)
			}
			input := seedVerifiedRealityInput(t, store, ctx, RealityCommandInput{ApplicationID: "verified-app", TargetHost: "www.example.com", ServerName: "www.example.com"})
			switch scenario {
			case "expired", "future":
				delta := -16 * time.Minute
				if scenario == "future" {
					delta = 2 * time.Minute
				}
				if _, err := store.db.ExecContext(ctx, `UPDATE application_commands SET updated_at = ? WHERE id = ?`, store.now().Add(delta).UTC().Format(time.RFC3339Nano), input.VerificationID); err != nil {
					t.Fatal(err)
				}
			case "identity":
				if _, err := store.db.ExecContext(ctx, `UPDATE agents SET x25519_public_key = ? WHERE id = ?`, []byte("changed-identity"), node.ID); err != nil {
					t.Fatal(err)
				}
			case "private address":
				profile.ServiceAddress = "10.0.0.62"
			case "public address":
				profile.PublicAddress = "203.0.113.62"
			case "different IP":
				input.TargetIP = "203.0.113.11"
			case "unfinished":
				if _, err := store.db.ExecContext(ctx, `UPDATE application_commands SET state = 'pending' WHERE id = ?`, input.VerificationID); err != nil {
					t.Fatal(err)
				}
			case "different application":
				input.ApplicationID = "other-app"
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			selected, err := store.selectedRealityTarget(ctx, tx, input, node.ID, profile.ServiceAddress, profile.PublicAddress)
			if scenario == "current" {
				if err != nil || selected == nil || selected.TargetIP != input.TargetIP {
					t.Fatalf("current selection rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("unverified or changed selection accepted")
			}
		})
	}
}

func seedVerifiedRealityInput(t *testing.T, store *Store, ctx context.Context, input RealityCommandInput) RealityCommandInput {
	t.Helper()
	var nodeID, siteID string
	request := RealityCommandTask{Action: "verify", TargetApplicationID: input.ApplicationID, TargetHost: input.TargetHost, ServerName: input.ServerName}
	if err := store.db.QueryRowContext(ctx, `SELECT a.node_id, a.site_id, p.service_address, p.public_address, n.x25519_public_key
		FROM applications a JOIN agent_network_profiles p ON p.agent_id = a.node_id JOIN agents n ON n.id = a.node_id
		WHERE a.id = ?`, input.ApplicationID).Scan(&nodeID, &siteID, &request.TargetAddress, &request.TargetPublicAddress, &request.TargetAgentPublicKey); err != nil {
		t.Fatal(err)
	}
	token, err := randomToken(18)
	if err != nil {
		t.Fatal(err)
	}
	input.VerificationID, input.TargetIP = "verification-"+token, "203.0.113.10"
	result := RealityCommandResult{Action: "verify", TargetHost: input.TargetHost, TargetIP: input.TargetIP, ServerName: input.ServerName, TLS13: true, X25519: true, HTTP2: true, CertificateValid: true}
	requestJSON, _ := json.Marshal(request)
	resultJSON, _ := json.Marshal(result)
	now := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, site_id, display_name, agent_id, gateway_node_id, kind, input_json, result_json, state, created_at, updated_at)
		VALUES(?, ?, ?, '', ?, ?, ?, ?, ?, 'succeeded', ?, ?)`, input.VerificationID, input.ApplicationID, siteID, nodeID, nodeID, realityVerifyCommandKind, requestJSON, resultJSON, now, now); err != nil {
		t.Fatal(err)
	}
	return input
}
