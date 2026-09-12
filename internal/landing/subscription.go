package landing

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// SubscriptionGrant is constructed only after the controller has resolved the
// subscription's parent identity and confirmed the applied grant revision.
// Native links remain the source of protocol credentials; never reconstruct
// REALITY keys from browser input or expose these values in task events.
type SubscriptionGrant struct {
	Grant       ClientGrant
	EntryName   string
	LandingName string
	BaseLink    string
	FixedLink   string
}

// ComposeLinks preserves original lines verbatim. Distinct combination UUIDs
// keep ordinary converters from merging nodes which differ only by display name.
func ComposeLinks(native []byte, parentID string, mode PublishingMode, grants []SubscriptionGrant, encoded bool) ([]byte, error) {
	if len(native) > 4<<20 || !validGrantID(parentID) || !mode.Valid() {
		return nil, errors.New("landing: invalid subscription input")
	}
	plain := native
	if encoded {
		var err error
		plain, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(native)))
		if err != nil {
			return nil, errors.New("landing: invalid native subscription")
		}
	}
	lines := strings.Split(strings.TrimRight(string(plain), "\r\n"), "\n")
	identities := map[string]bool{}
	for _, line := range lines {
		if link, err := parseVLESSLink(strings.TrimSpace(line)); err == nil {
			identities[link.User.Username()] = true
		}
	}
	for _, item := range grants {
		if err := validateSubscriptionGrant(item, parentID); err != nil {
			return nil, err
		}
		if !item.Grant.Enabled || !mode.Fixed() || !item.Grant.Mode.Fixed() {
			continue
		}
		base, err := parseVLESSLink(item.BaseLink)
		if err != nil || !identities[base.User.Username()] || !slices.ContainsFunc(lines, func(line string) bool { return sameLinkIdentity(strings.TrimSpace(line), item.BaseLink) }) {
			return nil, errors.New("landing: combination does not belong to this subscription")
		}
		fixed, err := parseVLESSLink(item.FixedLink)
		if err != nil || fixed.Host != base.Host || !sameRealityTransport(fixed, base) || identities[fixed.User.Username()] {
			return nil, errors.New("landing: invalid combination credentials")
		}
		fixed.Fragment = combinationName(item)
		lines = append(lines, fixed.String())
		identities[fixed.User.Username()] = true
	}
	result := []byte(strings.Join(lines, "\n") + "\n")
	if encoded {
		result = []byte(base64.StdEncoding.EncodeToString(result))
	}
	return result, nil
}

// ComposeMihomo augments the native per-user configuration, never the upstream
// global template. Existing rules/groups are retained; a new explicit strategy
// choice is added to select groups rather than replacing users' rule semantics.
func ComposeMihomo(native []byte, parentID string, mode PublishingMode, grants []SubscriptionGrant) ([]byte, error) {
	if len(native) > 4<<20 || !validGrantID(parentID) || !mode.Valid() {
		return nil, errors.New("landing: invalid subscription input")
	}
	var config map[string]any
	if yaml.Unmarshal(native, &config) != nil || config == nil {
		return nil, errors.New("landing: invalid native Mihomo configuration")
	}
	proxies, ok := config["proxies"].([]any)
	if !ok {
		return nil, errors.New("landing: native subscription has no proxy inventory")
	}
	groups, ok := config["proxy-groups"].([]any)
	if !ok {
		return nil, errors.New("landing: a full Mihomo configuration is required")
	}
	names := map[string]bool{"DIRECT": true, "REJECT": true}
	for _, values := range [][]any{proxies, groups} {
		for _, value := range values {
			entry, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("landing: invalid native subscription entry")
			}
			name, _ := entry["name"].(string)
			if name == "" || names[name] {
				return nil, errors.New("landing: ambiguous native subscription names")
			}
			names[name] = true
		}
	}
	reserve := func(name string) error {
		if names[name] {
			return errors.New("landing: managed subscription name conflicts with native configuration")
		}
		names[name] = true
		return nil
	}
	baseProxies := slices.Clone(proxies)
	fixedNames, entryNames := []any{}, []any{}
	landingEntries := map[string][]any{}
	landingItems := map[string]SubscriptionGrant{}
	for _, item := range grants {
		if err := validateSubscriptionGrant(item, parentID); err != nil {
			return nil, err
		}
		if !item.Grant.Enabled {
			continue
		}
		base, err := parseVLESSLink(item.BaseLink)
		if err != nil {
			return nil, err
		}
		var proxy map[string]any
		for _, value := range baseProxies {
			candidate := value.(map[string]any)
			if candidate["type"] == "vless" && candidate["uuid"] == base.User.Username() && candidate["server"] == base.Hostname() && fmt.Sprint(candidate["port"]) == base.Port() {
				if proxy != nil {
					return nil, errors.New("landing: ambiguous native entry identity")
				}
				proxy = candidate
			}
		}
		if proxy == nil || proxy["dialer-proxy"] != nil || !sameMihomoRealityTransport(proxy, base) {
			return nil, errors.New("landing: native entry does not match this user")
		}
		if mode.Fixed() && item.Grant.Mode.Fixed() {
			fixed, err := parseVLESSLink(item.FixedLink)
			if err != nil || fixed.Host != base.Host || !sameRealityTransport(fixed, base) || fixed.User.Username() == base.User.Username() {
				return nil, errors.New("landing: invalid fixed subscription identity")
			}
			for _, value := range proxies {
				if value.(map[string]any)["uuid"] == fixed.User.Username() {
					return nil, errors.New("landing: duplicated fixed subscription identity")
				}
			}
			name := combinationName(item)
			if err := reserve(name); err != nil {
				return nil, err
			}
			clone := make(map[string]any, len(proxy))
			for key, value := range proxy {
				clone[key] = value
			}
			clone["name"], clone["uuid"], clone["udp"] = name, fixed.User.Username(), false
			proxies = append(proxies, clone)
			fixedNames = append(fixedNames, name)
		}
		if mode.Advanced() && item.Grant.Mode.Advanced() {
			name := proxy["name"].(string)
			key := item.Grant.Peer.ID
			if prior, exists := landingItems[key]; exists && prior.Grant.Peer != item.Grant.Peer {
				return nil, errors.New("landing: landing identity changed")
			}
			landingItems[key] = item
			if !slices.Contains(landingEntries[key], any(name)) {
				landingEntries[key] = append(landingEntries[key], name)
			}
			if !slices.Contains(entryNames, any(name)) {
				entryNames = append(entryNames, name)
			}
		}
	}
	choices := slices.Clone(fixedNames)
	if len(landingEntries) > 0 {
		defaultGroup := "入口默认出口 · Vastora"
		if err := reserve(defaultGroup); err != nil {
			return nil, err
		}
		managedGroups := []any{map[string]any{"name": defaultGroup, "type": "select", "proxies": entryNames}}
		landingChoices := []any{defaultGroup}
		keys := make([]string, 0, len(landingEntries))
		for key := range landingEntries {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			item := landingItems[key]
			suffix := grantTag(key)[:8]
			entryGroup, landingName := "入口 → "+item.LandingName+" · "+suffix, "落地 · "+item.LandingName+" · "+suffix
			if err := reserve(entryGroup); err != nil {
				return nil, err
			}
			if err := reserve(landingName); err != nil {
				return nil, err
			}
			managedGroups = append(managedGroups, map[string]any{"name": entryGroup, "type": "select", "proxies": landingEntries[key]})
			proxies = append(proxies, map[string]any{"name": landingName, "type": "socks5", "server": item.Grant.Peer.Address, "port": SOCKSPort, "udp": false, "dialer-proxy": entryGroup})
			landingChoices = append(landingChoices, landingName)
		}
		strategy := "自选落地 · Vastora"
		if err := reserve(strategy); err != nil {
			return nil, err
		}
		managedGroups = append(managedGroups, map[string]any{"name": strategy, "type": "select", "proxies": landingChoices})
		choices = append(choices, strategy)
		// Attach only to original select groups. New per-landing entry groups
		// must never contain fixed identities or a group pointing back to them.
		for _, value := range groups {
			group := value.(map[string]any)
			if group["type"] == "select" {
				if members, ok := group["proxies"].([]any); ok {
					group["proxies"] = append(members, choices...)
				}
			}
		}
		groups = append(groups, managedGroups...)
	} else {
		for _, value := range groups {
			group := value.(map[string]any)
			if group["type"] == "select" {
				if members, ok := group["proxies"].([]any); ok {
					group["proxies"] = append(members, choices...)
				}
			}
		}
	}
	config["proxies"], config["proxy-groups"] = proxies, groups
	output, err := yaml.Marshal(config)
	if err != nil {
		return nil, errors.New("landing: cannot encode Mihomo subscription")
	}
	return output, nil
}

func validateSubscriptionGrant(item SubscriptionGrant, parentID string) error {
	if err := item.Grant.Validate(); err != nil {
		return err
	}
	if item.Grant.ParentID != parentID || item.EntryName == "" || item.LandingName == "" || len(item.EntryName)+len(item.LandingName) > 512 {
		return errors.New("landing: subscription grant scope does not match")
	}
	base, err := parseVLESSLink(item.BaseLink)
	if err != nil || Identity(base.User.Username()) != item.Grant.BaseIdentity {
		return errors.New("landing: base identity changed")
	}
	if item.Grant.Enabled && item.Grant.Mode.Fixed() {
		fixed, err := parseVLESSLink(item.FixedLink)
		if err != nil || Identity(fixed.User.Username()) != item.Grant.FixedIdentity {
			return errors.New("landing: fixed identity changed")
		}
	}
	return nil
}

func combinationName(item SubscriptionGrant) string {
	return item.EntryName + " → " + item.LandingName + " · " + grantTag(item.Grant.ID)[:8]
}

func parseVLESSLink(raw string) (*url.URL, error) {
	link, err := url.Parse(raw)
	if err != nil || link.Scheme != "vless" || link.User == nil || link.User.Username() == "" || link.Hostname() == "" || link.Port() == "" || link.Query().Get("security") != "reality" || link.Path != "" {
		return nil, errors.New("landing: a managed VLESS REALITY link is required")
	}
	if _, password := link.User.Password(); password {
		return nil, errors.New("landing: invalid native identity")
	}
	return link, nil
}

func sameLinkIdentity(left, right string) bool {
	a, err := parseVLESSLink(left)
	if err != nil {
		return false
	}
	b, err := parseVLESSLink(right)
	return err == nil && a.Host == b.Host && a.User.Username() == b.User.Username() && sameRealityTransport(a, b)
}

// Native exporters may order/escape query fields differently. Compare the
// actual transport and REALITY authentication fields, not serialized order or
// cosmetic client preferences. Neither credential nor destination may change.
func sameRealityTransport(a, b *url.URL) bool {
	left, right := a.Query(), b.Query()
	for _, key := range []string{"security", "type", "flow", "sni", "pbk", "sid"} {
		if len(left[key]) > 1 || len(right[key]) > 1 || left.Get(key) != right.Get(key) {
			return false
		}
	}
	return true
}

func sameMihomoRealityTransport(proxy map[string]any, base *url.URL) bool {
	query := base.Query()
	text := func(key string) string { value, _ := proxy[key].(string); return value }
	reality, ok := proxy["reality-opts"].(map[string]any)
	if !ok || proxy["tls"] != true || proxy["skip-cert-verify"] == true || query.Get("pbk") == "" || query.Get("sni") == "" {
		return false
	}
	if text("servername") != query.Get("sni") || text("flow") != query.Get("flow") || reality["public-key"] != query.Get("pbk") {
		return false
	}
	short, _ := reality["short-id"].(string)
	network := text("network")
	if network == "" {
		network = "tcp"
	}
	return short == query.Get("sid") && network == query.Get("type")
}
