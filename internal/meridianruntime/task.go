// Package meridianruntime defines the secret-bearing Vastora-to-Agent
// transport for one complete Meridian Xray revision. The durable account and
// routing model remains in github.com/petauron/meridian; this package only
// carries an already projected artifact across the authenticated task channel.
package meridianruntime

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/landing"
)

const (
	ApplyKind        = "meridian.runtime.apply"
	LegacyRetireKind = "meridian.legacy.retire"
)

type Command struct {
	EndpointID          string `json:"endpointId"`
	ReplacePendingState bool   `json:"replacePendingState,omitempty"`
}

func (c Command) Validate() error {
	if strings.TrimSpace(c.EndpointID) == "" || len(c.EndpointID) > meridian.MaxIdentifierLength {
		return errors.New("meridian runtime: invalid endpoint command")
	}
	return nil
}

type Task struct {
	ApplicationID         string                   `json:"applicationId"`
	ImageReference        string                   `json:"imageReference"`
	Desired               meridian.DesiredArtifact `json:"desired"`
	PreserveLegacyAliases bool                     `json:"preserveLegacyAliases"`
	RetireLegacy          bool                     `json:"retireLegacy"`
	ReplacePendingState   bool                     `json:"replacePendingState,omitempty"`
	Peers                 []Peer                   `json:"peers,omitempty"`
	Source                *landing.PeerIdentity    `json:"source,omitempty"`
}

func (t Task) Validate() error {
	if strings.TrimSpace(t.ApplicationID) == "" || len(t.ApplicationID) > meridian.MaxIdentifierLength || strings.TrimSpace(t.ImageReference) == "" || len(t.ImageReference) > 1024 || t.PreserveLegacyAliases && t.RetireLegacy || t.ReplacePendingState && t.RetireLegacy {
		return errors.New("meridian runtime: invalid task identity")
	}
	if err := t.Desired.Validate(); err != nil {
		return err
	}
	return t.validatePeers()
}

type Result struct {
	Receipt       meridian.AppliedReceipt `json:"receipt"`
	Stats         json.RawMessage         `json:"stats"`
	Usage         *UsageLedgerReport      `json:"usage,omitempty"`
	LegacyRetired bool                    `json:"legacyRetired"`
	Peers         []PeerObservation       `json:"peers,omitempty"`
	Source        *landing.PeerIdentity   `json:"source,omitempty"`
}

// UsageLedgerReport is cumulative within one durable Agent ledger. Stats in
// Result remains the raw Xray sample for the released Center during the Agent
// first rollout phase; the ledger is the next Center's accounting authority.
type UsageLedgerReport struct {
	ID            string          `json:"id"`
	Sequence      uint64          `json:"sequence"`
	Stats         json.RawMessage `json:"stats"`
	AffectedUsers []string        `json:"affectedUsers,omitempty"`
}

func (r UsageLedgerReport) Validate() error {
	id, err := hex.DecodeString(r.ID)
	if err != nil || len(id) != 16 || hex.EncodeToString(id) != r.ID || r.Sequence == 0 || r.Sequence > math.MaxInt64 || len(r.Stats) == 0 || len(r.Stats) > 4<<20 || !json.Valid(r.Stats) || len(r.AffectedUsers) > 65536 || !slices.IsSorted(r.AffectedUsers) {
		return errors.New("meridian runtime: invalid usage ledger report")
	}
	for index, user := range r.AffectedUsers {
		if strings.TrimSpace(user) == "" || len(user) > 512 || index > 0 && r.AffectedUsers[index-1] == user {
			return errors.New("meridian runtime: invalid affected usage user")
		}
	}
	return nil
}

// LegacyRetireTask authorizes removal of the superseded 3x-ui installation
// from a controller-only subscription host. Entry nodes retire through the
// verified runtime task so their Xray rollback boundary remains intact.
type LegacyRetireTask struct {
	ApplicationID string `json:"applicationId"`
}

func (t LegacyRetireTask) Validate() error {
	if strings.TrimSpace(t.ApplicationID) == "" || len(t.ApplicationID) > meridian.MaxIdentifierLength {
		return errors.New("meridian runtime: invalid legacy retirement task")
	}
	return nil
}

type LegacyRetireResult struct {
	LegacyRetired bool `json:"legacyRetired"`
}

func (r LegacyRetireResult) Validate() error {
	if !r.LegacyRetired {
		return errors.New("meridian runtime: legacy installation was not retired")
	}
	return nil
}

func (r Result) Validate(desired meridian.DesiredArtifact) error {
	if meridian.VerifyAppliedReceipt(desired, r.Receipt) != nil || len(r.Stats) == 0 || len(r.Stats) > 4<<20 || !json.Valid(r.Stats) {
		return errors.New("meridian runtime: invalid applied result")
	}
	if r.Usage != nil && r.Usage.Validate() != nil {
		return errors.New("meridian runtime: invalid usage ledger")
	}
	encoded, err := json.Marshal(r)
	if err != nil || len(encoded) > controlplane.MaxJSONPayload*3/4 {
		return errors.New("meridian runtime: result exceeds the heartbeat capacity")
	}
	return nil
}
