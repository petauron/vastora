package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/petauron/vastora/internal/catalog"
)

func TestCatalogCheckRejectsInvalidInputBeforeArtifactAccess(t *testing.T) {
	file := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(file, []byte(`{"schemaVersion":999,"apps":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--catalog", file, "--artifacts"}); err == nil {
		t.Fatal("invalid catalog accepted")
	}
	if err := verifyImage(context.Background(), "example/image:latest"); err == nil {
		t.Fatal("unpinned image accepted")
	}
}

func TestCatalogCheckRequiresReviewedRootsWhenRequested(t *testing.T) {
	// The root gate runs before catalog parsing and, importantly, before any
	// optional artifact access. A missing catalog must not mask this failure.
	directory := t.TempDir()
	err := run([]string{"--catalog", filepath.Join(directory, "not-read.json"), "--root-directory", directory, "--artifacts"})
	if err == nil || !strings.Contains(err.Error(), "no reviewed public roots") {
		t.Fatalf("root requirement was not checked first: %v", err)
	}
	if err := run([]string{"--catalog", "../../catalog/catalog.json"}); err != nil {
		t.Fatalf("contract checking without optional root validation failed: %v", err)
	}
}

func TestCatalogCheckRejectsUnboundedOCIConfigBeforeDownloading(t *testing.T) {
	for _, size := range []int64{-1, 0, catalog.MaxEnvelopeBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			manifest := v1.Manifest{
				SchemaVersion: 2, MediaType: types.OCIManifestSchema1,
				Config: v1.Descriptor{MediaType: types.OCIConfigJSON, Size: size, Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}},
				Layers: []v1.Descriptor{},
			}
			raw, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			digest, _, err := v1.SHA256(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			var configRequested atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch {
				case request.URL.Path == "/v2/":
					writer.WriteHeader(http.StatusOK)
				case strings.Contains(request.URL.Path, "/manifests/"):
					writer.Header().Set("Content-Type", string(types.OCIManifestSchema1))
					writer.Header().Set("Docker-Content-Digest", digest.String())
					_, _ = writer.Write(raw)
				case strings.Contains(request.URL.Path, "/blobs/"):
					configRequested.Store(true)
					http.Error(writer, "configuration must not be downloaded", http.StatusInternalServerError)
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = verifyImage(ctx, strings.TrimPrefix(server.URL, "http://")+"/test/image@"+digest.String())
			if err == nil || !strings.Contains(err.Error(), "configuration exceeds size limits") {
				t.Fatalf("configuration size was not rejected: %v", err)
			}
			if configRequested.Load() {
				t.Fatal("unbounded configuration was downloaded")
			}
		})
	}
}
