package meridianruntime

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/petauron/glivela"
)

func TestPilotCountersPreserveLegacyBaselineAndRestart(t *testing.T) {
	cutover := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	before := PilotCounters{EntryID: "pilot-entry", ConfigSHA256: strings.Repeat("a", 64), CutoverAt: cutover,
		Totals: []glivela.CounterSnapshot{{CredentialID: "credential-a", UpBytes: 100, DownBytes: 200}}}
	report := PilotReceipt{EntryID: before.EntryID, ConfigSHA256: before.ConfigSHA256,
		StartedAt: cutover.Add(time.Second), SampledAt: cutover.Add(2 * time.Second), Ready: true,
		Counters: []glivela.CounterSnapshot{{CredentialID: "credential-a", UpBytes: 10, DownBytes: 20}}}
	one, err := AdvancePilotCounters(before, report, report.SampledAt)
	if err != nil || one.Totals[0].UpBytes != 110 || one.Totals[0].DownBytes != 220 {
		t.Fatalf("first native sample lost baseline: %+v %v", one, err)
	}
	repeated, err := AdvancePilotCounters(one, report, report.SampledAt)
	if err != nil || !reflect.DeepEqual(one, repeated) {
		t.Fatal("duplicate sample changed totals", err)
	}
	report.StartedAt, report.SampledAt = cutover.Add(3*time.Second), cutover.Add(4*time.Second)
	report.Counters[0].UpBytes, report.Counters[0].DownBytes = 2, 3
	two, err := AdvancePilotCounters(one, report, report.SampledAt)
	if err != nil || two.Totals[0].UpBytes != 112 || two.Totals[0].DownBytes != 223 {
		t.Fatalf("worker restart lost baseline: %+v %v", two, err)
	}
	if before.Totals[0].UpBytes != 100 || one.Raw[0].UpBytes != 10 {
		t.Fatal("caller-owned checkpoint or receipt was mutated")
	}
}

func TestPilotCountersRejectForeignStaleOrRegressingReports(t *testing.T) {
	for _, fault := range []string{"entry", "config", "unready", "old-process", "stale", "future", "unknown-credential", "duplicate-credential", "decrease", "overflow", "same-time-change"} {
		t.Run(fault, func(t *testing.T) {
			start := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
			c := glivela.CounterSnapshot{CredentialID: "credential-a", UpBytes: 10, DownBytes: 20}
			p := PilotCounters{EntryID: "pilot-entry", ConfigSHA256: strings.Repeat("a", 64), CutoverAt: start,
				StartedAt: start.Add(time.Second), SampledAt: start.Add(2 * time.Second), Raw: []glivela.CounterSnapshot{c},
				Totals: []glivela.CounterSnapshot{{CredentialID: c.CredentialID, UpBytes: 110, DownBytes: 220}}}
			r := PilotReceipt{EntryID: p.EntryID, ConfigSHA256: p.ConfigSHA256, StartedAt: p.StartedAt,
				SampledAt: start.Add(3 * time.Second), Ready: true, Counters: []glivela.CounterSnapshot{c}}
			now := r.SampledAt
			switch fault {
			case "entry":
				r.EntryID = "other-entry"
			case "config":
				r.ConfigSHA256 = strings.Repeat("b", 64)
			case "unready":
				r.Ready = false
			case "old-process":
				r.StartedAt = start.Add(-time.Second)
			case "stale":
				now = now.Add(time.Minute)
			case "future":
				r.SampledAt = now.Add(time.Second)
			case "unknown-credential":
				r.Counters[0].CredentialID = "other-credential"
			case "duplicate-credential":
				r.Counters = append(r.Counters, c)
			case "decrease":
				r.Counters[0].UpBytes = 0
			case "overflow":
				p.Totals[0].UpBytes = math.MaxInt64
				r.Counters[0].UpBytes++
			case "same-time-change":
				r.SampledAt = p.SampledAt
				r.Counters[0].UpBytes++
			}
			if _, err := AdvancePilotCounters(p, r, now); err == nil {
				t.Fatal("unsafe sample accepted")
			}
		})
	}
}
