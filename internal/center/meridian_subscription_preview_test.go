package center

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/petauron/glivela"
)

func TestSubscriptionPreviewUsesActualInventoryWithoutSavingSnapshots(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	ctx := context.Background()
	var commandsBefore int
	if err := store.db.QueryRow(`SELECT count(*) FROM application_commands`).Scan(&commandsBefore); err != nil {
		t.Fatal(err)
	}
	view, err := store.MeridianSubscriptionPreview(ctx, sharedSnapshotAccountB)
	if err != nil || view.Count != 1 || view.Source != "live" {
		t.Fatalf("preview=%#v err=%v", view, err)
	}
	var commandsAfter int
	if err := store.db.QueryRow(`SELECT count(*) FROM application_commands`).Scan(&commandsAfter); err != nil || commandsAfter != commandsBefore {
		t.Fatal("preview launched a command", err)
	}
	var snapshots int
	if err := store.db.QueryRow(`SELECT count(*) FROM meridian_subscription_snapshots`).Scan(&snapshots); err != nil || snapshots != 0 {
		t.Fatal("preview saved a snapshot", err)
	}
	actual, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
	if err != nil {
		t.Fatal(err)
	}
	links, err := glivela.RenderLinks(actual.Entries, actual.Account.ID, glivela.FixedMode, actual.Routes, false)
	if err != nil {
		t.Fatal(err)
	}
	mihomo, err := glivela.RenderMihomo(actual.Entries, actual.Account.ID, glivela.FixedMode, actual.Routes)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range view.Items {
		found := false
		for _, line := range strings.Split(strings.TrimSpace(string(links)), "\n") {
			parsed, err := url.Parse(line)
			if err == nil && parsed.Fragment == item.Name {
				found = true
			}
		}
		if !strings.Contains(string(mihomo), item.Name) || !found {
			t.Fatal("preview differs from actual output")
		}
	}
	encoded, _ := json.Marshal(view)
	for _, secret := range []string{sharedSnapshotTokenB, sharedSnapshotUUIDB, "vless://", "subscriptionPath", "protocolSecret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("preview leaked credential material")
		}
	}
}

func TestSubscriptionPreviewExplainsPartialLiveAndRetainedSnapshot(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	addMeridianSnapshotSecondEndpoint(t, store)
	ctx := context.Background()
	if _, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := store.db.QueryRow(`SELECT content_sha256 FROM meridian_subscription_snapshots WHERE account_id=?`, sharedSnapshotAccountB).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE meridian_endpoints SET desired_revision=2,runtime_healthy=0,status='pending' WHERE id='snapshot-second-endpoint'`); err != nil {
		t.Fatal(err)
	}
	partial, err := store.MeridianSubscriptionPreview(ctx, sharedSnapshotAccountB)
	if err != nil || partial.Count != 1 || partial.Source != "live" || len(partial.Excluded) == 0 || partial.Excluded[0].Reason != "entry_pending" {
		t.Fatalf("partial=%#v err=%v", partial, err)
	}
	if _, err := store.db.Exec(`UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending'`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.MeridianSubscriptionPreview(ctx, sharedSnapshotAccountB)
	if err != nil || snapshot.Count != 2 || snapshot.Source != "applied_snapshot" || len(snapshot.Excluded) != 0 {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	var after string
	if err := store.db.QueryRow(`SELECT content_sha256 FROM meridian_subscription_snapshots WHERE account_id=?`, sharedSnapshotAccountB).Scan(&after); err != nil || before != after {
		t.Fatal("preview changed applied snapshot", err)
	}
}

func TestSubscriptionPreviewRetainsStartupAcceptedRoute(t *testing.T) {
	store, _, now := openMeridianRouteHealthReadFixture(t)
	ctx := context.Background()
	if _, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(10 * time.Second) }
	view, err := store.MeridianSubscriptionPreview(ctx, sharedSnapshotAccountB)
	if err != nil || view.Count != 2 || len(view.Excluded) != 0 {
		t.Fatalf("startup acceptance expired: %#v %v", view, err)
	}
}

func TestSubscriptionPreviewRequiresSessionAndPreservesUnavailablePublicResponse(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	ctx := context.Background()
	session, _, err := store.CreateFirstAdmin(ctx, "admin", "example-password-for-test-only")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE meridian_accounts SET enabled=0 WHERE id=?`, sharedSnapshotAccountB); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).Handler()
	for _, authenticated := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/meridian/accounts/"+sharedSnapshotAccountB+"/subscription-preview", nil)
		if authenticated {
			request.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("retired business API served: %d", response.Code)
		}
	}
	public := httptest.NewRecorder()
	handler.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/sub/"+sharedSnapshotTokenB, nil))
	if public.Code != http.StatusNotFound {
		t.Fatal("preview changed public access policy", public.Code)
	}
}

func TestSubscriptionPreviewZeroPublishedEntriesIsNotAServiceFailure(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	if _, err := store.db.Exec(`UPDATE meridian_credentials SET enabled=0 WHERE account_id=?`, sharedSnapshotAccountB); err != nil {
		t.Fatal(err)
	}
	view, err := store.MeridianSubscriptionPreview(context.Background(), sharedSnapshotAccountB)
	if err != nil || view.Count != 0 || len(view.Items) != 0 || view.Reason != "subscription_unavailable" {
		t.Fatalf("empty preview=%#v err=%v", view, err)
	}
	if len(view.Excluded) == 0 || view.Excluded[0].Reason != "credential_disabled" {
		t.Fatal("missing disabled credential reason", view.Excluded)
	}
}

func TestSubscriptionPreviewShowsHiddenNativeAndKeepsRoutedIdentity(t *testing.T) {
	store, id, _ := openMeridianRouteHealthReadFixture(t)
	if _, err := store.db.Exec(`UPDATE meridian_route_grants SET hide_native=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	view, err := store.MeridianSubscriptionPreview(context.Background(), sharedSnapshotAccountB)
	if err != nil || view.Count != 1 {
		t.Fatalf("view=%#v err=%v", view, err)
	}
	hidden := false
	for _, issue := range view.Excluded {
		if issue.Reason == "native_hidden" {
			hidden = true
		}
	}
	if !hidden || !strings.Contains(view.Items[0].Name, "双跳") {
		t.Fatal("native/routed identity confused", view)
	}
}

func TestSubscriptionPreviewExplainsAccountAvailability(t *testing.T) {
	for _, tc := range []struct{ name, query, reason string }{
		{"disabled", `UPDATE meridian_accounts SET enabled=0 WHERE id=?`, "account_disabled"},
		{"expired", `UPDATE meridian_accounts SET expiry_time=1 WHERE id=?`, "account_expired"},
		{"quota", `UPDATE meridian_accounts SET total_bytes=1 WHERE id=?`, "quota_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openMeridianSharedEndpointSnapshotFixture(t)
			if _, err := store.db.Exec(tc.query, sharedSnapshotAccountB); err != nil {
				t.Fatal(err)
			}
			view, err := store.MeridianSubscriptionPreview(context.Background(), sharedSnapshotAccountB)
			if err != nil || view.Count != 0 || view.Reason != tc.reason {
				t.Fatalf("view=%#v err=%v", view, err)
			}
		})
	}
}

func TestSubscriptionPreviewEmptyFilteredSnapshotDoesNotRestoreDirect(t *testing.T) {
	store, id, now := openMeridianRouteHealthReadFixture(t)
	ctx := context.Background()
	if _, err := store.db.Exec(`UPDATE meridian_route_grants SET hide_native=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending' WHERE id=?`, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE meridian_route_grants SET runtime_healthy=0,status='blocked' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(10 * time.Second) }
	view, err := store.MeridianSubscriptionPreview(ctx, sharedSnapshotAccountB)
	if err != nil || view.Count != 0 || len(view.Items) != 0 || view.Reason != "subscription_unavailable" {
		t.Fatalf("empty snapshot=%#v err=%v", view, err)
	}
	hidden := false
	for _, issue := range view.Excluded {
		if issue.Reason == "native_hidden" {
			hidden = true
		}
	}
	if !hidden {
		t.Fatal("missing fail-closed native suppression reason")
	}
	if _, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); !errors.Is(err, errMeridianSubscriptionNotFound) {
		t.Fatal("snapshot revived an unauthorized direct entry", err)
	}
}
