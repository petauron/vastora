package center

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/catalog"
)

func TestCatalogSourceLifecycleAndWriteOnlyCredentials(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSource(ctx, SourceInput{ID: OfficialCatalogSourceID, DisplayName: "shadow", URL: "https://example.invalid", PublicKey: publicKey}); err == nil {
		t.Fatal("reserved official source ID was accepted")
	}
	if err := store.CreateSource(ctx, SourceInput{ID: "private-source", DisplayName: "Private", URL: "https://catalog.example.invalid/v1", PublicKey: publicKey, BearerToken: "first-token", RefreshSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	before, err := store.SourceForRefresh(ctx, "private-source")
	if err != nil || before.bearerToken != "first-token" {
		t.Fatalf("initial source credential = %q err=%v", before.bearerToken, err)
	}
	newName := "Updated private source"
	disabled := false
	if err := store.UpdateSource(ctx, "private-source", SourceUpdate{DisplayName: &newName, Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	sources, err := store.ListSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].DisplayName != newName || sources[0].Status != "disabled" || !sources[0].BearerTokenSet {
		t.Fatalf("disabled source metadata = %#v err=%v", sources, err)
	}
	enabled := true
	if err := store.UpdateSource(ctx, "private-source", SourceUpdate{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	preserved, err := store.SourceForRefresh(ctx, "private-source")
	if err != nil || preserved.bearerToken != "first-token" {
		t.Fatalf("omitted credential was not preserved: %q err=%v", preserved.bearerToken, err)
	}
	var oldSecretID string
	if err := store.db.QueryRowContext(ctx, `SELECT bearer_secret_id FROM catalog_sources WHERE id = 'private-source'`).Scan(&oldSecretID); err != nil {
		t.Fatal(err)
	}
	replacement := "replacement-token"
	if err := store.UpdateSource(ctx, "private-source", SourceUpdate{BearerToken: &replacement}); err != nil {
		t.Fatal(err)
	}
	replaced, err := store.SourceForRefresh(ctx, "private-source")
	if err != nil || replaced.bearerToken != replacement {
		t.Fatalf("replacement credential = %q err=%v", replaced.bearerToken, err)
	}
	var oldSecretRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets WHERE id = ?`, oldSecretID).Scan(&oldSecretRows); err != nil || oldSecretRows != 0 {
		t.Fatalf("replaced secret remains: rows=%d err=%v", oldSecretRows, err)
	}
	empty := ""
	if err := store.UpdateSource(ctx, "private-source", SourceUpdate{BearerToken: &empty}); err != nil {
		t.Fatal(err)
	}
	cleared, err := store.SourceForRefresh(ctx, "private-source")
	if err != nil || cleared.bearerToken != "" {
		t.Fatalf("cleared credential = %q err=%v", cleared.bearerToken, err)
	}
	if err := store.DeleteSource(ctx, "private-source"); err != nil {
		t.Fatal(err)
	}
	if sources, err := store.ListSources(ctx); err != nil || len(sources) != 0 {
		t.Fatalf("deleted source remains: %#v err=%v", sources, err)
	}
	if err := store.UpdateSource(ctx, OfficialCatalogSourceID, SourceUpdate{DisplayName: &newName}); err == nil {
		t.Fatal("official source update was accepted")
	}
	if err := store.DeleteSource(ctx, OfficialCatalogSourceID); err == nil {
		t.Fatal("official source deletion was accepted")
	}
}

func TestCatalogManifestIdentityRemainsImmutableAcrossVersionsKeysAndRecreation(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSource(ctx, SourceInput{ID: "immutable-source", DisplayName: "Immutable", URL: "https://catalog.example.invalid", PublicKey: publicKey, RefreshSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	first := catalogLifecycleManifest("v1.0.0", "Original description")
	setCatalogIntegerDefault(&first, `1e0`)
	if err := commitCatalogForTest(ctx, store, "immutable-source", signedCatalogEnvelope(t, privateKey, first), `"first"`, ""); err != nil {
		t.Fatal(err)
	}
	equivalent := catalogLifecycleManifest("1.0.0", "Original description")
	setCatalogIntegerDefault(&equivalent, `1.0`)
	if err := commitCatalogForTest(ctx, store, "immutable-source", signedCatalogEnvelope(t, privateKey, equivalent), `"same"`, ""); err != nil {
		t.Fatalf("semantically identical immutable manifest was rejected: %v", err)
	}
	changed := catalogLifecycleManifest("1.0.0", "Changed description")
	setCatalogIntegerDefault(&changed, `1`)
	if err := commitCatalogForTest(ctx, store, "immutable-source", signedCatalogEnvelope(t, privateKey, changed), `"changed"`, ""); err == nil || !strings.Contains(err.Error(), "immutable catalog manifest changed") {
		t.Fatalf("changed immutable manifest was accepted: %v", err)
	}
	var historyRows int
	var historyVersion string
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*), version FROM catalog_manifest_history WHERE source_id = ? AND app_id = ?`, "immutable-source", "catalog-app").Scan(&historyRows, &historyVersion); err != nil {
		t.Fatal(err)
	}
	if historyRows != 1 || historyVersion != "1.0.0" {
		t.Fatalf("canonical manifest history rows=%d version=%q", historyRows, historyVersion)
	}
	apps, err := store.ListApps(ctx)
	if err != nil || len(apps) != 1 || apps[0].App.Description.English != "Original description" {
		t.Fatalf("last-good cache was replaced: %#v err=%v", apps, err)
	}
	secondVersion := catalogLifecycleManifest("1.0.1", "New version")
	if err := commitCatalogForTest(ctx, store, "immutable-source", signedCatalogEnvelope(t, privateKey, secondVersion), `"second"`, ""); err != nil {
		t.Fatalf("new version was rejected: %v", err)
	}
	if err := commitCatalogForTest(ctx, store, "immutable-source", signedCatalogEnvelope(t, privateKey, changed), `"reintroduced"`, ""); err == nil {
		t.Fatal("removed immutable version was allowed to return with changed content")
	}
	if err := store.DeleteSource(ctx, "immutable-source"); err != nil {
		t.Fatal(err)
	}
	rotatedPublicKey, rotatedPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSource(ctx, SourceInput{ID: "immutable-source", DisplayName: "Recreated", URL: "https://new.example.invalid", PublicKey: rotatedPublicKey, RefreshSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	if err := commitCatalogForTest(ctx, store, "immutable-source", signedCatalogEnvelope(t, rotatedPrivateKey, changed), "", ""); err == nil {
		t.Fatal("source deletion, recreation, and key rotation bypassed immutable history")
	}
}

func TestCatalogSourceStatusAndNotModifiedValidationTime(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSource(ctx, SourceInput{ID: "status-source", DisplayName: "Status", URL: "https://catalog.example.invalid", PublicKey: publicKey, RefreshSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	sources, err := store.ListSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].Status != "pending" {
		t.Fatalf("new source status = %#v err=%v", sources, err)
	}
	if err := commitCatalogForTest(ctx, store, "status-source", signedCatalogEnvelope(t, privateKey, catalogLifecycleManifest("1.0.0", "Healthy")), `"etag"`, "Sat, 30 Aug 2026 03:00:00 GMT"); err != nil {
		t.Fatal(err)
	}
	if due, err := store.DueCatalogSourceIDs(ctx, now.Add(299*time.Second)); err != nil || len(due) != 0 {
		t.Fatalf("source became due before its interval: %#v err=%v", due, err)
	}
	if due, err := store.DueCatalogSourceIDs(ctx, now.Add(300*time.Second)); err != nil || len(due) != 1 || due[0] != "status-source" {
		t.Fatalf("source was not due at its interval: %#v err=%v", due, err)
	}
	sources, _ = store.ListSources(ctx)
	if sources[0].Status != "healthy" || !sources[0].CheckedAt.Equal(now) {
		t.Fatalf("healthy source status = %#v", sources[0])
	}
	now = now.Add(11 * time.Minute)
	sources, _ = store.ListSources(ctx)
	if sources[0].Status != "stale" {
		t.Fatalf("overdue source status = %#v", sources[0])
	}
	if err := recordCatalogErrorForTest(ctx, store, "status-source", context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	sources, _ = store.ListSources(ctx)
	if sources[0].Status != "stale" {
		t.Fatalf("last-good refresh failure status = %#v", sources[0])
	}
	now = now.Add(time.Minute)
	if err := markCatalogNotModifiedForTest(ctx, store, "status-source", `"etag-confirmed"`, "Sat, 30 Aug 2026 03:12:00 GMT"); err != nil {
		t.Fatal(err)
	}
	sources, _ = store.ListSources(ctx)
	if sources[0].Status != "healthy" || !sources[0].CheckedAt.Equal(now) || !sources[0].FetchedAt.Equal(now) {
		t.Fatalf("304 did not revalidate source: %#v", sources[0])
	}
}

func TestCatalogFetchIdentityChangesRequireVerifiedResponseBeforeBecomingHealthy(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, string, string)
	}{
		{
			name: "URL",
			mutate: func(t *testing.T, store *Store, serverURL, _ string) {
				t.Helper()
				newURL := serverURL + "/new"
				if err := store.UpdateSource(context.Background(), "moving-source", SourceUpdate{URL: &newURL}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "Bearer token",
			mutate: func(t *testing.T, store *Store, _, _ string) {
				t.Helper()
				token := "new-tenant-token"
				if err := store.UpdateSource(context.Background(), "moving-source", SourceUpdate{BearerToken: &token}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "custom CA",
			mutate: func(t *testing.T, store *Store, _, caBundle string) {
				t.Helper()
				changedCA := caBundle + "\n"
				if err := store.UpdateSource(context.Background(), "moving-source", SourceUpdate{CustomCAPEM: &changedCA}); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			oldEnvelope := signedCatalogEnvelope(t, privateKey, catalogLifecycleManifest("1.0.0", "Old endpoint"))
			requestCount := 0
			remote := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requestCount++
				if requestCount == 1 {
					writer.Header().Set("ETag", `"old-endpoint"`)
					_, _ = writer.Write(oldEnvelope)
					return
				}
				if request.Header.Get("If-None-Match") != "" || request.Header.Get("If-Modified-Since") != "" {
					t.Errorf("old validators survived %s change: %q %q", test.name, request.Header.Get("If-None-Match"), request.Header.Get("If-Modified-Since"))
				}
				writer.WriteHeader(http.StatusNotModified)
			}))
			defer remote.Close()
			caBundle := string(catalogTestServerCAPEM(t, remote))

			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			if err := store.CreateSource(ctx, SourceInput{
				ID: "moving-source", DisplayName: "Moving", URL: remote.URL + "/old", PublicKey: publicKey,
				BearerToken: "old-tenant-token", CustomCAPEM: caBundle, RefreshSeconds: 300,
			}); err != nil {
				t.Fatal(err)
			}
			server := NewServer(store, "", false)
			if _, err := server.RefreshCatalogSource(ctx, "moving-source"); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, store, remote.URL, caBundle)
			refreshSource, err := store.SourceForRefresh(ctx, "moving-source")
			if err != nil {
				t.Fatal(err)
			}
			if refreshSource.etag != "" || refreshSource.lastMod != "" {
				t.Fatalf("old validators survived %s change: etag=%q lastModified=%q", test.name, refreshSource.etag, refreshSource.lastMod)
			}
			if _, err := server.RefreshCatalogSource(ctx, "moving-source"); err == nil || !strings.Contains(err.Error(), "304 without a conditional request") {
				t.Fatalf("source became healthy after %s change without a verified 200 response: %v", test.name, err)
			}
			sources, err := store.ListSources(ctx)
			if err != nil || len(sources) != 1 || sources[0].Status != "stale" {
				t.Fatalf("%s-change failure status = %#v err=%v", test.name, sources, err)
			}
			apps, err := store.ListApps(ctx)
			if err != nil || len(apps) != 1 || apps[0].App.Description.English != "Old endpoint" {
				t.Fatalf("last verified cache was not retained after %s change: %#v err=%v", test.name, apps, err)
			}
		})
	}
}

func TestCatalogRefreshDiscardsResponseAfterSourceLifecycleChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, SourceInput)
	}{
		{
			name: "configuration changed",
			mutate: func(t *testing.T, store *Store, _ SourceInput) {
				t.Helper()
				changedURL := "https://replacement.example.invalid/catalog.json"
				if err := store.UpdateSource(context.Background(), "racing-source", SourceUpdate{URL: &changedURL}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "source disabled",
			mutate: func(t *testing.T, store *Store, _ SourceInput) {
				t.Helper()
				disabled := false
				if err := store.UpdateSource(context.Background(), "racing-source", SourceUpdate{Enabled: &disabled}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "source deleted and recreated with the same identity",
			mutate: func(t *testing.T, store *Store, input SourceInput) {
				t.Helper()
				if err := store.DeleteSource(context.Background(), input.ID); err != nil {
					t.Fatal(err)
				}
				if err := store.CreateSource(context.Background(), input); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			rawEnvelope := signedCatalogEnvelope(t, privateKey, catalogLifecycleManifest("1.0.0", "Must not commit"))
			requestStarted := make(chan struct{}, 1)
			releaseResponse := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requestStarted <- struct{}{}
				<-releaseResponse
				_, _ = writer.Write(rawEnvelope)
			}))
			defer server.Close()
			certificate, err := x509.ParseCertificate(server.Certificate().Raw)
			if err != nil {
				t.Fatal(err)
			}
			customCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sourceInput := SourceInput{
				ID: "racing-source", DisplayName: "Racing", URL: server.URL, PublicKey: publicKey,
				CustomCAPEM: customCA, RefreshSeconds: 300,
			}
			if err := store.CreateSource(context.Background(), sourceInput); err != nil {
				t.Fatal(err)
			}

			refreshResult := make(chan error, 1)
			go func() {
				_, err := NewServer(store, "", false).RefreshCatalogSource(context.Background(), "racing-source")
				refreshResult <- err
			}()
			select {
			case <-requestStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("catalog request did not start")
			}
			test.mutate(t, store, sourceInput)
			close(releaseResponse)
			select {
			case err := <-refreshResult:
				if err == nil || !(strings.Contains(err.Error(), "changed during refresh") || strings.Contains(err.Error(), "disabled")) {
					t.Fatalf("in-flight response was not fenced: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("catalog refresh did not finish")
			}
			if apps, err := store.ListApps(context.Background()); err != nil || len(apps) != 0 {
				t.Fatalf("stale in-flight response reached the cache: %#v err=%v", apps, err)
			}
		})
	}
}

func TestScheduledCatalogRefreshUsesTheLifecycleService(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rawEnvelope := signedCatalogEnvelope(t, privateKey, catalogLifecycleManifest("1.0.0", "Scheduled"))
	requested := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer scheduler-token" {
			t.Errorf("scheduler authorization = %q", request.Header.Get("Authorization"))
		}
		requested <- struct{}{}
		_, _ = writer.Write(rawEnvelope)
	}))
	defer server.Close()
	certificate, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	customCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateSource(context.Background(), SourceInput{ID: "scheduled-source", DisplayName: "Scheduled", URL: server.URL, PublicKey: publicKey, BearerToken: "scheduler-token", CustomCAPEM: customCA, RefreshSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		NewServer(store, "", false).RunCatalogRefresh(ctx, time.Hour, func(err error) { t.Errorf("scheduled refresh: %v", err) })
		close(done)
	}()
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled refresh did not fetch a due source")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		apps, err := store.ListApps(context.Background())
		if err == nil && len(apps) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduled refresh did not commit its cache: %#v err=%v", apps, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog scheduler did not stop")
	}
	apps, err := store.ListApps(context.Background())
	if err != nil || len(apps) != 1 || apps[0].App.Description.English != "Scheduled" {
		t.Fatalf("scheduled catalog cache = %#v err=%v", apps, err)
	}
}

func catalogLifecycleManifest(version, description string) catalog.Catalog {
	return catalog.Catalog{
		SchemaVersion: catalog.SchemaVersion,
		GeneratedAt:   time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
		Apps: []catalog.AppManifest{{
			ID: "catalog-app", Version: version,
			Name:        catalog.LocalizedText{English: "Catalog app", SimplifiedChinese: "目录应用"},
			Description: catalog.LocalizedText{English: description, SimplifiedChinese: "目录应用描述"},
			License:     "Apache-2.0",
			Images:      []catalog.Image{{Name: "app", Reference: "example.invalid/catalog-app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
			Config:      []catalog.ConfigField{},
		}},
	}
}

func commitCatalogForTest(ctx context.Context, store *Store, sourceID string, rawEnvelope []byte, etag, lastModified string) error {
	source, err := store.SourceForRefresh(ctx, sourceID)
	if err != nil {
		return err
	}
	return store.CommitCatalogRefresh(ctx, sourceID, source.generation, source.revision, rawEnvelope, etag, lastModified)
}

func recordCatalogErrorForTest(ctx context.Context, store *Store, sourceID string, refreshErr error) error {
	source, err := store.SourceForRefresh(ctx, sourceID)
	if err != nil {
		return err
	}
	return store.RecordCatalogErrorForRevision(ctx, sourceID, source.generation, source.revision, refreshErr)
}

func markCatalogNotModifiedForTest(ctx context.Context, store *Store, sourceID, etag, lastModified string) error {
	source, err := store.SourceForRefresh(ctx, sourceID)
	if err != nil {
		return err
	}
	return store.MarkCatalogNotModifiedForRevision(ctx, sourceID, source.generation, source.revision, etag, lastModified)
}

func setCatalogIntegerDefault(value *catalog.Catalog, raw string) {
	defaultValue := json.RawMessage(raw)
	value.Apps[0].Config = []catalog.ConfigField{{
		Key:         "replicas",
		Type:        "integer",
		Label:       catalog.LocalizedText{English: "Replicas", SimplifiedChinese: "副本数"},
		Description: catalog.LocalizedText{English: "Replica count", SimplifiedChinese: "副本数量"},
		Default:     &defaultValue,
	}}
}

func catalogTestServerCAPEM(t *testing.T, server *httptest.Server) []byte {
	t.Helper()
	certificate, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
}

func signedCatalogEnvelope(t *testing.T, privateKey ed25519.PrivateKey, value catalog.Catalog) []byte {
	t.Helper()
	payload, err := catalog.MarshalCatalog(value)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := catalog.Sign("catalog-lifecycle-test", privateKey, payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := catalog.MarshalEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
