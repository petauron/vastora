package center

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func newOfficialCheckpointRoot(t *testing.T, now time.Time) (*metadata.Metadata[metadata.RootType], map[string][]signature.Signer) {
	t.Helper()
	root := metadata.Root(now.Add(48 * time.Hour))
	root.Signed.ConsistentSnapshot = true
	signers := make(map[string][]signature.Signer)
	for _, role := range []string{"root", "timestamp", "snapshot", "targets"} {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key, err := metadata.KeyFromPublicKey(public)
		if err != nil {
			t.Fatal(err)
		}
		if err := root.Signed.AddKey(key, role); err != nil {
			t.Fatal(err)
		}
		signer, err := signature.LoadSigner(private, crypto.Hash(0))
		if err != nil {
			t.Fatal(err)
		}
		signers[role] = []signature.Signer{signer}
	}
	if _, err := root.Sign(signers["root"][0]); err != nil {
		t.Fatal(err)
	}
	return root, signers
}

// A signed catalog can fail the compiled executor contract after TUF has
// already revoked its former signing keys. The accepted app remains unchanged,
// but the revocation must survive that failure and a Center restart.
func TestOfficialCatalogContractFailureRetainsRevocationAcrossRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	encode := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	oldRoot, oldSigners := newOfficialCheckpointRoot(t, now)
	bootstrap := encode(oldRoot)
	newRoot, newSigners := newOfficialCheckpointRoot(t, now)
	newRoot.Signed.Version = 2
	newRoot.Signatures = nil
	for _, signer := range []signature.Signer{oldSigners["root"][0], newSigners["root"][0]} {
		if _, err := newRoot.Sign(signer); err != nil {
			t.Fatal(err)
		}
	}
	rotatedRoot := encode(newRoot)
	var files map[string][]byte
	var filesMu sync.RWMutex
	distribution := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		name, valid := strings.CutPrefix(request.URL.Path, "/vastora/catalog/")
		filesMu.RLock()
		raw, exists := files[name]
		filesMu.RUnlock()
		if valid && exists {
			_, _ = w.Write(raw)
			return
		}
		http.NotFound(w, request)
	}))
	defer distribution.Close()
	// Sequential test only: give the production fetch path this test server's
	// CA, without weakening production TLS or using a fake verification result.
	previousTransport := http.DefaultTransport
	http.DefaultTransport = distribution.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	origin := distribution.URL + "/vastora/catalog/"
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	})
	if err := store.ConfigureOfficialCatalog(ctx, origin); err != nil {
		t.Fatal(err)
	}
	rawCatalog, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	base, err := catalog.ParseCatalog(rawCatalog)
	if err != nil {
		t.Fatal(err)
	}
	var app catalog.AppManifest
	for _, candidate := range base.Apps {
		if candidate.ID == "cpa" {
			app = candidate
		}
	}
	if app.ID == "" || app.HostAccess {
		t.Fatal("missing supported unprivileged CPA fixture")
	}
	app.Version = "99.0.0"
	publish := func(root []byte, signers map[string][]signature.Signer, revision uint64, app catalog.AppManifest, previous catalog.OfficialAcceptance) {
		t.Helper()
		value := base
		value.Apps = []catalog.AppManifest{app}
		target := encode(catalog.OfficialTarget{Source: catalog.OfficialSourceIdentity, Channel: "stable", Revision: revision, GeneratedAt: now, ExpiresAt: now.Add(time.Hour), Catalog: encode(value)})
		next, err := catalog.BuildOfficialRepository(root, target, "stable", previous, time.Now().UTC(), signers)
		if err != nil {
			t.Fatal(err)
		}
		filesMu.Lock()
		files = next
		filesMu.Unlock()
	}
	refresh := func() (int, error) {
		// Always pass the original program trust root. Persisted rotations,
		// rather than a changed program/configuration, must provide revocation.
		return store.RefreshTrustedOfficialCatalog(ctx, origin, "stable", bootstrap)
	}
	publish(bootstrap, oldSigners, 1, app, catalog.OfficialAcceptance{})
	if count, err := refresh(); err != nil || count != 1 {
		t.Fatalf("initial signed catalog failed: count=%d err=%v", count, err)
	}
	before, originalTarget, err := store.OfficialCatalogTrust(ctx, "stable")
	if err != nil {
		t.Fatal(err)
	}
	rejected := app
	rejected.Version = "99.0.1"
	rejected.HostAccess = true // Valid catalog syntax, unsupported executor permission.
	publish(rotatedRoot, newSigners, 2, rejected, before.Acceptance)
	if count, err := refresh(); err == nil || count != 0 || !strings.Contains(err.Error(), "unsupported executor contract") {
		t.Fatalf("expected executor contract rejection after verified rotation: count=%d err=%v", count, err)
	}
	checkpoint, retainedTarget, err := store.OfficialCatalogTrust(ctx, "stable")
	if err != nil {
		t.Fatal(err)
	}
	assertRetained := func(state catalog.OfficialFetchState, target []byte) {
		t.Helper()
		if state.Acceptance.Revision != before.Acceptance.Revision || state.Acceptance.SHA256 != before.Acceptance.SHA256 || !state.Acceptance.ExpiresAt.Equal(before.Acceptance.ExpiresAt) || !bytes.Equal(target, originalTarget) {
			t.Fatal("failed catalog changed accepted target, revision, digest, or expiry")
		}
		if !bytes.Equal(state.Metadata["root"], rotatedRoot) || state.Acceptance.ObservedAt.Before(before.Acceptance.ObservedAt) {
			t.Fatal("authorized revocation or observed clock floor was lost")
		}
	}
	assertRetained(checkpoint, retainedTarget)
	var rejectedHistory int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_manifest_history WHERE source_id = ? AND app_id = ? AND version = ?`, OfficialCatalogSourceID, app.ID, rejected.Version).Scan(&rejectedHistory); err != nil || rejectedHistory != 0 {
		t.Fatalf("rejected catalog entered immutable history: count=%d err=%v", rejectedHistory, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	restarted, targetAfterRestart, err := store.OfficialCatalogTrust(ctx, "stable")
	if err != nil {
		t.Fatal(err)
	}
	assertRetained(restarted, targetAfterRestart)
	if !reflect.DeepEqual(restarted, checkpoint) {
		t.Fatal("Center restart changed the verified trust checkpoint")
	}
	recovered := app
	recovered.Version = "99.0.2"
	publish(bootstrap, oldSigners, 3, recovered, restarted.Acceptance)
	if count, err := refresh(); err == nil || count != 0 {
		t.Fatalf("revoked online keys accepted after restart: count=%d err=%v", count, err)
	}
	afterAttack, retainedTarget, err := store.OfficialCatalogTrust(ctx, "stable")
	if err != nil {
		t.Fatal(err)
	}
	assertRetained(afterAttack, retainedTarget)
	publish(rotatedRoot, newSigners, 3, recovered, afterAttack.Acceptance)
	if count, err := refresh(); err != nil || count != 1 {
		t.Fatalf("authorized keys could not recover after contract rejection: count=%d err=%v", count, err)
	}
	accepted, acceptance, err := readAcceptedOfficialCatalog(ctx, store.db, "stable")
	if err != nil || acceptance.Revision != 3 || len(accepted.Apps) != 1 || accepted.Apps[0].Version != recovered.Version || accepted.Apps[0].HostAccess {
		t.Fatalf("authorized recovery did not replace accepted catalog: revision=%d err=%v", acceptance.Revision, err)
	}
}
