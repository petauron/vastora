package meridianruntime

import (
	"errors"
	"math"
	"slices"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/landing"
)

// PilotReceipt is a verified Glivela observation, not an instruction to the
// legacy runtime. The transport must authenticate its source before calling
// AdvancePilotCounters. It deliberately contains no configuration or secrets.
type PilotReceipt struct {
	EntryID      string
	ConfigSHA256 string
	StartedAt    time.Time
	SampledAt    time.Time
	Ready        bool
	Counters     []glivela.CounterSnapshot
	Health       *PilotHealth
}

type PilotHealth struct {
	SampledAt time.Time                  `json:"sampledAt"`
	Source    *landing.PeerIdentity      `json:"source,omitempty"`
	Peers     map[string]PeerObservation `json:"peers"`
}

// PilotCounters is a transaction checkpoint for one explicitly selected entry.
// Totals start with the legacy raw watermarks and remain monotonic across native
// worker restarts. The legacy ledger remains the sole accounting authority.
// Callers must persist this checkpoint and ledger deltas in one transaction;
// persisting only either half would permit loss or replay after a crash.
type PilotCounters struct {
	EntryID       string
	ConfigSHA256  string
	CertificateID string
	Revision      uint64
	Source        *landing.PeerIdentity
	Peers         []Peer
	ExpectedExits map[string]string
	CutoverAt     time.Time
	StartedAt     time.Time
	SampledAt     time.Time
	Raw           []glivela.CounterSnapshot
	Totals        []glivela.CounterSnapshot
}

// AdvancePilotCounters has no side effects. On any invalid observation it
// returns no replacement checkpoint, so a caller cannot accidentally commit a
// partially advanced credential. Exact re-delivery is idempotent.
func AdvancePilotCounters(previous PilotCounters, receipt PilotReceipt, now time.Time) (PilotCounters, error) {
	invalid := errors.New("meridianruntime: invalid scoped pilot observation")
	if !glivela.ValidIdentifier(previous.EntryID) || !glivela.ValidIdentity(previous.ConfigSHA256) ||
		receipt.EntryID != previous.EntryID || receipt.ConfigSHA256 != previous.ConfigSHA256 ||
		previous.CutoverAt.IsZero() || previous.CutoverAt.After(now) || !receipt.Ready ||
		receipt.StartedAt.Before(previous.CutoverAt) || receipt.SampledAt.Before(receipt.StartedAt) ||
		receipt.SampledAt.After(now) || now.Sub(receipt.SampledAt) > 30*time.Second {
		return PilotCounters{}, invalid
	}
	totals, err := pilotCounterMap(previous.Totals)
	if err != nil || len(totals) == 0 {
		return PilotCounters{}, invalid
	}
	current, err := pilotCounterMap(receipt.Counters)
	if err != nil || len(current) != len(totals) {
		return PilotCounters{}, invalid
	}
	for id := range totals {
		if _, exists := current[id]; !exists {
			return PilotCounters{}, invalid
		}
	}
	raw, err := pilotCounterMap(previous.Raw)
	if err != nil {
		return PilotCounters{}, invalid
	}
	first := previous.StartedAt.IsZero()
	if first {
		if !previous.SampledAt.IsZero() || len(raw) != 0 {
			return PilotCounters{}, invalid
		}
	} else {
		if previous.StartedAt.Before(previous.CutoverAt) || previous.SampledAt.Before(previous.StartedAt) ||
			receipt.StartedAt.Before(previous.StartedAt) || receipt.SampledAt.Before(previous.SampledAt) ||
			len(raw) != len(totals) {
			return PilotCounters{}, invalid
		}
		for id, total := range totals {
			last, exists := raw[id]
			if !exists || last.UpBytes > total.UpBytes || last.DownBytes > total.DownBytes {
				return PilotCounters{}, invalid
			}
		}
	}
	sameProcess := !first && receipt.StartedAt.Equal(previous.StartedAt)
	if !first && !sameProcess && !receipt.StartedAt.After(previous.SampledAt) {
		return PilotCounters{}, invalid
	}
	next := previous
	next.Totals = make([]glivela.CounterSnapshot, 0, len(totals))
	for _, incoming := range receipt.Counters {
		deltaUp, deltaDown := incoming.UpBytes, incoming.DownBytes
		if sameProcess {
			last := raw[incoming.CredentialID]
			if incoming.UpBytes < last.UpBytes || incoming.DownBytes < last.DownBytes {
				return PilotCounters{}, invalid
			}
			if receipt.SampledAt.Equal(previous.SampledAt) && incoming != last {
				return PilotCounters{}, invalid
			}
			deltaUp -= last.UpBytes
			deltaDown -= last.DownBytes
		}
		total := totals[incoming.CredentialID]
		if deltaUp > math.MaxInt64-total.UpBytes || deltaDown > math.MaxInt64-total.DownBytes {
			return PilotCounters{}, invalid
		}
		total.UpBytes += deltaUp
		total.DownBytes += deltaDown
		next.Totals = append(next.Totals, total)
	}
	next.Raw = slices.Clone(receipt.Counters)
	next.StartedAt, next.SampledAt = receipt.StartedAt, receipt.SampledAt
	return next, nil
}

func pilotCounterMap(counters []glivela.CounterSnapshot) (map[string]glivela.CounterSnapshot, error) {
	result := make(map[string]glivela.CounterSnapshot, len(counters))
	for _, counter := range counters {
		_, duplicate := result[counter.CredentialID]
		if !glivela.ValidIdentifier(counter.CredentialID) || duplicate || counter.UpBytes < 0 || counter.DownBytes < 0 {
			return nil, errors.New("meridianruntime: invalid pilot counters")
		}
		result[counter.CredentialID] = counter
	}
	return result, nil
}
