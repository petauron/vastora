package center

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func reinstallEntryCheckFixture(t *testing.T) (*Store, AgentCredential, AgentReinstallApplicationInput) {
	t.Helper()
	s, node, input := reinstallListenerFixture(t)
	ctx := context.Background()
	if _, err := s.QueueAgentReinstallListener(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	if response := submitReinstallListener(t, s, node, task, true); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	return s, node, input
}

func TestAgentReinstallEntryCheckRecordsEvidenceWithoutCompletingRecovery(t *testing.T) {
	s, node, input := reinstallEntryCheckFixture(t)
	ctx := context.Background()
	probes := 0
	check := func(ctx context.Context, entry AgentReinstallEntryCheckResult) string {
		probes++
		if entry.PublicAddress != "198.51.100.8" || entry.Hostname != "entry.example.test" || entry.SNIHostname != "www.example.com" || entry.PublicationID != "restored-entry" {
			t.Fatalf("unreviewed target: %+v", entry)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded check")
		}
		return "passed"
	}
	result, err := s.verifyAgentReinstallEntry(ctx, node.ID, "reinstall-review-admin", input, check)
	if err != nil || result.State != "passed" || !result.Current || probes != 1 {
		t.Fatalf("check: %+v %v", result, err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved := plan.Applications[0].Preparation.EntryCheck
	if saved == nil || saved.ID != result.ID || !saved.Current || plan.Recovery.State != "review_required" {
		t.Fatalf("receipt: %+v", saved)
	}
	var profiles int
	var endpoint, publication, source string
	if err = s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM agent_network_profiles WHERE agent_id=?),(SELECT endpoint FROM services WHERE id='restore-service'),(SELECT status FROM publications WHERE id='restored-entry'),(SELECT CAST(source_peer_json AS TEXT) FROM meridian_endpoints WHERE id='restore-endpoint')`, node.ID).Scan(&profiles, &endpoint, &publication, &source); err != nil {
		t.Fatal(err)
	}
	if profiles != 0 || endpoint != "10.0.0.7:10443" || publication != "degraded" || source != "{}" {
		t.Fatalf("reachability published business: %d %s %s %s", profiles, endpoint, publication, source)
	}
	if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("released fence: %v", err)
	}
	// An explicit read-only recheck retains both observations, without changing intent.
	next, err := s.verifyAgentReinstallEntry(ctx, node.ID, "reinstall-review-admin", input, check)
	if err != nil || next.ID == result.ID {
		t.Fatalf("recheck: %v", err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_entry_checks`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("history: %d %v", count, err)
	}
	directory, clock := s.dataDir, s.now
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.now = clock
	plan, err = s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Applications[0].Preparation.EntryCheck.ID != next.ID || !plan.Applications[0].Preparation.EntryCheck.Current {
		t.Fatalf("restart lost evidence: %v", err)
	}
}

func TestAgentReinstallEntryCheckReportsFailures(t *testing.T) {
	s, node, input := reinstallEntryCheckFixture(t)
	ctx := context.Background()
	for _, failure := range []string{"dns_pending", "tls_pending"} {
		result, err := s.verifyAgentReinstallEntry(ctx, node.ID, "reinstall-review-admin", input, func(context.Context, AgentReinstallEntryCheckResult) string { return failure })
		if err != nil || result.State != "pending" || !result.Current || result.Entries[0].State != failure {
			t.Fatalf("failure evidence: %+v %v", result, err)
		}
	}
}

func TestAgentReinstallEntryCheckFencesChangedReview(t *testing.T) {
	for _, at := range []string{"before", "during", "after"} {
		for _, mode := range []string{"identity", "route", "credential", "listener", "approval", "runtime"} {
			t.Run(at+"/"+mode, func(t *testing.T) {
				s, node, input := reinstallEntryCheckFixture(t)
				change := func() {
					var query string
					switch mode {
					case "identity":
						query = `UPDATE agents SET x25519_public_key=randomblob(32)`
					case "route":
						query = `UPDATE publications SET sni_hostname='changed.example.test'`
					case "credential":
						query = `UPDATE meridian_credentials SET enabled=0`
					case "listener":
						query = `UPDATE node_listener_states SET desired_revision=desired_revision+1`
					case "approval":
						query = `UPDATE agent_reinstall_network_approvals SET approval_json=json_set(approval_json,'$.profile.publicAddress','198.51.100.99')`
					case "runtime":
						query = `UPDATE application_commands SET state='failed' WHERE id LIKE 'reinstall-runtime-%'`
					}
					if _, err := s.db.Exec(query); err != nil {
						t.Fatal(err)
					}
				}
				if at == "before" {
					change()
				}
				probes := 0
				_, err := s.verifyAgentReinstallEntry(context.Background(), node.ID, "reinstall-review-admin", input, func(context.Context, AgentReinstallEntryCheckResult) string {
					probes++
					if at == "during" {
						change()
					}
					return "passed"
				})
				if at == "after" {
					if err != nil {
						t.Fatal(err)
					}
					change()
					plan, err := s.AgentReinstallPlan(context.Background(), node.ID)
					if err != nil {
						t.Fatal(err)
					}
					saved := plan.Applications[0].Preparation.EntryCheck
					if saved == nil || saved.Current {
						t.Fatalf("changed evidence current: %+v", saved)
					}
				} else {
					if err == nil || at == "before" && probes != 0 {
						t.Fatalf("accepted stale check (%d probes): %v", probes, err)
					}
					var count int
					if err = s.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_entry_checks`).Scan(&count); err != nil || count != 0 {
						t.Fatalf("stored stale evidence: %d %v", count, err)
					}
				}
			})
		}
	}
}

func TestAgentReinstallEntryCheckRejectsUnreviewedAndExpiredEvidence(t *testing.T) {
	s, node, input := reinstallEntryCheckFixture(t)
	ctx := context.Background()
	for _, mode := range []string{"administrator", "revision", "operation", "application"} {
		denied := input
		admin := "reinstall-review-admin"
		switch mode {
		case "administrator":
			admin = "different-admin"
		case "revision":
			denied.PlanRevision = "stale"
		case "operation":
			denied.OperationID = "different"
		case "application":
			denied.ApplicationID = "different"
		}
		if _, err := s.verifyAgentReinstallEntry(ctx, node.ID, admin, denied, func(context.Context, AgentReinstallEntryCheckResult) string {
			t.Fatal("unreviewed probe")
			return "passed"
		}); err == nil {
			t.Fatalf("accepted %s", mode)
		}
	}
	if _, err := s.verifyAgentReinstallEntry(ctx, node.ID, "reinstall-review-admin", input, func(context.Context, AgentReinstallEntryCheckResult) string { return "passed" }); err != nil {
		t.Fatal(err)
	}
	now := s.now()
	s.now = func() time.Time { return now.Add(31 * time.Minute) }
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Applications[0].Preparation.EntryCheck.Current {
		t.Fatalf("expired check current: %v", err)
	}
}

func TestAgentReinstallEntryDNSRejectsOldOrUnapprovedAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, address string
		answers       []string
		want          bool
	}{
		{"approved", "198.51.100.8", []string{"198.51.100.8"}, true},
		{"dual old", "198.51.100.8", []string{"198.51.100.8", "198.51.100.7"}, false},
		{"other family", "198.51.100.8", []string{"198.51.100.8", "2001:db8::8"}, false},
		{"empty", "198.51.100.8", nil, false}, {"private", "10.0.0.8", []string{"10.0.0.8"}, false},
		{"private network", "100.64.0.8", []string{"100.64.0.8"}, false}, {"invalid", "invalid", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addresses := []net.IPAddr{}
			for _, a := range tc.answers {
				addresses = append(addresses, net.IPAddr{IP: net.ParseIP(a)})
			}
			if got := reinstallEntryDNSMatches(tc.address, addresses); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestAgentReinstallEntryCannotBePublishedByOrdinaryVerification(t *testing.T) {
	for _, nodeRole := range []string{"application", "entry"} {
		t.Run(nodeRole, func(t *testing.T) {
			s, node, _ := reinstallEntryCheckFixture(t)
			ctx := context.Background()
			if nodeRole == "entry" {
				// The recovered ingress must be fenced even if the application belongs to
				// another healthy node.
				enrollment, err := s.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: testSiteID(t, s), Name: "Node B", CenterURL: "https://center.example.test"})
				if err != nil {
					t.Fatal(err)
				}
				other, err := s.EnrollAgent(ctx, enrollment.Token, Version, "linux", "amd64", testAgentPublicKey(t))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`UPDATE applications SET node_id=?`, other.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.VerifyPublication(ctx, "restored-entry"); err == nil {
				t.Fatal("ordinary probe bypassed recovery")
			}
			// Simulate completion of an ordinary probe which started before replacement.
			if _, err := s.db.Exec(`UPDATE applications SET status='running'; UPDATE services SET status='ready'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.markPublicationReady(ctx, "restored-entry", 0); err != nil {
				t.Fatal(err)
			}
			if _, err := s.recordPublicationVerification(ctx, "restored-entry", 0, "stale result"); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := s.db.QueryRow(`SELECT status FROM publications WHERE id='restored-entry'`).Scan(&status); err != nil || status != "degraded" {
				t.Fatalf("late result changed recovery: %s %v", status, err)
			}
			if blocked, err := agentReinstallBlocked(ctx, s.db, node.ID); err != nil || !blocked {
				t.Fatal("recovery fence lost")
			}
		})
	}
}
