package meridianruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/glivela"
)

func TestTransportReceiptPreservesLedgerAndRejectsOtherChanges(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(map[bool]string{false: "unlimited", true: "quota"}[limited], func(t *testing.T) {
			now := time.Now().UTC()
			start := now.Add(-time.Hour)
			cert := strings.Repeat("a", 64)
			makeDesired := func(revision uint64, transport map[string]any) map[string]any {
				config, _ := json.Marshal(map[string]any{"fixtureRevision": revision})
				hash := sha256.Sum256(config)
				return map[string]any{"revision": revision, "config": config, "configSha256": hex.EncodeToString(hash[:]),
					"entry": map[string]any{"revision": revision, "credentials": []any{map[string]any{"credential": map[string]any{"id": "credential-a", "enabled": true}}},
						"peers": []any{map[string]any{"egressId": "landing-a", "address": "100.64.0.2", "port": 1080, "transport": transport}}}, "entryNetwork": map[string]any{"dns": "retained"}}
			}
			origin := makeDesired(1, map[string]any{})
			current := makeDesired(2, map[string]any{"protocol": "ss2022", "key": "AAAAAAAAAAAAAAAAAAAAAA=="})
			originBytes, _ := json.Marshal(origin)
			originHash, _ := quotaBaseDigest(originBytes)
			traffic := func(started time.Time, n string) map[string]any {
				return map[string]any{"startedAt": started, "sampledAt": now, "counters": map[string]any{"credential-a": map[string]string{"upload": n, "download": "0"}}}
			}
			q := map[string]any{"totalBytes": "0", "usedBytes": "7", "resetDay": 0, "timezone": "", "cutoverAt": start,
				"base": current, "transportOrigin": origin, "forwarded": traffic(start, "107"), "raw": traffic(now.Add(-time.Second), "7")}
			previous := PilotCounters{EntryID: "entry-a", CertificateID: cert, ConfigSHA256: origin["configSha256"].(string), Revision: 1, CutoverAt: start,
				StartedAt: start, SampledAt: now.Add(-2 * time.Second), Raw: []glivela.CounterSnapshot{{CredentialID: "credential-a", UpBytes: 100}}, Totals: []glivela.CounterSnapshot{{CredentialID: "credential-a", UpBytes: 300}}}
			if limited {
				previous.Quota = &PilotQuota{TotalBytes: 1000, UsedBytes: 100, CutoverAt: start, BaseSHA256: originHash}
				q["totalBytes"], q["usedBytes"] = "1000", "107"
			}
			node := map[string]any{"id": previous.EntryID, "certificateId": cert, "desired": current, "entryQuota": q, "reportedAt": now,
				"receipt": map[string]any{"revision": uint64(2), "configSha256": current["configSha256"], "runtimeReady": true, "traffic": q["raw"]}}
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "nodes.json")
			read := func() (PilotReceipt, error) {
				t.Helper()
				data, err := json.Marshal([]any{node})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				if limited {
					return ReadQuotaPilotReceipt(path, previous, now)
				}
				return ReadUnlimitedPilotReceipt(path, previous, now)
			}
			r, err := read()
			if err != nil {
				t.Fatal(err)
			}
			next, err := AdvancePilotCounters(previous, r, now)
			if err != nil || next.Totals[0].UpBytes != 307 || r.RuntimeRevision != 2 || !r.StartedAt.Equal(start) {
				t.Fatal("transport restart lost original ledger", err)
			}
			repeated, err := AdvancePilotCounters(next, r, now)
			if err != nil || repeated.Totals[0].UpBytes != 307 {
				t.Fatal("duplicate receipt counted twice", err)
			}
			peer := current["entry"].(map[string]any)["peers"].([]any)[0].(map[string]any)

			peer["transport"] = map[string]any{"protocol": "ss2022", "key": "AAAAAAAAAAAAAAAAAAAAAA==", "publicAddress": "8.8.8.8"}
			publicReceipt, err := read()
			if err != nil {
				t.Fatal("public transport receipt rejected", err)
			}
			publicCounters, err := AdvancePilotCounters(next, publicReceipt, now)
			if err != nil || publicCounters.Totals[0].UpBytes != 307 {
				t.Fatal("public transport changed cumulative ledger", err)
			}
			for _, address := range []any{"100.64.0.2", "127.0.0.1", "192.168.1.1", "0.0.0.0", "8.8.8.8/32", "example.test", "2001:4860:4860::8888", "", 123} {
				peer["transport"].(map[string]any)["publicAddress"] = address
				if _, err := read(); err == nil {
					t.Fatal("invalid public transport accepted", address)
				}
			}
			peer["transport"] = map[string]any{"protocol": "ss2022", "key": "AAAAAAAAAAAAAAAAAAAAAA==", "unknown": "8.8.8.8"}
			if _, err := read(); err == nil {
				t.Fatal("unknown transport field accepted")
			}
			peer["transport"] = map[string]any{"protocol": "ss2022", "key": "AAAAAAAAAAAAAAAAAAAAAA==", "publicAddress": "8.8.8.8"}
			peer["address"] = "100.64.0.3"
			if _, err := read(); err == nil {
				t.Fatal("address change accepted")
			}
			peer["address"] = "100.64.0.2"
			peer["transport"] = map[string]any{"protocol": "ss2022", "key": "invalid"}
			if _, err := read(); err == nil {
				t.Fatal("invalid transport accepted")
			}
		})
	}
}
