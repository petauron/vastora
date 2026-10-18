// Package nodesecurity defines the bounded, opt-in public-node security task.
// No task accepts commands, paths, credentials or arbitrary firewall fragments.
package nodesecurity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"slices"
	"time"
)

const Kind = "node.security"
const ConfirmationWindowSeconds = 300

type Rule struct {
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
}

type Policy struct {
	// Explicit interfaces, not inference from a public egress address. NAT and
	// globally routed IPv6 can expose a host without a local public IPv4.
	PublicInterfaces  []string `json:"publicInterfaces"`
	ManagementSources []string `json:"managementSources"`
	SSHPorts          []int    `json:"sshPorts"`
	PublicRules       []Rule   `json:"publicRules"`
}

type Task struct {
	Action   string `json:"action"`
	Revision string `json:"revision,omitempty"`
	Policy   Policy `json:"policy"`
	Address  string `json:"address,omitempty"`
}

type View struct {
	State     string   `json:"state"`
	Revision  string   `json:"revision,omitempty"`
	Deadline  string   `json:"deadline,omitempty"`
	AppliedAt string   `json:"appliedAt,omitempty"`
	Policy    Policy   `json:"policy"`
	Listeners []string `json:"listeners"`
	Banned    []string `json:"banned"`
	Reasons   []string `json:"reasons"`
	Supported bool     `json:"supported"`
}

var interfaceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (p Policy) Validate() error {
	if len(p.PublicInterfaces) == 0 || len(p.PublicInterfaces) > 4 || len(p.ManagementSources) == 0 || len(p.ManagementSources) > 8 || len(p.SSHPorts) == 0 || len(p.SSHPorts) > 4 || len(p.PublicRules) > 32 {
		return errors.New("security: explicit public interfaces, management sources and SSH ports are required")
	}
	for i, name := range p.PublicInterfaces {
		if !interfaceName.MatchString(name) || name == "lo" || name == "tailscale0" || name == "docker0" || slices.Contains(p.PublicInterfaces[:i], name) {
			return errors.New("security: invalid public interface")
		}
	}
	for i, raw := range p.ManagementSources {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix != prefix.Masked() || prefix.Addr().Is4In6() || prefix.Addr().IsUnspecified() || prefix.Addr().IsMulticast() || prefix.Bits() < 24 && prefix.Addr().Is4() || prefix.Bits() < 64 && prefix.Addr().Is6() || slices.Contains(p.ManagementSources[:i], raw) {
			return errors.New("security: management sources must be explicit canonical IPv4 /24-or-narrower or IPv6 /64-or-narrower prefixes")
		}
	}
	for i, port := range p.SSHPorts {
		if port < 1 || port > 65535 || slices.Contains(p.SSHPorts[:i], port) {
			return errors.New("security: invalid SSH port")
		}
	}
	for i, rule := range p.PublicRules {
		if rule.Port < 1 || rule.Port > 65535 || rule.Protocol != "tcp" && rule.Protocol != "udp" || rule.Protocol == "tcp" && slices.Contains(p.SSHPorts, rule.Port) || slices.Contains(p.PublicRules[:i], rule) {
			return errors.New("security: invalid public service rule; SSH cannot be public to all sources")
		}
	}
	return nil
}

func (p Policy) Digest() string {
	encoded, _ := json.Marshal(p)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (t Task) Validate() error {
	switch t.Action {
	case "inspect", "prepare":
		return nil
	case "preview":
		return t.Policy.Validate()
	case "apply":
		if t.Policy.Validate() != nil || !digestPattern.MatchString(t.Revision) {
			return errors.New("security: apply must match the reviewed preview revision")
		}
	case "confirm", "disable":
		if !digestPattern.MatchString(t.Revision) {
			return errors.New("security: exact security revision is required")
		}
	case "unban":
		address, err := netip.ParseAddr(t.Address)
		if err != nil || address.Is4In6() || address.IsUnspecified() || address.IsMulticast() || address.String() != t.Address {
			return errors.New("security: invalid ban address")
		}
		if !digestPattern.MatchString(t.Revision) {
			return errors.New("security: exact security revision is required")
		}
	default:
		return errors.New("security: unsupported action")
	}
	return nil
}

func (v View) Validate() error {
	if len(v.Listeners) > 256 || len(v.Banned) > 256 || len(v.Reasons) > 16 {
		return errors.New("security: oversized result")
	}
	for _, list := range [][]string{v.Listeners, v.Banned, v.Reasons} {
		for _, s := range list {
			if len(s) > 256 {
				return errors.New("security: oversized result field")
			}
		}
	}
	switch v.State {
	case "unconfigured", "preview", "pending_confirmation", "verified", "rolled_back", "action_required":
	default:
		return errors.New("security: invalid state")
	}
	if v.Revision != "" && !digestPattern.MatchString(v.Revision) {
		return errors.New("security: invalid revision")
	}
	if v.State == "preview" || v.State == "pending_confirmation" || v.State == "verified" {
		if v.Policy.Validate() != nil || !digestPattern.MatchString(v.Revision) {
			return errors.New("security: result policy does not match revision")
		}
	}
	if v.State == "pending_confirmation" {
		if _, err := time.Parse(time.RFC3339Nano, v.Deadline); err != nil {
			return errors.New("security: invalid confirmation deadline")
		}
	}
	for _, raw := range v.Banned {
		ip, err := netip.ParseAddr(raw)
		if err != nil || ip.String() != raw {
			return errors.New("security: invalid ban result")
		}
	}
	return nil
}

func (v View) ValidateFor(t Task) error {
	if err := v.Validate(); err != nil {
		return err
	}
	want := ""
	switch t.Action {
	case "prepare":
		want = "unconfigured"
	case "preview":
		want = "preview"
	case "apply":
		if v.State != "verified" {
			want = "pending_confirmation"
		}
	case "confirm", "unban":
		want = "verified"
	case "disable":
		want = "rolled_back"
	}
	if want != "" && v.State != want {
		return errors.New("security: result does not match operation")
	}
	if t.Action == "preview" || t.Action == "apply" {
		if v.Revision != t.Revision || v.Policy.Digest() != t.Policy.Digest() {
			return errors.New("security: result differs from reviewed policy")
		}
	} else if t.Action != "inspect" && t.Action != "prepare" && v.Revision != t.Revision {
		return errors.New("security: stale result revision")
	}
	return nil
}
