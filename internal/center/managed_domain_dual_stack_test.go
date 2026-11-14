package center

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestManagedDomainDualStackOwnsBothFamiliesAcrossPartialFailure(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	n := seedVerificationPublication(t, s, publicationShared443, "manual", 1, 1, "ready")
	ctx := context.Background()
	if _, err := s.db.Exec(`UPDATE agent_network_profiles SET public_address='1.1.1.1' WHERE agent_id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO agent_network_candidates(agent_id,address,interface_name,kind,observed_at) VALUES(?,'2001:4860:4860::8888','eth0','public',?)`, n.ID, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	records := map[string]managedCloudflareRecord{}
	creates := 0
	lost := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch r.Method {
		case "GET":
			list := []managedCloudflareRecord{}
			for _, v := range records {
				list = append(list, v)
			}
			result = list
		case "POST", "PUT":
			var v managedCloudflareRecord
			if json.NewDecoder(r.Body).Decode(&v) != nil {
				t.Error("invalid provider request")
			}
			v.ID = v.Type + "-owned"
			if r.Method == "POST" {
				creates++
			}
			records[v.Type] = v
			result = v
			if v.Type == "AAAA" && !lost {
				lost = true
				w.WriteHeader(502)
				return
			}
		case "DELETE":
			for typ, v := range records {
				if strings.HasSuffix(r.URL.Path, "/"+v.ID) {
					delete(records, typ)
				}
			}
			result = map[string]any{}
		default:
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result, "errors": []any{}})
	}))
	defer api.Close()
	s.cloudflareOAuth = cloudflareOAuthConfig{APIURL: api.URL, HTTPClient: api.Client()}
	storeCloudflareOAuthIntegration(t, s, cloudflareOAuthToken{AccessToken: "synthetic", RefreshToken: "synthetic", Scope: "dns.write", ExpiresAt: time.Now().Add(time.Hour)})
	input := ManagedDomainInput{ApplicationID: "verification-app", Key: "ingress", Address: "1.1.1.1"}
	first, err := s.EnsureManagedDomain(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	secondary := "2001:4860:4860::8888"
	input.SecondaryAddress = &secondary
	input.Hostname = first.Hostname
	input.ExpectedRevision = first.Revision
	partial, err := s.EnsureManagedDomain(ctx, input)
	if err == nil || partial.Status == "ready" || len(records) != 2 {
		t.Fatal("partial pair incorrectly marked ready", err)
	}
	pair, err := s.EnsureManagedDomain(ctx, input)
	if err != nil || pair.Status != "ready" || pair.Revision != 2 || pair.ID != first.ID || pair.RecordID != first.RecordID || creates != 2 {
		t.Fatal("pair retry identity", pair, err, creates)
	}
	foreign := records["AAAA"]
	foreign.Comment = "foreign-owner"
	records["AAAA"] = foreign
	if s.reconcileManagedDomainLocked(ctx, pair.ID) == nil || len(records) != 2 {
		t.Fatal("foreign secondary was deleted or primary partially removed")
	}
	foreign.Comment = "vastora-managed-domain:" + pair.ID
	records["AAAA"] = foreign
	// Explicitly clearing the second family keeps the original A record identity.
	empty := ""
	input.SecondaryAddress = &empty
	input.ExpectedRevision = pair.Revision
	single, err := s.EnsureManagedDomain(ctx, input)
	if err != nil || single.SecondaryAddress != "" || len(records) != 1 || single.RecordID != first.RecordID {
		t.Fatal("remove second family", single, err)
	}
	input.SecondaryAddress = &secondary
	input.ExpectedRevision = single.Revision
	pairedAgain, e := s.EnsureManagedDomain(ctx, input)
	if e != nil {
		t.Fatal(e)
	}
	foreign = records["AAAA"]
	foreign.Comment = "foreign-owner"
	records["AAAA"] = foreign
	if s.RetireManagedDomain(ctx, pairedAgain.ID) == nil || len(records) != 2 {
		t.Fatal("retirement touched a foreign pair")
	}
	foreign.Comment = "vastora-managed-domain:" + pairedAgain.ID
	records["AAAA"] = foreign
	if err = s.RetireManagedDomain(ctx, single.ID); err != nil || len(records) != 0 {
		t.Fatal("retirement", err)
	}
}
func TestPublicIngressSchema133PreservesCertificateAndDomain(t *testing.T) {
	s, _, app := readyMigrationSource(t)
	ctx := context.Background()
	before, err := s.NodeSite(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	dir := s.dataDir
	if _, err = s.db.Exec(`ALTER TABLE managed_domains DROP COLUMN secondary_address; ALTER TABLE managed_domains DROP COLUMN secondary_record_id; ALTER TABLE managed_domains DROP COLUMN secondary_expected_address; ALTER TABLE node_sites DROP COLUMN tls_loopback_only; DELETE FROM goose_db_version WHERE version_id=133; PRAGMA user_version=132;`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	after, err := m.NodeSite(ctx, app)
	if err != nil || after != before {
		t.Fatal("schema133 changed original certificate", err)
	}
	loopback := true
	v, err := m.configureNodeSite(ctx, app, nodeSiteInput{Hostname: after.Hostname, Port: after.Port, CADirectory: after.CADirectory, TLSLoopbackOnly: &loopback})
	if err != nil || !v.TLSLoopbackOnly || v.Revision <= after.Revision || v.Fingerprint != after.Fingerprint {
		t.Fatal("backend-only certificate transition", v, err)
	}
}
