package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/petauron/vastora/internal/networking"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var handoverTables = []string{"meridian_cutover", "meridian_endpoints", "meridian_accounts", "meridian_credentials", "meridian_route_grants", "meridian_line_usage", "meridian_usage_watermarks", "meridian_subscription_snapshots", "meridian_deployments", "meridian_route_health_events", "landing_server_states", "landing_proxy_states", "landing_proxy_retirements", "landing_client_grants", "landing_client_blocks", "node_egress_policies", "glivela_pilot_checkpoint"}

func handoverTableHashes(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range handoverTables {
		rows, err := db.Query(`SELECT * FROM "` + table + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		enc := json.NewEncoder(h)
		enc.Encode(columns)
		for rows.Next() {
			v := make([]any, len(columns))
			p := make([]any, len(columns))
			for i := range p {
				p[i] = &v[i]
			}
			if err := rows.Scan(p...); err != nil {
				t.Fatal(err)
			}
			if err := enc.Encode(v); err != nil {
				t.Fatal(err)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		out[table] = hex.EncodeToString(h.Sum(nil))
	}
	return out
}
func writeSyntheticHandoverProof(t *testing.T, dir string) {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	proof := businessHandoverProof{Version: 1, SnapshotID: "isolated-handover", SourceSHA256: hex.EncodeToString(h.Sum(nil)), CaptureSHA256: strings.Repeat("a", 64), DestinationSHA256: strings.Repeat("b", 64), PreviousWriterID: strings.Repeat("c", 64), CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	raw, _ := json.Marshal(proof)
	if err := os.WriteFile(filepath.Join(dir, "glivela-business-handover.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func assertRetiredHandover(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.businessRetired || s.RequireBusinessHandover(context.Background()) != nil {
		t.Fatal("retirement did not remain authoritative")
	}
	if !reflect.DeepEqual(before, handoverTableHashes(t, s.db)) {
		t.Fatal("retirement changed account, subscription, credentials, history or counters")
	}
	for _, table := range handoverTables {
		if _, err := s.db.Exec(`INSERT INTO "` + table + `" DEFAULT VALUES`); err == nil || !strings.Contains(err.Error(), "permanently handed over") {
			t.Fatalf("retired table %s accepted a writer: %v", table, err)
		}
	}
	if _, err := s.db.Exec("INSERT INTO settings(key,value) VALUES('ordinary-platform-setting','kept') ON CONFLICT(key) DO UPDATE SET value=excluded.value"); err != nil {
		t.Fatal("platform writes were blocked", err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal("restart lost retirement", err)
	}
	defer s.Close()
	if !s.businessRetired || !reflect.DeepEqual(before, handoverTableHashes(t, s.db)) {
		t.Fatal("restart changed frozen history")
	}
}
func TestBusinessHandoverPreservesHistoryAndPermanentlyFencesWrites(t *testing.T) {
	s := openMeridianSharedEndpointSnapshotFixture(t)
	dir := s.dataDir
	if s.RequireBusinessHandover(context.Background()) == nil {
		t.Fatal("old business may serve without completed handover")
	}
	before := handoverTableHashes(t, s.db)
	s.Close()
	writeSyntheticHandoverProof(t, dir)
	assertRetiredHandover(t, dir, before)
}
func TestBusinessHandoverRejectsChangedFinalSource(t *testing.T) {
	s := openMeridianSharedEndpointSnapshotFixture(t)
	dir := s.dataDir
	s.Close()
	writeSyntheticHandoverProof(t, dir)
	f, err := os.OpenFile(filepath.Join(dir, "center.db"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("changed"))
	f.Close()
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatal("changed final source accepted")
	}
}
func TestRetiredProxyAPIsCannotMutateOrServeOldSubscriptions(t *testing.T) {
	s := openMeridianSharedEndpointSnapshotFixture(t)
	defer s.Close()
	before := handoverTableHashes(t, s.db)
	handler := NewServer(s, "", false).Handler()
	for _, r := range []struct{ method, path string }{{"GET", "/sub/retained-token"}, {"GET", "/api/v1/meridian"}, {"POST", "/api/v1/meridian/accounts"}, {"PUT", "/api/v1/meridian/accounts/example"}, {"GET", "/api/v1/meridian/traffic"}, {"POST", "/api/v1/meridian/endpoints/example/glivela-pilot"}, {"GET", "/api/v1/meridian/landing/latencies/events"}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(r.method, r.path, strings.NewReader("{}")))
		if w.Code != http.StatusNotFound {
			t.Fatalf("retired route %s %s status=%d", r.method, r.path, w.Code)
		}
	}
	if !reflect.DeepEqual(before, handoverTableHashes(t, s.db)) {
		t.Fatal("retired API changed history")
	}
}
func TestPrivateBusinessHandoverSchema120Copy(t *testing.T) {
	source := os.Getenv("VASTORA_GLIVELA_HANDOVER_FIXTURE")
	if source == "" {
		t.Skip("optional private frozen schema120 fixture")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	for _, name := range []string{"center.db", "center.key"} {
		in, err := os.Open(filepath.Join(source, name))
		if err != nil {
			t.Fatal("private source unavailable")
		}
		out, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(out, in)
		out.Close()
		in.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "center.db")+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	before := handoverTableHashes(t, db)
	db.Close()
	writeSyntheticHandoverProof(t, dir)
	assertRetiredHandover(t, dir, before)
	t.Log("schema120 copy retained all 17 business tables through migration, writer fencing and restart")
}

func TestRetiredBusinessKeepsAgentHeartbeatAndClaimUsable(t *testing.T) {
	s := openMeridianSharedEndpointSnapshotFixture(t)
	ctx := context.Background()
	n := enrollOrchestrationNode(t, s, "ordinary-worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.23", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.23", LANAddress: "10.0.0.23", EnabledKinds: []string{networking.KindLAN}})
	dir := s.dataDir
	s.Close()
	writeSyntheticHandoverProof(t, dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before := handoverTableHashes(t, s.db)
	for _, startup := range []bool{false, true} {
		if err := s.RecordAgentHeartbeat(ctx, n.ID, n.Credential, NodeHeartbeat{Version: Version, Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true}, Startup: startup}); err != nil {
			t.Fatal("retirement blocked ordinary heartbeat", err)
		}
		if _, err := s.ClaimNextTask(ctx, n.ID, n.Credential); err != nil {
			t.Fatal("retirement blocked ordinary task selection", err)
		}
	}
	if !reflect.DeepEqual(before, handoverTableHashes(t, s.db)) {
		t.Fatal("ordinary Agent paths changed retired business")
	}
}
