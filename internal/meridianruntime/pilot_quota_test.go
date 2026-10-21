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

func TestQuotaPilotSourceAcceptsOnlyPinnedAuthorizationRevisions(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nodes.json")
	cutover := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	cert := strings.Repeat("a", 64)
	config := []byte(`{"fixture":"initial"}`)
	digest := sha256.Sum256(config)
	baseDigest := hex.EncodeToString(digest[:])
	base := map[string]any{"revision": uint64(1), "config": config, "configSha256": baseDigest,
		"entry": map[string]any{"revision": uint64(1), "credentials": []any{map[string]any{"credential": map[string]any{"id": "credential-a", "enabled": true, "user": "retained-user"}, "protocolId": "retained-test-identity"}}}, "entryNetwork": map[string]any{}}
	zero := map[string]any{"startedAt": cutover, "sampledAt": cutover, "counters": map[string]any{"credential-a": map[string]string{"upload": "0", "download": "0"}}}
	q := map[string]any{"totalBytes": "100", "usedBytes": "80", "resetDay": 1, "timezone": "UTC", "nextResetAt": time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "cutoverAt": cutover, "base": base, "forwarded": zero}
	node := map[string]any{"id": "entry-a", "certificateId": cert, "desired": base, "entryQuota": q}
	write := func() {
		t.Helper()
		raw, e := json.Marshal([]any{node})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write()
	seed := PilotQuota{TotalBytes: 100, UsedBytes: 80, ResetDay: 1, Timezone: "UTC", NextResetAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), CutoverAt: cutover}
	seed, err = ReadPilotQuotaSeed(path, "entry-a", cert, baseDigest, 1, seed)
	if err != nil {
		t.Fatal(err)
	}
	previous := PilotCounters{EntryID: "entry-a", CertificateID: cert, ConfigSHA256: baseDigest, Revision: 1, CutoverAt: cutover, Quota: &seed, Totals: []glivela.CounterSnapshot{{CredentialID: "credential-a", UpBytes: 300, DownBytes: 400}}}
	blockedConfig := []byte(`{"fixture":"blocked"}`)
	blockedHash := sha256.Sum256(blockedConfig)
	blockedDigest := hex.EncodeToString(blockedHash[:])
	now := cutover.Add(10 * time.Second)
	current := map[string]any{"revision": uint64(2), "config": blockedConfig, "configSha256": blockedDigest, "entry": map[string]any{"revision": uint64(2), "credentials": []any{map[string]any{"credential": map[string]any{"id": "credential-a", "enabled": false, "user": "retained-user"}, "protocolId": "retained-test-identity"}}}, "entryNetwork": map[string]any{}}
	q["usedBytes"] = "105"
	q["forwarded"] = map[string]any{"startedAt": cutover, "sampledAt": now, "counters": map[string]any{"credential-a": map[string]string{"upload": "25", "download": "0"}}}
	rawSample := map[string]any{"startedAt": cutover.Add(5 * time.Second), "sampledAt": now, "counters": map[string]any{"credential-a": map[string]string{"upload": "0", "download": "0"}}}
	q["raw"] = rawSample
	node["desired"] = current
	node["reportedAt"] = now
	node["receipt"] = map[string]any{"revision": uint64(2), "configSha256": blockedDigest, "runtimeReady": true, "traffic": rawSample}
	write()
	receipt, err := ReadQuotaPilotReceipt(path, previous, now)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := AdvancePilotCounters(previous, receipt, now)
	if err != nil || checkpoint.Totals[0].UpBytes != 325 || checkpoint.Quota.UsedBytes != 105 {
		t.Fatal("quota revision lost baseline or pre-restart bytes", err)
	}
	current["entry"].(map[string]any)["credentials"].([]any)[0].(map[string]any)["protocolId"] = "changed-identity"
	write()
	if _, err := ReadQuotaPilotReceipt(path, previous, now); err == nil {
		t.Fatal("quota update changed credentials")
	}
}

func TestPilotQuotaAccountingRejectsFalsePeriodUsage(t *testing.T) {
	now := time.Now().UTC()
	q := PilotQuota{TotalBytes: 100, UsedBytes: 80, CutoverAt: now, BaseSHA256: strings.Repeat("b", 64)}
	p := PilotCounters{EntryID: "entry-a", ConfigSHA256: strings.Repeat("a", 64), CutoverAt: now, Quota: &q, Totals: []glivela.CounterSnapshot{{CredentialID: "credential-a", UpBytes: 300}}}
	next := q
	next.UsedBytes = 85
	r := PilotReceipt{EntryID: p.EntryID, ConfigSHA256: p.ConfigSHA256, StartedAt: now, SampledAt: now.Add(time.Second), Ready: true, Quota: &next, Counters: []glivela.CounterSnapshot{{CredentialID: "credential-a", UpBytes: 5}}}
	if _, err := AdvancePilotCounters(p, r, r.SampledAt); err != nil {
		t.Fatal(err)
	}
	next.UsedBytes = 0
	if _, err := AdvancePilotCounters(p, r, r.SampledAt); err == nil {
		t.Fatal("usage cleared without cycle boundary")
	}
}
