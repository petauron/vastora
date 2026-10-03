package center

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type reinstallDNSTransport func(*http.Request) (*http.Response, error)

func (f reinstallDNSTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type reinstallDNSAPI struct {
	t                        *testing.T
	records                  []cloudflareDNSRecord
	reads, writes            int
	failWrite, commitFailure bool
	afterRead, afterWrite    func()
}

func (api *reinstallDNSAPI) transport(r *http.Request) (*http.Response, error) {
	api.t.Helper()
	status := http.StatusOK
	var result any
	switch r.Method {
	case http.MethodGet:
		api.reads++
		if r.URL.Path != "/zones/zone/dns_records" || r.URL.Query().Get("name") != "entry.example.test" {
			api.t.Fatalf("unexpected read %s", r.URL)
		}
		result = api.records
		if api.afterRead != nil {
			api.afterRead()
		}
	case http.MethodPut:
		api.writes++
		var record cloudflareDNSRecord
		if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
			api.t.Fatal(err)
		}
		if r.URL.Path != "/zones/zone/dns_records/owned-record" || record.Type != "A" || record.Name != "entry.example.test" || record.Content != "198.51.100.8" || record.Proxied {
			api.t.Fatalf("unreviewed write: %s %+v", r.URL, record)
		}
		if !api.failWrite || api.commitFailure {
			record.ID = "owned-record"
			api.records = []cloudflareDNSRecord{record}
		}
		if api.failWrite {
			status = http.StatusBadGateway
		}
		result = map[string]string{"id": "owned-record"}
		if api.afterWrite != nil {
			api.afterWrite()
		}
	default:
		api.t.Fatalf("unexpected external action: %s %s", r.Method, r.URL)
	}
	encoded, _ := json.Marshal(map[string]any{"success": status == http.StatusOK, "result": result, "errors": []any{}})
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(encoded))), Header: make(http.Header), Request: r}, nil
}

func reinstallDNSFixture(t *testing.T) (*Store, AgentCredential, AgentReinstallDNSInput, *reinstallDNSAPI) {
	t.Helper()
	s, node, input := reinstallEntryCheckFixture(t)
	if _, err := s.db.Exec(`UPDATE publications SET dns_provider='cloudflare',dns_record_id='owned-record' WHERE id='restored-entry'`); err != nil {
		t.Fatal(err)
	}
	storeCloudflareOAuthIntegration(t, s, cloudflareOAuthToken{AccessToken: "test-access", RefreshToken: "test-refresh", ExpiresAt: s.now().Add(time.Hour)})
	mock := &reinstallDNSAPI{t: t, records: []cloudflareDNSRecord{{ID: "owned-record", Name: "entry.example.test", Type: "A", Content: "198.51.100.7"}}}
	s.cloudflareOAuth.HTTPClient = &http.Client{Transport: reinstallDNSTransport(mock.transport)}
	s.cloudflareOAuth.APIURL = "https://api.example.test"
	plan, err := s.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	if _, err = s.ActivateAgentReinstallAccess(context.Background(), node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	plan, err = s.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, node, AgentReinstallDNSInput{OperationID: input.OperationID, PlanRevision: plan.Revision, ApplicationID: input.ApplicationID}, mock
}

func TestAgentReinstallDNSUpdatesOnlyOwnedRecordAndPreservesFence(t *testing.T) {
	s, node, input, api := reinstallDNSFixture(t)
	ctx := context.Background()
	result, err := s.MigrateAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || result.State != "succeeded" || !result.Current || result.Attempt != 1 || api.writes != 1 || api.reads != 2 {
		t.Fatalf("migration: %+v %v %d/%d", result, err, api.reads, api.writes)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Recovery.State != "review_required" || plan.Applications[0].Preparation.DNS.ID != result.ID || !plan.Applications[0].Preparation.DNS.Current {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("released fence: %v", err)
	}
	var status string
	if err = s.db.QueryRow(`SELECT status FROM publications WHERE id='restored-entry'`).Scan(&status); err != nil || status != "degraded" {
		t.Fatalf("published: %s %v", status, err)
	}
	again, err := s.MigrateAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || again.ID != result.ID || api.writes != 1 || api.reads != 2 {
		t.Fatalf("repeated mutation: %+v %v", again, err)
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
	again, err = s.MigrateAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || again.ID != result.ID {
		t.Fatalf("restart lost durable result: %+v %v", again, err)
	}
}

func TestAgentReinstallDNSLostWriteResponseRequiresReadOnlyInspection(t *testing.T) {
	s, node, input, api := reinstallDNSFixture(t)
	ctx := context.Background()
	api.failWrite = true
	api.commitFailure = true
	result, err := s.MigrateAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || result.State != "needs_review" || result.CanContinue || api.writes != 1 || api.reads != 1 {
		t.Fatalf("uncertain: %+v %v", result, err)
	}
	repeated, err := s.MigrateAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || repeated.ID != result.ID || api.writes != 1 || api.reads != 1 {
		t.Fatalf("replayed: %+v %v", repeated, err)
	}
	input.ExpectedAttempt = result.Attempt
	if _, err = s.MigrateAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("continued without inspection")
	}
	checked, err := s.InspectAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || checked.State != "succeeded" || checked.CanContinue || api.writes != 1 || api.reads != 2 {
		t.Fatalf("inspection wrote: %+v %v", checked, err)
	}
}

func TestAgentReinstallDNSExplicitContinuationRetainsAttempts(t *testing.T) {
	s, node, input, api := reinstallDNSFixture(t)
	ctx := context.Background()
	api.failWrite = true
	first, err := s.MigrateAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedAttempt = first.Attempt
	checked, err := s.InspectAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || !checked.CanContinue || checked.State != "needs_review" || api.writes != 1 {
		t.Fatalf("continue evidence: %+v %v", checked, err)
	}
	api.failWrite = false
	next, err := s.MigrateAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || next.State != "succeeded" || next.Attempt != 2 || next.ID == first.ID || api.writes != 2 {
		t.Fatalf("continuation: %+v %v", next, err)
	}
	if _, err = s.MigrateAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("reused attempt")
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_dns_migrations`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("lost history: %d %v", count, err)
	}
}

func TestAgentReinstallDNSRejectsChangedAuthorityAndOwnership(t *testing.T) {
	for _, mode := range []string{"admin", "revision", "record", "zone", "access", "source-key", "owned-by-other", "remote-id", "remote-address", "remote-AAAA", "remote-proxy", "remote-missing"} {
		t.Run(mode, func(t *testing.T) {
			s, node, input, api := reinstallDNSFixture(t)
			admin := "reinstall-review-admin"
			query := ""
			switch mode {
			case "admin":
				admin = "different-admin"
			case "revision":
				input.PlanRevision = strings.Repeat("f", 64)
			case "record":
				query = `UPDATE publications SET dns_record_id='different-record'`
			case "zone":
				query = `UPDATE network_integrations SET zone_id='different-zone' WHERE kind='cloudflare'`
			case "access":
				query = `UPDATE services SET endpoint='10.0.0.99:10443'`
			case "source-key":
				query = `UPDATE agents SET x25519_public_key=randomblob(32)`
			case "owned-by-other":
				query = `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,status,created_at,updated_at) SELECT 'other-reference',service_id,'headscale_gateway','site_gateway',entry_node_id,hostname,'','manual','pending',created_at,updated_at FROM publications WHERE id='restored-entry'`
			case "remote-id":
				api.records[0].ID = "someone-elses-record"
			case "remote-address":
				api.records[0].Content = "198.51.100.99"
			case "remote-AAAA":
				api.records = append(api.records, cloudflareDNSRecord{ID: "extra", Name: "entry.example.test", Type: "AAAA", Content: "2001:db8::1"})
			case "remote-proxy":
				api.records[0].Proxied = true
			case "remote-missing":
				api.records = nil
			}
			if query != "" {
				if _, err := s.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.MigrateAgentReinstallDNS(context.Background(), node.ID, admin, input)
			if err == nil && result.State == "succeeded" {
				t.Fatalf("accepted changed evidence: %+v", result)
			}
			if api.writes != 0 {
				t.Fatal("changed evidence caused write")
			}
		})
	}
}

func TestAgentReinstallDNSChangesDuringIOStayUnconfirmed(t *testing.T) {
	for _, phase := range []string{"read", "write"} {
		t.Run(phase, func(t *testing.T) {
			s, node, input, api := reinstallDNSFixture(t)
			change := func() {
				if _, err := s.db.Exec(`UPDATE publications SET status='stopped' WHERE id='restored-entry'`); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "read" {
				api.afterRead = change
			} else {
				api.afterWrite = change
			}
			result, err := s.MigrateAgentReinstallDNS(context.Background(), node.ID, "reinstall-review-admin", input)
			if err != nil || result.State != "needs_review" || result.Current || result.CanContinue {
				t.Fatalf("stale mutation declared successful: %+v %v", result, err)
			}
			if phase == "read" && api.writes != 0 {
				t.Fatal("wrote after authority changed")
			}
			plan, err := s.AgentReinstallPlan(context.Background(), node.ID)
			if err != nil || plan.Applications[0].Preparation.DNS.Current {
				t.Fatalf("lost stale receipt: %v", err)
			}
		})
	}
}

func TestAgentReinstallDNSPersistsIntentBeforeExternalWrite(t *testing.T) {
	s, node, input, api := reinstallDNSFixture(t)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_dns_intent BEFORE INSERT ON agent_reinstall_dns_migrations BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MigrateAgentReinstallDNS(context.Background(), node.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("ignored journal failure")
	}
	if api.reads != 0 || api.writes != 0 {
		t.Fatal("external action before durable approval")
	}
}

func TestAgentReinstallDNSManualRecordsNeverInvokeProvider(t *testing.T) {
	s, node, input, api := reinstallDNSFixture(t)
	if _, err := s.db.Exec(`UPDATE publications SET dns_provider='manual',dns_record_id='' WHERE id='restored-entry'`); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	if dns := plan.Applications[0].Preparation.DNS; dns == nil || dns.Entries[0].State != "manual" || dns.Entries[0].Address != "198.51.100.8" {
		t.Fatalf("missing manual instructions: %+v", dns)
	}
	if _, err = s.MigrateAgentReinstallDNS(context.Background(), node.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("attempted manual migration")
	}
	if api.reads != 0 || api.writes != 0 {
		t.Fatal("manual record invoked provider")
	}
}

func TestAgentReinstallDNSResultCommitFailureCanBeInspectedWithoutReplay(t *testing.T) {
	s, node, input, api := reinstallDNSFixture(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`CREATE TRIGGER reject_dns_result BEFORE UPDATE ON agent_reinstall_dns_migrations BEGIN SELECT RAISE(ABORT,'injected result failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MigrateAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("ignored result persistence failure")
	}
	if api.writes != 1 {
		t.Fatal("missing external effect")
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_dns_result`); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved := plan.Applications[0].Preparation.DNS
	if saved == nil || saved.State != "needs_review" || saved.CanContinue {
		t.Fatalf("lost uncertain intent: %+v", saved)
	}
	input.ExpectedAttempt = saved.Attempt
	result, err := s.InspectAgentReinstallDNS(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || result.State != "succeeded" || api.writes != 1 {
		t.Fatalf("inspection replayed mutation: %+v %v", result, err)
	}
}
