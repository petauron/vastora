package agent

import (
	"fmt"
	"testing"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
)

func TestMeridianLegacyChildCoverageFencesUnimportedAgentGrant(t *testing.T) {
	const fixedUUID = "22222222-2222-4222-8222-222222222222"
	grant := landing.ClientGrant{ID: "old-child", Mode: landing.FixedMode, Enabled: true, FixedIdentity: landing.Identity(fixedUUID)}
	config := []byte(fmt.Sprintf(`{"inbounds":[{"protocol":"vless","settings":{"clients":[{"email":%q,"id":%q}]}}]}`,
		meridian.RouteUser(grant.ID), fixedUUID))
	if err := meridianLegacyChildCoverage([]landing.ClientGrant{grant}, config); err != nil {
		t.Fatalf("matching fixed child was rejected: %v", err)
	}
	if err := meridianLegacyChildCoverage([]landing.ClientGrant{grant}, []byte(`{"inbounds":[]}`)); err == nil {
		t.Fatal("unimported fixed child did not block retirement")
	}
	if err := meridianLegacyChildCoverage([]landing.ClientGrant{grant}, []byte(fmt.Sprintf(`{"inbounds":[{"protocol":"vless","settings":{"clients":[{"email":%q,"id":"33333333-3333-4333-8333-333333333333"}]}}]}`,
		meridian.RouteUser(grant.ID)))); err == nil {
		t.Fatal("changed child UUID did not block retirement")
	}
	grant.Enabled = false
	if err := meridianLegacyChildCoverage([]landing.ClientGrant{grant}, []byte(`{"inbounds":[]}`)); err != nil {
		t.Fatalf("disabled legacy child still blocked retirement: %v", err)
	}
}
