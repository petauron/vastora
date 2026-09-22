// Package meridianruntime defines the secret-bearing Vastora-to-Agent
// transport for one complete Meridian Xray revision. The durable account and
// routing model remains in github.com/petauron/meridian; this package only
// carries an already projected artifact across the authenticated task channel.
package meridianruntime

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/petauron/meridian"
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
}

func (t Task) Validate() error {
	if strings.TrimSpace(t.ApplicationID) == "" || len(t.ApplicationID) > meridian.MaxIdentifierLength || strings.TrimSpace(t.ImageReference) == "" || len(t.ImageReference) > 1024 || t.PreserveLegacyAliases && t.RetireLegacy || t.ReplacePendingState && t.RetireLegacy {
		return errors.New("meridian runtime: invalid task identity")
	}
	return t.Desired.Validate()
}

type Result struct {
	Receipt       meridian.AppliedReceipt `json:"receipt"`
	Stats         json.RawMessage         `json:"stats"`
	LegacyRetired bool                    `json:"legacyRetired"`
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
	return nil
}
