package landing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
)

// Publishing modes also constrain installed identities and routes. They are
// not merely a presentation preference on the subscription response.
type PublishingMode string

const (
	FixedMode    PublishingMode = "fixed"
	AdvancedMode PublishingMode = "advanced"
	BothMode     PublishingMode = "both"
)

func (m PublishingMode) Valid() bool    { return m == FixedMode || m == AdvancedMode || m == BothMode }
func (m PublishingMode) Fixed() bool    { return m == FixedMode || m == BothMode }
func (m PublishingMode) Advanced() bool { return m == AdvancedMode || m == BothMode }

func (g ClientGrant) Published(mode PublishingMode) ClientGrant {
	fixed, advanced := mode.Fixed() && g.Mode.Fixed(), mode.Advanced() && g.Mode.Advanced()
	switch {
	case fixed && advanced:
		g.Mode = BothMode
	case fixed:
		g.Mode = FixedMode
	case advanced:
		g.Mode = AdvancedMode
	default:
		g.Enabled = false
	}
	return g
}

// ClientGrant contains no authentication secret. Center resolves IDs against
// managed inventory; Agent separately checks these user/inbound associations
// against its actual local Xray inventory before applying a route.
type ClientGrant struct {
	ID            string         `json:"id"`
	ParentID      string         `json:"parentId"`
	InboundTag    string         `json:"inboundTag"`
	BaseUser      string         `json:"baseUser"`
	BaseIdentity  string         `json:"baseIdentity"`
	FixedUser     string         `json:"fixedUser,omitempty"`
	FixedIdentity string         `json:"fixedIdentity,omitempty"`
	Peer          PeerIdentity   `json:"peer"`
	Mode          PublishingMode `json:"mode"`
	Enabled       bool           `json:"enabled"`
}

func (g ClientGrant) Validate() error {
	if !validGrantID(g.ID) || !validGrantID(g.ParentID) || !g.Mode.Valid() ||
		!validRouteUser(g.BaseUser) || strings.HasPrefix(g.BaseUser, "vastora-combination-") || !validIdentity(g.BaseIdentity) || g.ParentID != g.BaseIdentity || g.InboundTag == "" || len(g.InboundTag) > 128 ||
		strings.TrimSpace(g.InboundTag) != g.InboundTag || !tailnetIPv4(g.Peer.Address) ||
		g.Peer.ID == "" || g.Peer.PublicKey == "" {
		return errors.New("landing: invalid client grant")
	}
	// A disabled fixed credential remains explicitly rejected until its actual
	// deletion and existing-session termination have both been confirmed.
	if g.FixedUser != "" && (g.FixedUser != FixedUser(g.ID) || !validIdentity(g.FixedIdentity)) {
		return errors.New("landing: invalid fixed identity ownership")
	}
	if g.Enabled && g.Mode.Fixed() && g.FixedUser == "" {
		return errors.New("landing: missing fixed identity")
	}
	return nil
}

func validGrantID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validRouteUser(user string) bool {
	return user != "" && len(user) <= 256 && strings.TrimSpace(user) == user && !strings.ContainsAny(user, "\r\n\x00")
}

func grantTag(id string) string {
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:16])
}

func FixedUser(id string) string     { return "vastora-combination-" + grantTag(id) }
func FixedOutbound(id string) string { return "vastora-combination-" + grantTag(id) }

// Identity binds a grant to authentication material instead of a reusable name.
func Identity(uuid string) string {
	if uuid == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(uuid))
	return hex.EncodeToString(digest[:])
}

func validIdentity(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(id) == id
}

// MergeSources computes the union, including protocol capability, before any
// writer emits Headscale, Dante or firewall configuration. Dropping one use
// cannot remove another use of the same source address.
func MergeSources(uses ...[]AuthorizedNode) ([]AuthorizedNode, error) {
	byAddress := map[string]AuthorizedNode{}
	for _, use := range uses {
		for _, source := range use {
			if !tailnetIPv4(source.Address) {
				return nil, errors.New("landing: invalid source identity")
			}
			if previous, found := byAddress[source.Address]; found {
				source.TCPOnly = source.TCPOnly && previous.TCPOnly
			}
			byAddress[source.Address] = source
		}
	}
	if len(byAddress) > 128 {
		return nil, errors.New("landing: too many source identities")
	}
	result := make([]AuthorizedNode, 0, len(byAddress))
	for _, source := range byAddress {
		result = append(result, source)
	}
	slices.SortFunc(result, func(a, b AuthorizedNode) int { return strings.Compare(a.Address, b.Address) })
	return result, nil
}
