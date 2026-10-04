package meridianruntime

import (
	"errors"
	"net/netip"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
)

// EgressImage is selected explicitly by a node policy change. Existing nodes
// without a policy retain their installed package runtime until managed update.
const EgressImage = "ghcr.io/xtls/xray-core:26.9.30@sha256:6d30597c3e729b5dc1faac8dbc0138fbbe6d4d787bfb54120ea48f14e2e7b5ca"

type EgressObservation struct {
	Policy       meridian.EgressPolicy `json:"policy"`
	ConfigSHA256 string                `json:"configSha256"`
	Exits        []string              `json:"exits"`
	CheckedAt    time.Time             `json:"checkedAt"`
}

func MatchesEgress(policy meridian.EgressPolicy, exit string) bool {
	ip, err := netip.ParseAddr(exit)
	if err != nil || !landing.PublicIP(ip) {
		return false
	}
	switch policy {
	case meridian.EgressAuto:
		return true
	case meridian.EgressIPv4Only:
		return ip.Unmap().Is4()
	case meridian.EgressIPv6Only:
		return ip.Is6() && !ip.Is4In6()
	default:
		return false
	}
}

func (t Task) validateEgress() error {
	if t.NativeEgress == "" {
		if len(t.EgressClients) != 0 {
			return errors.New("egress: unexpected clients")
		}
		return nil
	}
	if t.NativeEgress.Validate() != nil || t.ImageReference != EgressImage || len(t.EgressClients) > 2 {
		return errors.New("egress: unsupported policy, core or verification clients")
	}
	seen := map[meridian.ProtocolKind]bool{}
	for _, c := range t.EgressClients {
		if c.Material.Credential.Kind != meridian.NativeCredential || seen[c.Protocol] {
			return errors.New("egress: verification must use distinct native protocols")
		}
		if _, err := c.Config(1080); err != nil {
			return err
		}
		seen[c.Protocol] = true
	}
	return nil
}

func (r Result) VerifyEgress(t Task, now time.Time) error {
	if t.NativeEgress == "" || len(t.EgressClients) == 0 {
		return nil
	}
	o := r.Egress
	if o == nil || o.Policy != t.NativeEgress || o.ConfigSHA256 != t.Desired.ConfigSHA256 || len(o.Exits) != len(t.EgressClients) || o.CheckedAt.After(now.Add(time.Minute)) || now.Sub(o.CheckedAt) > 5*time.Minute {
		return errors.New("egress: missing or stale native client verification")
	}
	for _, ip := range o.Exits {
		if !MatchesEgress(o.Policy, ip) {
			return errors.New("egress: observed address family does not match policy")
		}
	}
	return nil
}
