package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
)

// Before discarding the legacy installation, prove that every still-enabled
// fixed child credential in its applied journal exists in the native artifact.
// Center may not know about orphaned Agent children, so Center route counts
// alone are not sufficient retirement evidence.
func (s *Store) verifyMeridianLegacyChildCoverage(ctx context.Context, applied meridian.DesiredArtifact) error {
	legacy, err := s.landingRuntime(ctx)
	if err != nil {
		return err
	}
	if legacy == nil || legacy.Desired.Clients == nil {
		return nil
	}
	return meridianLegacyChildCoverage(legacy.Desired.Clients.Grants, applied.Config)
}

func meridianLegacyChildCoverage(grants []landing.ClientGrant, config []byte) error {
	var rendered struct {
		Inbounds []struct {
			Protocol string `json:"protocol"`
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
					ID    string `json:"id"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(config, &rendered) != nil {
		return errors.New("agent: Meridian artifact cannot prove legacy child coverage")
	}
	identities := map[string]string{}
	for _, inbound := range rendered.Inbounds {
		if inbound.Protocol != "vless" {
			continue
		}
		for _, client := range inbound.Settings.Clients {
			if _, duplicate := identities[client.Email]; duplicate {
				return errors.New("agent: Meridian artifact has duplicate child users")
			}
			identities[client.Email] = landing.Identity(client.ID)
		}
	}
	for _, grant := range grants {
		if !grant.Enabled || !grant.Mode.Fixed() {
			continue
		}
		if identities[meridian.RouteUser(grant.ID)] != grant.FixedIdentity {
			return errors.New("agent: legacy fixed child is missing from Meridian; retirement requires explicit reconciliation")
		}
	}
	return nil
}
