package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func TestOfficialFetchHTTPSUpdateAndFailurePreserveAcceptance(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root, signers := officialTestRoot(t, now)
	payload, err := os.ReadFile("testdata/v3/valid-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	build := func(revision uint64, previous OfficialAcceptance) map[string][]byte {
		t.Helper()
		raw, err := json.Marshal(OfficialTarget{Source: OfficialSourceIdentity, Channel: "stable", Revision: revision, GeneratedAt: now, ExpiresAt: now.Add(time.Hour), Catalog: payload})
		if err != nil {
			t.Fatal(err)
		}
		files, err := BuildOfficialRepository(root, raw, "stable", previous, time.Now().UTC(), signers)
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	files := build(1, OfficialAcceptance{})
	var stateMu sync.RWMutex
	status := 0
	tamper := false
	const prefix = "/vastora/catalog/"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			t.Errorf("catalog request escaped project prefix: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, prefix)
		stateMu.RLock()
		responseStatus, responseTamper := status, tamper
		raw, ok := files[name]
		stateMu.RUnlock()
		if responseStatus != 0 {
			w.WriteHeader(responseStatus)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		if responseTamper && strings.HasPrefix(name, "targets/") {
			raw = append(append([]byte{}, raw...), ' ')
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	fetch := func(previous OfficialFetchState) (OfficialFetchResult, error) {
		return fetchOfficial(context.Background(), server.URL+prefix, "stable", root, previous, server.Client().Transport)
	}
	first, err := fetch(OfficialFetchState{})
	if err != nil {
		t.Fatal(err)
	}
	if first.State.Acceptance.Revision != 1 || len(first.Catalog.Apps) == 0 {
		t.Fatal("initial publication not accepted")
	}
	nextFiles := build(2, first.State.Acceptance)
	stateMu.Lock()
	oldFiles := files
	files = nextFiles
	stateMu.Unlock()
	second, err := fetch(first.State)
	if err != nil {
		t.Fatal(err)
	}
	if second.State.Acceptance.Revision != 2 {
		t.Fatal("independent update not accepted")
	}
	before, _ := json.Marshal(second.State)
	for _, test := range []struct {
		name           string
		status         int
		tamper, replay bool
	}{
		{name: "network failure", status: http.StatusServiceUnavailable},
		{name: "304 is not new freshness", status: http.StatusNotModified},
		{name: "target tampering", tamper: true},
		{name: "older signed repository", replay: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateMu.Lock()
			status, tamper = test.status, test.tamper
			if test.replay {
				files = oldFiles
			}
			stateMu.Unlock()
			result, err := fetch(second.State)
			if err == nil || result.State.Acceptance.Revision != 0 {
				t.Fatalf("failed refresh accepted: %v", err)
			}
			after, _ := json.Marshal(second.State)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed refresh mutated accepted state")
			}
		})
	}
}

func TestOfficialFetchRetainsRevocationWhenTargetDownloadFails(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	oldRoot, oldSigners := officialTestRoot(t, now)
	nextRoot, nextSigners := officialTestRoot(t, now)
	rotated, err := metadata.Root().FromBytes(nextRoot)
	if err != nil {
		t.Fatal(err)
	}
	rotated.Signed.Version = 2
	rotated.Signatures = nil
	for _, signer := range []signature.Signer{oldSigners["root"][0], nextSigners["root"][0]} {
		if _, err := rotated.Sign(signer); err != nil {
			t.Fatal(err)
		}
	}
	rotatedBytes, err := json.Marshal(rotated)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("testdata/v3/valid-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	build := func(root []byte, signers map[string][]signature.Signer, revision uint64, previous OfficialAcceptance) map[string][]byte {
		t.Helper()
		raw, err := json.Marshal(OfficialTarget{Source: OfficialSourceIdentity, Channel: "stable", Revision: revision, GeneratedAt: now, ExpiresAt: now.Add(time.Hour), Catalog: payload})
		if err != nil {
			t.Fatal(err)
		}
		files, err := BuildOfficialRepository(root, raw, "stable", previous, time.Now().UTC(), signers)
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	files := build(oldRoot, oldSigners, 1, OfficialAcceptance{})
	var stateMu sync.RWMutex
	failTarget := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/vastora/catalog/")
		stateMu.RLock()
		responseFailTarget := failTarget
		raw, ok := files[name]
		stateMu.RUnlock()
		if responseFailTarget && strings.HasPrefix(name, "targets/") {
			w.WriteHeader(503)
			return
		}
		if ok {
			_, _ = w.Write(raw)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	fetch := func(previous OfficialFetchState) (OfficialFetchResult, error) {
		return fetchOfficial(context.Background(), server.URL+"/vastora/catalog/", "stable", oldRoot, previous, server.Client().Transport)
	}
	first, err := fetch(OfficialFetchState{})
	if err != nil {
		t.Fatal(err)
	}
	nextFiles := build(rotatedBytes, nextSigners, 2, first.State.Acceptance)
	stateMu.Lock()
	files = nextFiles
	failTarget = true
	stateMu.Unlock()
	failed, err := fetch(first.State)
	if err == nil || failed.Checkpoint == nil || len(failed.Target) != 0 {
		t.Fatalf("expected failed target with trust checkpoint: %v", err)
	}
	savedRoot, err := metadata.Root().FromBytes(failed.Checkpoint.Metadata["root"])
	if err != nil || savedRoot.Signed.Version != 2 {
		t.Fatalf("revocation was discarded: %v", err)
	}
	// Simulate persisted/reloaded checkpoint plus unchanged accepted catalog.
	persisted := first.State
	persisted.Metadata = failed.Checkpoint.Metadata
	persisted.Acceptance.ObservedAt = failed.Checkpoint.ObservedAt
	nextFiles = build(oldRoot, oldSigners, 3, persisted.Acceptance)
	stateMu.Lock()
	failTarget = false
	files = nextFiles
	stateMu.Unlock()
	if _, err := fetch(persisted); err == nil {
		t.Fatal("revoked online keys accepted after failed target download")
	}
	nextFiles = build(rotatedBytes, nextSigners, 2, persisted.Acceptance)
	stateMu.Lock()
	files = nextFiles
	stateMu.Unlock()
	if result, err := fetch(persisted); err != nil || result.State.Acceptance.Revision != 2 {
		t.Fatalf("authorized recovery failed: %v", err)
	}
}

func TestOfficialFetchRejectsMissingIndependentTrust(t *testing.T) {
	for _, test := range []struct {
		name, origin, channel string
		root                  []byte
		state                 OfficialFetchState
	}{
		{name: "missing bootstrap", origin: "https://example.invalid/catalog", channel: "stable"},
		{name: "http", origin: "http://example.invalid/catalog", channel: "stable"},
		{name: "credentials", origin: "https://user:password@example.invalid/catalog", channel: "stable"},
		{name: "query", origin: "https://example.invalid/catalog?token=secret", channel: "stable"},
		{name: "channel traversal", origin: "https://example.invalid/catalog", channel: "../stable"},
		{name: "missing accepted root", origin: "https://example.invalid/catalog", channel: "stable", root: []byte("must not reset to bootstrap"), state: OfficialFetchState{Acceptance: OfficialAcceptance{Revision: 1}}},
		{name: "clock rollback", origin: "https://example.invalid/catalog", channel: "stable", state: OfficialFetchState{Acceptance: OfficialAcceptance{ObservedAt: time.Now().Add(time.Hour)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := FetchOfficial(context.Background(), test.origin, test.channel, test.root, test.state)
			if err == nil || result.State.Acceptance.Revision != 0 || len(result.Target) != 0 {
				t.Fatalf("invalid trust configuration produced a result: %+v %v", result, err)
			}
		})
	}
}
