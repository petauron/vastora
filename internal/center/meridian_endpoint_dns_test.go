package center

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestCreateMeridianEndpointManagesConfiguredCloudflareDNS(t *testing.T) {
	for _, test := range []struct {
		name         string
		cloudflare   bool
		apiFailure   bool
		revisionRace bool
		hostname     string
		wantError    bool
	}{
		{name: "manual without an integration", hostname: "entry.example.test"},
		{name: "managed DNS", cloudflare: true, hostname: "entry.example.com"},
		{name: "runtime revision race is retried", cloudflare: true, revisionRace: true, hostname: "entry.example.com"},
		{name: "API failure does not downgrade to manual", cloudflare: true, apiFailure: true, hostname: "entry.example.com"},
		{name: "outside configured zone fails before commit", cloudflare: true, hostname: "entry.other.test", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := openOrchestrationStore(t)
			t.Cleanup(func() { _ = store.Close() })
			var created atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !test.cloudflare || test.wantError {
					t.Error("unexpected external DNS operation")
				}
				if test.apiFailure {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"not authorized"}]}`))
					return
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /zones/zone/dns_records":
					if r.URL.Query().Get("name") != test.hostname {
						t.Error("DNS lookup escaped the entry hostname")
					}
					_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
				case "POST /zones/zone/dns_records":
					var record struct {
						Type    string `json:"type"`
						Name    string `json:"name"`
						Content string `json:"content"`
						Proxied bool   `json:"proxied"`
					}
					if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
						t.Error(err)
					}
					if record.Type != "A" || record.Name != test.hostname || record.Content != "203.0.113.42" || record.Proxied {
						t.Errorf("unexpected DNS record: %+v", record)
					}
					if created.Add(1) == 1 && test.revisionRace {
						if _, err := store.db.Exec(`UPDATE publications SET desired_revision=desired_revision+1 WHERE hostname=?`, test.hostname); err != nil {
							t.Error(err)
						}
					}
					_, _ = w.Write([]byte(`{"success":true,"result":{"id":"entry-dns-record"}}`))
				case "DELETE /zones/zone/dns_records/entry-dns-record":
					if !test.revisionRace {
						t.Error("unexpected DNS compensation")
					}
					_, _ = w.Write([]byte(`{"success":true,"result":{"id":"entry-dns-record"}}`))
				default:
					t.Errorf("unexpected DNS operation: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			if test.cloudflare {
				storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "test-access", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(time.Hour)})
				store.cloudflareOAuth.APIURL = server.URL
				store.cloudflareOAuth.HTTPClient = server.Client()
			}
			profile := networking.Profile{ServiceAddress: "10.0.0.42", LANAddress: "10.0.0.42", PublicAddress: "203.0.113.42", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true}
			node := enrollOrchestrationNode(t, store, "entry-dns", NodeCapabilities{Docker: true}, []networking.Candidate{
				{Address: profile.ServiceAddress, Interface: "eth0", Kind: networking.KindLAN},
				{Address: profile.PublicAddress, Interface: "eth0", Kind: networking.KindPublic},
			}, profile)
			stamp := store.now().UTC().Format(time.RFC3339Nano)
			const applicationID = "managed-entry-dns"
			if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
				VALUES(?,?,?,?,?,'','running','docker','',?,?)`, applicationID, "DNS entry", node.ID, testSiteID(t, store), meridianAppKey, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			verified := seedVerifiedRealityInput(t, store, ctx, RealityCommandInput{ApplicationID: applicationID, TargetHost: "www.example.com", ServerName: "www.example.com"})
			endpoint, err := store.CreateMeridianEndpoint(ctx, MeridianEndpointInput{ApplicationID: applicationID, AdvertiseHost: test.hostname, RegionCode: "JP", Name: "Example entry", VerificationID: verified.VerificationID, TargetIP: verified.TargetIP, TargetHost: verified.TargetHost, ServerName: verified.ServerName})
			if test.wantError {
				if err == nil {
					t.Fatal("out-of-zone entry accepted")
				}
				var count int
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM meridian_endpoints WHERE application_id=?`, applicationID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("rejected entry persisted: count=%d err=%v", count, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.revisionRace {
				var id, record string
				if err := store.db.QueryRow(`SELECT id,dns_record_id FROM publications WHERE service_id=?`, endpoint.ServiceID).Scan(&id, &record); err != nil || record != "" {
					t.Fatalf("stale DNS operation was not compensated: err=%v", err)
				}
				// On-demand/background verification must retry DNS for the new
				// revision even though the publication is pending, not failed.
				if _, err := store.db.Exec(`UPDATE meridian_endpoints SET status='ready',runtime_healthy=1,applied_revision=desired_revision WHERE id=?`, endpoint.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`UPDATE services SET status='ready' WHERE id=?`, endpoint.ServiceID); err != nil {
					t.Fatal(err)
				}
				if _, err := store.VerifyPublication(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			var provider, recordID, status string
			if err := store.db.QueryRow(`SELECT dns_provider,dns_record_id,status FROM publications WHERE service_id=?`, endpoint.ServiceID).Scan(&provider, &recordID, &status); err != nil {
				t.Fatal(err)
			}
			if test.cloudflare {
				if provider != "cloudflare" {
					t.Fatalf("managed entry downgraded to %q", provider)
				}
				if test.apiFailure {
					if status != "failed" || recordID != "" || created.Load() != 0 {
						t.Fatalf("failed DNS misreported: status=%s record=%s created=%d", status, recordID, created.Load())
					}
				} else {
					wantCreated := int32(1)
					if test.revisionRace {
						wantCreated = 2
					}
					if recordID != "entry-dns-record" || created.Load() != wantCreated || status == "ready" {
						t.Fatalf("DNS ownership or readiness wrong: record=%s created=%d status=%s", recordID, created.Load(), status)
					}
				}
			} else if provider != "manual" || recordID != "" || created.Load() != 0 {
				t.Fatalf("manual entry changed unexpectedly: provider=%s record=%s created=%d", provider, recordID, created.Load())
			}
		})
	}
}
