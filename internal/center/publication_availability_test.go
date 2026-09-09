package center

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestPublicationAddressFailureIsIsolatedToOneEntry(t *testing.T) {
	for _, test := range []struct {
		name, kind, status, address, lastError string
		missing, actionRequired                bool
	}{
		{name: "missing LAN", kind: publicationLAN, status: "ready", missing: true},
		{name: "missing private network", kind: publicationHeadscale, status: "ready", missing: true},
		{name: "missing public", kind: publicationPublic, status: "ready", missing: true},
		{name: "pending shared listener", kind: publicationShared443, status: "pending", missing: true},
		{name: "empty public address", kind: publicationPublic, status: "ready"},
		{name: "invalid public address", kind: publicationPublic, status: "ready", address: "not-an-ip"},
		{name: "unsupported IPv6 address", kind: publicationPublic, status: "ready", address: "2001:db8::1"},
		{name: "stopped stays stopped", kind: publicationPublic, status: "stopped", missing: true},
		{name: "security failure is retained", kind: publicationPublic, status: "failed", missing: true, actionRequired: true, lastError: "security policy requires attention"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := seedVerificationPublication(t, store, test.kind, "manual", 1, 1, test.status)
			healthy := enrollOrchestrationNode(t, store, "healthy-entry", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.41", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.41", LANAddress: "10.0.0.41", EnabledKinds: []string{networking.KindLAN}})
			if _, err := store.db.ExecContext(ctx, `UPDATE services SET protocol = 'http' WHERE id = 'verification-service'`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `INSERT INTO publications(id, service_id, kind, ingress_owner, entry_node_id, hostname, dns_provider, desired_revision, applied_revision, status, created_at, updated_at)
				SELECT 'healthy-publication', service_id, 'lan_gateway', 'site_gateway', ?, 'healthy.example.test', 'manual', 1, 1, 'ready', created_at, updated_at FROM publications WHERE id = 'verification-publication'`, healthy.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE publications SET last_error = ?, action_required = ? WHERE id = 'verification-publication'`, test.lastError, test.actionRequired); err != nil {
				t.Fatal(err)
			}
			if test.missing {
				if _, err := store.db.ExecContext(ctx, `DELETE FROM agent_network_profiles WHERE agent_id = ?`, node.ID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := store.db.ExecContext(ctx, `UPDATE agent_network_profiles SET public_address = ? WHERE agent_id = ?`, test.address, node.ID); err != nil {
				t.Fatal(err)
			}
			values, err := store.ListPublications(ctx)
			if err != nil || len(values) != 2 {
				t.Fatalf("entry failure broke collection: count=%d err=%v", len(values), err)
			}
			for _, value := range values {
				detail, err := store.Publication(ctx, value.ID)
				if err != nil || !reflect.DeepEqual(value, detail) {
					t.Fatalf("detail/list mismatch: list=%+v detail=%+v err=%v", value, detail, err)
				}
				if value.ID == "healthy-publication" {
					if value.Status != "ready" || value.AccessURL == "" || value.DNSRecord == nil || value.DNSRecord.Value != "10.0.0.41" {
						t.Fatalf("healthy entry changed: %+v", value)
					}
					continue
				}
				wantStatus, wantError := "degraded", "Node network is recovering"
				if test.status == "stopped" || test.status == "failed" {
					wantStatus, wantError = test.status, test.lastError
				}
				if value.Status != wantStatus || value.LastError != wantError || value.ActionRequired != test.actionRequired || value.AccessURL != "" || value.DNSRecord != nil || value.SecurityCheck != nil {
					t.Fatalf("unavailable entry was not fail-closed: %+v", value)
				}
			}
			var persistedStatus, persistedError string
			if err := store.db.QueryRowContext(ctx, `SELECT status, last_error FROM publications WHERE id = 'verification-publication'`).Scan(&persistedStatus, &persistedError); err != nil {
				t.Fatal(err)
			}
			if persistedStatus != test.status || persistedError != test.lastError {
				t.Fatal("reading an entry mutated desired state")
			}
		})
	}
}

func TestPublicationDatabaseFailureIsNotTreatedAsUnavailableAddress(t *testing.T) {
	store := openOrchestrationStore(t)
	seedVerificationPublication(t, store, publicationPublic, "manual", 1, 1, "ready")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.ListPublications(ctx); err == nil {
		t.Fatal("closed database was hidden by list availability handling")
	}
	if _, err := store.Publication(ctx, "verification-publication"); err == nil {
		t.Fatal("closed database was hidden by detail availability handling")
	}
	if _, err := store.publicationDNSRecord(ctx, PublicationView{ID: "verification-publication", Kind: publicationPublic}); err == nil || errors.Is(err, errPublicationEntryAddressUnavailable) {
		t.Fatalf("database failure became an unavailable address: %v", err)
	}
}

func TestMissingPublicationAddressHidesPreviousSecurityResult(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	seedRealitySecurityCheckPublication(t, store)
	store.dialRealitySecurityProbe = func(_ context.Context, _, serverName string) error {
		if serverName == "www.intel.com" {
			return nil
		}
		return errors.New("rejected")
	}
	if _, err := store.RunRealitySecurityCheck(ctx, "verification-publication", "security-admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE agent_network_profiles SET public_address = '' WHERE agent_id = (SELECT entry_node_id FROM publications WHERE id = 'verification-publication')`); err != nil {
		t.Fatal(err)
	}
	values, err := store.ListPublications(ctx)
	if err != nil || len(values) != 1 {
		t.Fatalf("missing entry broke collection: count=%d err=%v", len(values), err)
	}
	detail, err := store.Publication(ctx, "verification-publication")
	if err != nil || detail.SecurityCheck != nil || values[0].SecurityCheck != nil {
		t.Fatalf("unavailable entry still advertises an old safety result: list=%+v detail=%+v err=%v", values, detail, err)
	}
	// Address recovery is not a read-side state change: the original desired
	// status and revision remain intact, and no replacement profile is guessed.
	if _, err := store.db.ExecContext(ctx, `UPDATE agent_network_profiles SET public_address = '203.0.113.40' WHERE agent_id = (SELECT entry_node_id FROM publications WHERE id = 'verification-publication')`); err != nil {
		t.Fatal(err)
	}
	detail, err = store.Publication(ctx, "verification-publication")
	if err != nil || detail.Status != "ready" || detail.DNSRecord == nil || detail.DNSRecord.Value != "203.0.113.40" {
		t.Fatalf("confirmed address did not restore the read model: %+v err=%v", detail, err)
	}
}
