package landing

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

const grantDenyTag = "vastora-client-landing-deny"
const grantPrivateTag = "vastora-client-landing-private-"

// PrepareClientRoutes composes user grants on top of the same original
// configuration used for forced landing. The returned checkpoint is owned by
// the existing route writer, not a second API/script that patches live JSON.
func PrepareClientRoutes(raw json.RawMessage, revision uint64, forced *ProxyPlan, grants []ClientGrant) (RouteChange, error) {
	if revision == 0 || len(grants) > 512 {
		return RouteChange{}, errors.New("landing: invalid client route plan")
	}
	_, before, err := decodeRouteSettings(raw)
	if err != nil {
		return RouteChange{}, err
	}
	base := before
	if forced != nil {
		change, err := PrepareRouteChange(base, revision, forced.InboundTags, forced.Peer)
		if err != nil {
			return RouteChange{}, err
		}
		base = change.After
	}
	config, _, err := decodeRouteSettings(base)
	if err != nil {
		return RouteChange{}, err
	}
	outbounds, ok := config["outbounds"].([]any)
	if !ok || len(outbounds) == 0 {
		return RouteChange{}, errors.New("landing: missing original outbounds")
	}
	routing, ok := config["routing"].(map[string]any)
	if !ok {
		return RouteChange{}, errors.New("landing: missing original routing")
	}
	rules, ok := routing["rules"].([]any)
	if !ok {
		return RouteChange{}, errors.New("landing: missing original routing rules")
	}
	strategy, _ := routing["domainStrategy"].(string)
	if strategy != "" && strategy != "AsIs" && strategy != "IPIfNonMatch" {
		return RouteChange{}, errors.New("landing: original routing requires local DNS resolution")
	}
	known, blocked := map[string]bool{}, map[string]bool{}
	for _, value := range outbounds {
		outbound, ok := value.(map[string]any)
		if !ok {
			return RouteChange{}, errors.New("landing: invalid original outbound")
		}
		tag, _ := outbound["tag"].(string)
		protocol, _ := outbound["protocol"].(string)
		if tag == "" || known[tag] || protocol == "" || tag == grantDenyTag || strings.HasPrefix(tag, grantPrivateTag) || strings.HasPrefix(tag, "vastora-combination-") {
			return RouteChange{}, errors.New("landing: conflicting client route ownership")
		}
		known[tag], blocked[tag] = true, protocol == "blackhole"
	}
	if api, ok := config["api"].(map[string]any); ok {
		if tag, ok := api["tag"].(string); ok && tag != "" {
			if known[tag] || tag == grantDenyTag || strings.HasPrefix(tag, grantPrivateTag) {
				return RouteChange{}, errors.New("landing: ambiguous API route")
			}
			known[tag] = true
		}
	}
	ordered := slices.Clone(grants)
	slices.SortFunc(ordered, func(a, b ClientGrant) int { return strings.Compare(a.ID, b.ID) })
	ids, fixedUsers, scopes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, grant := range ordered {
		if err := grant.Validate(); err != nil {
			return RouteChange{}, err
		}
		scope := grant.ParentID + "\x00" + grant.InboundTag + "\x00" + grant.Peer.ID
		if ids[grant.ID] || scopes[scope] || grant.FixedUser != "" && fixedUsers[grant.FixedUser] {
			return RouteChange{}, errors.New("landing: duplicate client grant")
		}
		ids[grant.ID], scopes[scope] = true, true
		if grant.FixedUser != "" {
			fixedUsers[grant.FixedUser] = true
		}
	}
	for _, grant := range ordered {
		if fixedUsers[grant.BaseUser] {
			return RouteChange{}, errors.New("landing: combination cannot be an advanced entry")
		}
	}
	prefix := []any{}
	// Keep all explicit security rejects except the standard private-address
	// blocker before advanced exceptions. Only that well-understood blocker
	// can be crossed, and only by the exact authenticated tuple below.
	for _, value := range rules {
		rule, ok := value.(map[string]any)
		if !ok {
			return RouteChange{}, errors.New("landing: invalid original routing rule")
		}
		outbound, _ := rule["outboundTag"].(string)
		if !known[outbound] {
			return RouteChange{}, errors.New("landing: unknown original outbound")
		}
		for _, field := range []string{"inboundTag", "user"} {
			if value, exists := rule[field]; exists {
				values, ok := value.([]any)
				if !ok {
					return RouteChange{}, errors.New("landing: malformed original route scope")
				}
				for _, value := range values {
					if _, ok := value.(string); !ok {
						return RouteChange{}, errors.New("landing: malformed original route scope")
					}
				}
			}
		}
		if rule["balancerTag"] != nil {
			return RouteChange{}, errors.New("landing: ambiguous original security policy")
		}
		if !blocked[outbound] || standardPrivateBlock(rule) {
			continue
		}
		prefix = append(prefix, scopedGrantRejects(rule, ordered)...)
	}
	for _, grant := range ordered {
		if !grant.Enabled || !grant.Mode.Advanced() {
			continue
		}
		tag := grantPrivateTag + grantTag(grant.Peer.ID)
		prefix = append(prefix, map[string]any{
			"type": "field", "inboundTag": []string{grant.InboundTag}, "user": []string{grant.BaseUser},
			"ip": []string{grant.Peer.Address + "/32"}, "port": "1080", "network": "tcp", "outboundTag": tag,
		})
		if !known[tag] {
			// Pin the actual socket as well as the routing predicate. Sniffing or
			// destination rewriting cannot turn this into arbitrary private access.
			outbounds = append(outbounds, map[string]any{"tag": tag, "protocol": "freedom", "settings": map[string]any{"domainStrategy": "AsIs", "redirect": grant.Peer.Address + ":1080", "finalRules": []any{
				map[string]any{"action": "allow", "network": "tcp", "ip": []string{grant.Peer.Address + "/32"}, "port": "1080"},
				map[string]any{"action": "block"},
			}}})
			known[tag] = true
		}
	}
	// Explicitly deny other private SOCKS tuples for each scoped identity,
	// including UDP when another purpose shares this source's relay ACL.
	for _, grant := range ordered {
		prefix = append(prefix, map[string]any{"type": "field", "inboundTag": []string{grant.InboundTag}, "user": []string{grant.BaseUser}, "ip": []string{grant.Peer.Address + "/32"}, "outboundTag": grantDenyTag})
	}
	// Fixed identities must encounter every security rejection before the
	// fixed route; they never receive the advanced private-address exception.
	for _, value := range rules {
		rule := value.(map[string]any)
		outbound, _ := rule["outboundTag"].(string)
		if blocked[outbound] {
			prefix = append(prefix, scopedGrantRejects(rule, ordered)...)
		}
	}
	for _, grant := range ordered {
		if grant.FixedUser == "" {
			continue
		}
		scope := func(network, outbound string) map[string]any {
			return map[string]any{"type": "field", "inboundTag": []string{grant.InboundTag}, "user": []string{grant.FixedUser}, "network": network, "outboundTag": outbound}
		}
		if grant.Enabled && grant.Mode.Fixed() {
			prefix = append(prefix, scope("udp", grantDenyTag), scope("tcp", FixedOutbound(grant.ID)))
			outbounds = append(outbounds, map[string]any{"tag": FixedOutbound(grant.ID), "protocol": "socks", "settings": map[string]any{"servers": []any{map[string]any{"address": grant.Peer.Address, "port": SOCKSPort}}}})
		}
		// Unscoped by inbound on purpose: stale/misattached child credentials
		// must never fall through into another inbound's default route.
		prefix = append(prefix, map[string]any{"type": "field", "user": []string{grant.FixedUser}, "outboundTag": grantDenyTag})
	}
	outbounds = append(outbounds,
		map[string]any{"tag": grantDenyTag, "protocol": "blackhole", "settings": map[string]any{}},
	)
	routing["rules"] = append(prefix, rules...)
	config["outbounds"] = outbounds
	after, err := json.Marshal(config)
	if err != nil {
		return RouteChange{}, errors.New("landing: cannot encode client routing")
	}
	return RouteChange{Revision: revision, Before: before, After: after}, nil
}

func scopedGrantRejects(rule map[string]any, grants []ClientGrant) []any {
	result := []any{}
	seen := map[string]bool{}
	for _, grant := range grants {
		if tags, ok := rule["inboundTag"].([]any); ok && len(tags) > 0 && !slices.Contains(tags, any(grant.InboundTag)) {
			continue
		}
		for _, user := range []string{grant.BaseUser, grant.FixedUser} {
			if user == "" || seen[grant.InboundTag+"\x00"+user] {
				continue
			}
			if users, ok := rule["user"].([]any); ok && len(users) > 0 && !slices.Contains(users, any(user)) {
				continue
			}
			clone := make(map[string]any, len(rule)+2)
			for key, value := range rule {
				clone[key] = value
			}
			clone["inboundTag"], clone["user"] = []string{grant.InboundTag}, []string{user}
			result = append(result, clone)
			seen[grant.InboundTag+"\x00"+user] = true
		}
	}
	return result
}

func standardPrivateBlock(rule map[string]any) bool {
	for key := range rule {
		if key != "type" && key != "ip" && key != "outboundTag" && key != "inboundTag" {
			return false
		}
	}
	values, ok := rule["ip"].([]any)
	return rule["type"] == "field" && ok && len(values) == 1 && values[0] == "geoip:private"
}
