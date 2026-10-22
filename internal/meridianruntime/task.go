// Package meridianruntime defines the secret-bearing Vastora-to-Agent
// transport for one complete Meridian Xray revision. The durable account and
// routing model remains in github.com/petauron/glivela; this package only
// carries an already projected artifact across the authenticated task channel.
package meridianruntime

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/landing"
)

const (
	ApplyKind        = "meridian.runtime.apply"
	LegacyRetireKind = "meridian.legacy.retire"
)

type Command struct {
	EndpointID          string `json:"endpointId"`
	ReplacePendingState bool   `json:"replacePendingState,omitempty"`
	ResumeApplied       bool   `json:"resumeApplied,omitempty"`
}

func (c Command) Validate() error {
	if strings.TrimSpace(c.EndpointID) == "" || len(c.EndpointID) > glivela.MaxIdentifierLength || c.ResumeApplied && c.ReplacePendingState {
		return errors.New("meridian runtime: invalid endpoint command")
	}
	return nil
}

type Task struct {
	NativeEgress        glivela.EgressPolicy    `json:"nativeEgress,omitempty"`
	EgressClients       []AcceptanceClient      `json:"egressClients,omitempty"`
	ApplicationID       string                  `json:"applicationId"`
	ImageReference      string                  `json:"imageReference"`
	Desired             glivela.DesiredArtifact `json:"desired"`
	RetireLegacy        bool                    `json:"retireLegacy"`
	ReplacePendingState bool                    `json:"replacePendingState,omitempty"`
	ResumeApplied       bool                    `json:"resumeApplied,omitempty"`
	Peers               []Peer                  `json:"peers,omitempty"`
	Source              *landing.PeerIdentity   `json:"source,omitempty"`
}

func (t Task) Validate() error {
	if t.ResumeApplied && (t.ReplacePendingState || t.RetireLegacy) {
		return errors.New("meridian runtime: monitor recovery cannot replace or retire a runtime")
	}
	if strings.TrimSpace(t.ApplicationID) == "" || len(t.ApplicationID) > glivela.MaxIdentifierLength || strings.TrimSpace(t.ImageReference) == "" || len(t.ImageReference) > 1024 || t.ReplacePendingState && t.RetireLegacy {
		return errors.New("meridian runtime: invalid task identity")
	}
	if err := t.Desired.Validate(); err != nil {
		return err
	}
	if err := t.validateEgress(); err != nil {
		return err
	}
	return t.validatePeers()
}

type Result struct {
	Egress        *EgressObservation     `json:"egress,omitempty"`
	Receipt       glivela.AppliedReceipt `json:"receipt"`
	Stats         json.RawMessage        `json:"stats"`
	LegacyRetired bool                   `json:"legacyRetired"`
	Peers         []PeerObservation      `json:"peers,omitempty"`
	Source        *landing.PeerIdentity  `json:"source,omitempty"`
}

// LegacyRetireTask authorizes removal of the superseded 3x-ui installation
// from a controller-only subscription host. Entry nodes retire through the
// verified runtime task so their Xray rollback boundary remains intact.
type LegacyRetireTask struct {
	ApplicationID string `json:"applicationId"`
}

func (t LegacyRetireTask) Validate() error {
	if strings.TrimSpace(t.ApplicationID) == "" || len(t.ApplicationID) > glivela.MaxIdentifierLength {
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

func (r Result) Validate(desired glivela.DesiredArtifact) error {
	if glivela.VerifyAppliedReceipt(desired, r.Receipt) != nil || len(r.Stats) == 0 || len(r.Stats) > 4<<20 || !json.Valid(r.Stats) {
		return errors.New("meridian runtime: invalid applied result")
	}
	return nil
}
