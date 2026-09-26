package center

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestSupersededFailedLandingExecutionReleasesFence(t *testing.T) {
	for _, kind := range []string{"landing.proxy.apply", "landing.server.apply"} {
		t.Run(kind, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "superseded-landing", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.93", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.93", LANAddress: "10.0.0.93", EnabledKinds: []string{networking.KindLAN}})
			stamp := store.now().UTC().Format(time.RFC3339Nano)
			desired, _ := json.Marshal(map[string]any{"nodeId": node.ID, "revision": 8})
			taskID := landingServerTaskID(node.ID, 7)
			if kind == "landing.server.apply" {
				if _, err := store.db.Exec(`INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,status,attempt,updated_at) VALUES(?,8,6,?,'pending',4,?)`, node.ID, desired, stamp); err != nil {
					t.Fatal(err)
				}
			} else {
				deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`), AuthorizedCapabilities: testCapabilityGrant("root")})
				if err != nil {
					t.Fatal(err)
				}
				taskID = landingProxyTaskID(node.ID, 7)
				if _, err := store.db.Exec(`INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,server_revision,source_address,desired_revision,applied_revision,desired_json,status,attempt,updated_at) VALUES(?,?,?,1,'100.64.0.93',8,6,?,'pending',4,?)`, node.ID, deployment.ApplicationID, node.ID, desired, stamp); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,last_error,expires_at,created_at,updated_at) VALUES('superseded-landing-execution',?,?,?,3,'old-session','digest',X'00','failed','reported','preserved failure',?,?,?)`, node.ID, taskID, kind, stamp, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			session := "superseded-landing-replacement-session"
			if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			if err := store.executionClaimAllowed(ctx, node.ID, session); err != nil {
				t.Fatalf("superseded failure kept the Agent fenced: %v", err)
			}
			var state, disposition, message string
			if err := store.db.QueryRow(`SELECT state,disposition,last_error FROM task_executions WHERE id='superseded-landing-execution'`).Scan(&state, &disposition, &message); err != nil {
				t.Fatal(err)
			}
			if state != "failed" || disposition != "superseded" || message != "preserved failure" {
				t.Fatalf("execution evidence changed: state=%q disposition=%q error=%q", state, disposition, message)
			}
		})
	}
}

func TestCurrentOrUnknownLandingExecutionRemainsFenced(t *testing.T) {
	for _, tc := range []struct {
		name, state, phase string
		revision, attempt  int64
		wantReleased       bool
	}{
		{"current failure", "failed", "reported", 8, 4, false},
		{"older unknown", "unknown", "reported", 7, 3, false},
		{"older active", "running", "started", 7, 3, false},
		{"older unreported failure", "failed", "apply", 7, 3, false},
		{"older attempt", "failed", "reported", 8, 3, true},
		{"future revision", "failed", "reported", 9, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "current-landing", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.94", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.94", LANAddress: "10.0.0.94", EnabledKinds: []string{networking.KindLAN}})
			stamp := store.now().UTC().Format(time.RFC3339Nano)
			desired, _ := json.Marshal(map[string]any{"nodeId": node.ID, "revision": 8})
			if _, err := store.db.Exec(`INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,status,attempt,updated_at) VALUES(?,8,7,?,'failed',4,?)`, node.ID, desired, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,last_error,expires_at,created_at,updated_at) VALUES('current-landing-execution',?,?,'landing.server.apply',?,'old-session','digest',X'00',?,?,'preserved failure',?,?,?)`, node.ID, landingServerTaskID(node.ID, tc.revision), tc.attempt, tc.state, tc.phase, stamp, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			session := "current-landing-replacement-session"
			if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			if err := store.executionClaimAllowed(ctx, node.ID, session); tc.wantReleased && err != nil || !tc.wantReleased && !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("current or unknown execution lost its fence: %v", err)
			}
			var disposition string
			wantDisposition := ""
			if tc.wantReleased {
				wantDisposition = "superseded"
			}
			if err := store.db.QueryRow(`SELECT disposition FROM task_executions WHERE id='current-landing-execution'`).Scan(&disposition); err != nil || disposition != wantDisposition {
				t.Fatalf("execution was disposed: %q %v", disposition, err)
			}
		})
	}
}
