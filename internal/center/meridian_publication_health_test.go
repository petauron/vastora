package center

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

type meridianProbeFunc func(context.Context, deployapi.MeridianProbeRequest) error

func (probe meridianProbeFunc) VerifyMeridianEntry(ctx context.Context, input deployapi.MeridianProbeRequest) error {
	return probe(ctx, input)
}

func TestMeridianPublicationRequiresAuthenticatedProxyEvidence(t *testing.T) {
	for _, scenario := range []string{"unavailable", "proxy failure", "verified", "runtime changed", "credential disabled", "stale exit"} {
		t.Run(scenario, func(t *testing.T) {
			store := openMeridianSharedEndpointSnapshotFixture(t)
			if store.publicationVerificationTimeoutFor("snapshot-shared-publication") <= publicationVerificationTimeout {
				t.Fatal("authenticated verification still has the plain TLS timeout")
			}
			ctx := context.Background()
			stamp := store.now().UTC().Format(time.RFC3339Nano)
			if _, err := store.db.Exec(`UPDATE agents SET public_egress_address='1.1.1.1',public_egress_bind_address='1.1.1.1',public_egress_mode='direct',public_egress_observed_at=?
				WHERE id=(SELECT node_id FROM applications WHERE id='snapshot-shared-app')`, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE publications SET status='pending' WHERE id='snapshot-shared-publication'`); err != nil {
				t.Fatal(err)
			}
			called := false
			if scenario != "unavailable" {
				store.meridianProber = meridianProbeFunc(func(_ context.Context, input deployapi.MeridianProbeRequest) error {
					called = true
					if input.Client.Reality == nil || input.Client.Reality.AdvertiseHost != "entry.example.test" || input.Client.Reality.PrivateKey != "" || !input.Client.Material.Credential.Enabled || input.ExpectedExit != "1.1.1.1" {
						t.Fatal("probe did not receive the saved public identity and confirmed exit")
					}
					switch scenario {
					case "proxy failure":
						return errors.New("sensitive-runtime-error")
					case "runtime changed":
						_, err := store.db.Exec(`UPDATE meridian_endpoints SET desired_revision=desired_revision+1,status='pending' WHERE id=?`, sharedSnapshotEndpointID)
						return err
					case "credential disabled":
						_, err := store.db.Exec(`UPDATE meridian_credentials SET enabled=0`)
						return err
					}
					return nil
				})
			}
			if scenario == "stale exit" {
				if _, err := store.db.Exec(`UPDATE agents SET public_egress_observed_at=?`, store.now().Add(-11*time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
			}
			publication, err := store.Publication(ctx, "snapshot-shared-publication")
			if err != nil {
				t.Fatal(err)
			}
			_, probeErr := store.verifyMeridianPublication(ctx, publication, publication.DesiredRevision)
			actual, err := store.Publication(ctx, publication.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "verified" {
				if probeErr != nil || actual.Status != "ready" || !called {
					t.Fatalf("authenticated verification not accepted: status=%s err=%v", actual.Status, probeErr)
				}
			} else if actual.Status == "ready" {
				t.Fatal("ordinary TLS or stale client evidence became proxy readiness")
			}
			if scenario == "stale exit" && called {
				t.Fatal("probe ran without fresh exit evidence")
			}
			if strings.Contains(actual.LastError, "sensitive-runtime-error") {
				t.Fatal("runtime error leaked into publication state")
			}
		})
	}
}
