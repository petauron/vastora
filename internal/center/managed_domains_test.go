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

func TestManagedDomainAdoptionRetryAndSafeRetirement(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	n := seedVerificationPublication(t, s, publicationShared443, "cloudflare", 1, 1, "ready")
	ctx := context.Background()
	const host = "abcdefghijklmnopqrstuvwxyz.example.com"
	if _, err := s.db.Exec(`UPDATE agent_network_profiles SET public_address='1.1.1.1' WHERE agent_id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE publications SET hostname=?,dns_record_id='old-record' WHERE id='verification-publication'`, host); err != nil {
		t.Fatal(err)
	}
	record := managedCloudflareRecord{ID: "old-record", Name: host, Type: "A", Content: "1.1.1.1"}
	writes, deletes := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch r.Method {
		case http.MethodGet:
			result = []managedCloudflareRecord{record}
		case http.MethodPut:
			writes++
			id := record.ID
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
				t.Error(err)
			}
			record.ID = id
			result = record
		case http.MethodDelete:
			deletes++
			result = map[string]any{}
		default:
			t.Errorf("unexpected DNS operation: %s", r.Method)
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result, "errors": []any{}})
	}))
	defer api.Close()
	s.cloudflareOAuth = cloudflareOAuthConfig{APIURL: api.URL, HTTPClient: api.Client()}
	storeCloudflareOAuthIntegration(t, s, cloudflareOAuthToken{AccessToken: "synthetic", RefreshToken: "synthetic", Scope: "dns.write", ExpiresAt: time.Now().Add(time.Hour)})
	input := ManagedDomainInput{ApplicationID: "verification-app", Key: "entry", Address: "1.1.1.1"}

	// A same-node application cannot take a historical record merely because
	// its address matches. Only the existing verified handover authorizes it.
	if _, err := s.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,created_at,updated_at) SELECT 'replacement-app','Native replacement',node_id,site_id,'test/native-replacement','running',created_at,updated_at FROM applications WHERE id='verification-app'`); err != nil {
		t.Fatal(err)
	}
	input.ApplicationID = "replacement-app"
	input.Hostname = host
	if _, err := s.EnsureManagedDomain(ctx, input); err == nil || writes != 0 {
		t.Fatal("unrelated application adopted an existing record")
	}
	if _, err := s.db.Exec(`INSERT INTO application_execution_retirements(application_id,replacement_application_id,evidence_json,verified_at,created_at) VALUES('verification-app','replacement-app','{}','verified','before')`); err != nil {
		t.Fatal(err)
	}
	first, err := s.EnsureManagedDomain(ctx, input)
	if err != nil || first.Hostname != host || first.Status != "ready" || first.Revision != 1 {
		t.Fatalf("adopt: %+v %v", first, err)
	}
	second, err := s.EnsureManagedDomain(ctx, input)
	if err != nil || first.ID != second.ID || first.Hostname != second.Hostname || second.Revision != 1 {
		t.Fatalf("retry: %+v %v", second, err)
	}
	var oldID, linked string
	if err := s.db.QueryRow(`SELECT dns_record_id,managed_domain_id FROM publications WHERE id='verification-publication'`).Scan(&oldID, &linked); err != nil || oldID != "" || linked != first.ID {
		t.Fatalf("DNS lifecycle not transferred: %q %q %v", oldID, linked, err)
	}
	if s.RetireManagedDomain(ctx, first.ID) == nil || deletes != 0 {
		t.Fatal("active publication lost DNS")
	}
	input.Address = "8.8.8.8"
	if _, err := s.db.Exec(`UPDATE agent_network_profiles SET public_address=? WHERE agent_id=?`, input.Address, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureManagedDomain(ctx, input); err == nil {
		t.Fatal("unfenced address change accepted")
	}
	input.ExpectedRevision = 1
	updated, err := s.EnsureManagedDomain(ctx, input)
	if err != nil || updated.Revision != 2 || updated.Hostname != host || record.Content != "8.8.8.8" {
		t.Fatalf("update: %+v %v", updated, err)
	}
	record.Comment = "other-owner"
	before := writes
	if _, err := s.EnsureManagedDomain(ctx, input); err == nil || writes != before {
		t.Fatal("foreign marker overwritten")
	}
	if _, err := s.db.Exec(`UPDATE publications SET status='stopped' WHERE id='verification-publication'`); err != nil {
		t.Fatal(err)
	}
	if s.RetireManagedDomain(ctx, first.ID) == nil || deletes != 0 {
		t.Fatal("foreign DNS deleted")
	}
	record.Comment = "vastora-managed-domain:" + first.ID
	if err := s.RetireManagedDomain(ctx, first.ID); err != nil || deletes != 1 {
		t.Fatalf("retire: %v deletes=%d", err, deletes)
	}
}

func TestManagedDomainRegionNameAndLostResponseReuse(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	n := seedVerificationPublication(t, s, publicationShared443, "manual", 1, 1, "ready")
	if _, err := s.db.Exec(`UPDATE agent_network_profiles SET public_address='1.1.1.1' WHERE agent_id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	var record managedCloudflareRecord
	creates := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var result any
		if r.Method == http.MethodGet {
			list := []managedCloudflareRecord{}
			if record.ID != "" {
				list = append(list, record)
			}
			result = list
		} else {
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
				t.Error(err)
			}
			record.ID = "created-record"
			result = record
			if r.Method == http.MethodPost {
				creates++
				w.WriteHeader(502)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result, "errors": []any{}})
	}))
	defer api.Close()
	s.cloudflareOAuth = cloudflareOAuthConfig{APIURL: api.URL, HTTPClient: api.Client()}
	storeCloudflareOAuthIntegration(t, s, cloudflareOAuthToken{AccessToken: "synthetic", RefreshToken: "synthetic", Scope: "dns.write", ExpiresAt: time.Now().Add(time.Hour)})
	in := ManagedDomainInput{ApplicationID: "verification-app", Key: "site", Prefix: "jp", Address: "1.1.1.1"}
	first, err := s.EnsureManagedDomain(context.Background(), in)
	if err == nil || first.Status != "failed" || !strings.HasPrefix(first.Hostname, "jp-") {
		t.Fatalf("lost response: %+v %v", first, err)
	}
	again, err := s.EnsureManagedDomain(context.Background(), in)
	if err != nil || again.Status != "ready" || again.ID != first.ID || again.Hostname != first.Hostname || creates != 1 {
		t.Fatalf("recovery: %+v %v creates=%d", again, err, creates)
	}
}

func TestManagedDomainExplicitFamilyMigrationPreservesRecordAndRecoversLostResponse(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	n := seedVerificationPublication(t, s, publicationShared443, "manual", 1, 1, "ready")
	const oldAddress = "2001:4860:4860::8888"
	if _, err := s.db.Exec(`UPDATE agent_network_profiles SET public_address=? WHERE agent_id=?`, oldAddress, n.ID); err != nil {
		t.Fatal(err)
	}
	var record managedCloudflareRecord
	writes := 0
	loseResponse := false
	failBeforeWrite := false
	deletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var result any
		if r.Method == http.MethodPut && failBeforeWrite {
			failBeforeWrite = false
			w.WriteHeader(502)
			return
		}
		if r.Method == http.MethodGet {
			rows := []managedCloudflareRecord{}
			if record.ID != "" {
				rows = append(rows, record)
			}
			result = rows
		} else if r.Method == http.MethodPost || r.Method == http.MethodPut {
			writes++
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
				t.Error(err)
			}
			record.ID = "owned-record"
			result = record
			if loseResponse {
				loseResponse = false
				w.WriteHeader(502)
				return
			}
		} else if r.Method == http.MethodDelete {
			deletes++
			result = map[string]any{}
		} else {
			t.Errorf("unexpected DNS operation %s", r.Method)
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result, "errors": []any{}})
	}))
	defer server.Close()
	s.cloudflareOAuth = cloudflareOAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}
	storeCloudflareOAuthIntegration(t, s, cloudflareOAuthToken{AccessToken: "synthetic", RefreshToken: "synthetic", Scope: "dns.write", ExpiresAt: time.Now().Add(time.Hour)})
	in := ManagedDomainInput{ApplicationID: "verification-app", Key: "reality-site", Prefix: "sg", Address: oldAddress}
	initial, err := s.EnsureManagedDomain(context.Background(), in)
	if err != nil || initial.Type != "AAAA" || initial.Status != "ready" {
		t.Fatal(initial, err)
	}
	if _, err = s.db.Exec(`UPDATE agent_network_profiles SET public_address='8.8.8.8' WHERE agent_id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	in.Address = "8.8.8.8"
	in.Hostname = initial.Hostname
	in.ExpectedRevision = initial.Revision
	before := writes
	if _, err = s.EnsureManagedDomain(context.Background(), in); err == nil || writes != before {
		t.Fatal("implicit family change accepted")
	}
	in.MigrateAddressFamily = true
	if _, err = s.EnsureManagedDomain(context.Background(), in); err == nil || writes != before {
		t.Fatal("unverified old address accepted")
	}
	in.ExpectedRecordAddress = oldAddress
	in.ExpectedRevision = 0
	if _, err = s.EnsureManagedDomain(context.Background(), in); err == nil || writes != before {
		t.Fatal("stale revision accepted")
	}
	in.ExpectedRevision = initial.Revision
	loseResponse = true
	if _, err = s.EnsureManagedDomain(context.Background(), in); err == nil {
		t.Fatal("lost response reported as success")
	}
	updated, err := s.EnsureManagedDomain(context.Background(), in)
	if err != nil || updated.ID != initial.ID || updated.RecordID != initial.RecordID || updated.Hostname != initial.Hostname || updated.Revision != 2 || updated.Status != "ready" || record.Type != "A" || record.Content != in.Address {
		t.Fatal("family migration failed", updated, err)
	}
	in.Address = oldAddress
	in.ExpectedRecordAddress = "8.8.8.8"
	in.ExpectedRevision = updated.Revision
	if _, err = s.db.Exec(`UPDATE agent_network_profiles SET public_address=? WHERE agent_id=?`, oldAddress, n.ID); err != nil {
		t.Fatal(err)
	}
	record.Comment = "foreign-owner"
	before = writes
	if _, err = s.EnsureManagedDomain(context.Background(), in); err == nil || writes != before {
		t.Fatal("foreign record overwritten during migration")
	}
	record.Comment = "vastora-managed-domain:" + initial.ID
	restored, err := s.EnsureManagedDomain(context.Background(), in)
	if err != nil || restored.RecordID != initial.RecordID || restored.Hostname != initial.Hostname || restored.Revision != 3 || restored.Status != "ready" || record.Type != "AAAA" || record.Content != oldAddress {
		t.Fatal("retry did not retain migration identity", restored, err)
	}
	// A migration that failed before the provider write retains the old owned
	// tuple for safe retirement; a changed owner must still prevent deletion.
	if _, err = s.db.Exec(`UPDATE agent_network_profiles SET public_address='8.8.8.8' WHERE agent_id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	in.Address = "8.8.8.8"
	in.ExpectedRecordAddress = oldAddress
	in.ExpectedRevision = restored.Revision
	failBeforeWrite = true
	if _, err = s.EnsureManagedDomain(context.Background(), in); err == nil {
		t.Fatal("failed provider write reported ready")
	}
	record.Comment = "foreign-owner"
	if err = s.RetireManagedDomain(context.Background(), initial.ID); err == nil || deletes != 0 {
		t.Fatal("foreign prior-family record deleted")
	}
	record.Comment = "vastora-managed-domain:" + initial.ID
	if err = s.RetireManagedDomain(context.Background(), initial.ID); err != nil || deletes != 1 {
		t.Fatal("owned prior-family retirement failed", err, deletes)
	}

}
