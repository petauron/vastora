package meridianruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPilotSourcePinnedPrivateReceipt(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nodes.json")
	now := time.Now().UTC()
	cert, digest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	node := map[string]any{
		"id": "pilot-entry", "certificateId": cert, "reportedAt": now,
		"desired": map[string]any{"revision": 2, "configSha256": digest},
		"receipt": map[string]any{
			"revision": 2, "configSha256": digest, "runtimeReady": true,
			"traffic": map[string]any{
				"startedAt": now.Add(-time.Minute), "sampledAt": now,
				"counters": map[string]any{"credential-a": map[string]string{"upload": "3", "download": "7"}},
			},
		},
	}
	write := func(nodes ...any) {
		t.Helper()
		raw, err := json.Marshal(nodes)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func() (PilotReceipt, error) {
		return ReadPilotReceipt(path, "pilot-entry", cert, digest, 2, now)
	}
	write(node)
	receipt, err := read()
	if err != nil || len(receipt.Counters) != 1 || receipt.Counters[0].UpBytes != 3 || receipt.Counters[0].DownBytes != 7 {
		t.Fatal("valid pinned source rejected", err)
	}
	node["publicationSuspended"] = true
	write(node)
	receipt, err = read()
	if err != nil || !receipt.PublicationSuspended || !receipt.Ready || len(receipt.Counters) != 1 || receipt.Counters[0].UpBytes != 3 || receipt.Counters[0].DownBytes != 7 {
		t.Fatal("suspension must retain verified accounting", err)
	}
	delete(node, "publicationSuspended")
	write(node, node)
	if _, err := read(); err == nil {
		t.Fatal("duplicate node accepted")
	}
	write(node)
	if _, err := ReadPilotReceipt(path, "pilot-entry", strings.Repeat("c", 64), digest, 2, now); err == nil {
		t.Fatal("untrusted node certificate accepted")
	}
	if _, err := ReadPilotReceipt(path, "pilot-entry", cert, digest, 3, now); err == nil {
		t.Fatal("different revision accepted")
	}
	if _, err := ReadPilotReceipt(path, "pilot-entry", cert, digest, 2, now.Add(time.Minute)); err == nil {
		t.Fatal("stale state accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := read(); err == nil {
		t.Fatal("public state accepted")
	}
}
