package landing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

const proxyOutboundTag = "vastora-landing-egress"

// RouteChange contains credentials and the previous configuration. Persist it
// only in an encrypted journal, before writing settings to the selected node.
// It is not a public API response or a runtime health result.
type RouteChange struct {
	Revision uint64          `json:"revision"`
	Before   json.RawMessage `json:"before"`
	After    json.RawMessage `json:"after"`
	Replaces json.RawMessage `json:"replaces,omitempty"`
}

// Preserve the original direct-route checkpoint through an exit change. The
// exact old managed configuration is the only additional accepted source.
func PrepareRouteReplacement(previous RouteChange, current json.RawMessage, revision uint64, tags []string, peer PeerIdentity) (RouteChange, error) {
	if revision <= previous.Revision {
		return RouteChange{}, errors.New("landing: stale route replacement")
	}
	_, write, err := previous.NextWrite(current, true)
	if err != nil || write {
		return RouteChange{}, errors.New("landing: current route is not the applied landing configuration")
	}
	next, err := PrepareRouteChange(previous.Before, revision, tags, peer)
	if err != nil {
		return RouteChange{}, err
	}
	next.Replaces = slices.Clone(previous.After)
	return next, nil
}

// PrepareRouteChange changes only the routing of explicitly supplied managed
// business inbounds. The caller must verify their ownership against the local
// inbound inventory; a browser-provided tag is not an authorization decision.
// Fallback sockets and unselected inbounds are deliberately not selected here.
func PrepareRouteChange(raw json.RawMessage, revision uint64, tags []string, peer PeerIdentity) (RouteChange, error) {
	if revision == 0 || !tailnetIPv4(peer.Address) || peer.ID == "" || peer.PublicKey == "" || len(tags) == 0 {
		return RouteChange{}, errors.New("landing: invalid route plan")
	}
	selected := map[string]bool{}
	for _, tag := range tags {
		if strings.TrimSpace(tag) != tag || tag == "" || len(tag) > 128 || selected[tag] {
			return RouteChange{}, errors.New("landing: invalid business inbound selection")
		}
		selected[tag] = true
	}
	config, before, err := decodeRouteSettings(raw)
	if err != nil {
		return RouteChange{}, err
	}
	outbounds, ok := config["outbounds"].([]any)
	if !ok || len(outbounds) == 0 {
		return RouteChange{}, errors.New("landing: missing original outbounds")
	}
	blocked := map[string]bool{}
	seen := map[string]bool{}
	for _, item := range outbounds {
		outbound, ok := item.(map[string]any)
		if !ok {
			return RouteChange{}, errors.New("landing: invalid original outbound")
		}
		tag, _ := outbound["tag"].(string)
		protocol, _ := outbound["protocol"].(string)
		if tag == "" || seen[tag] || tag == proxyOutboundTag || protocol == "" {
			return RouteChange{}, errors.New("landing: ambiguous original outbound ownership")
		}
		seen[tag] = true
		blocked[tag] = protocol == "blackhole"
	}
	if api, ok := config["api"].(map[string]any); ok {
		if tag, ok := api["tag"].(string); ok && tag != "" {
			if seen[tag] || tag == proxyOutboundTag || selected[tag] {
				return RouteChange{}, errors.New("landing: ambiguous API route ownership")
			}
			seen[tag] = true
		}
	}
	routing, ok := config["routing"].(map[string]any)
	if !ok {
		return RouteChange{}, errors.New("landing: missing original routing settings")
	}
	// IPOnDemand resolves domains before the selected SOCKS route can match.
	// Do not alter the global strategy (and thereby unrelated traffic) to hide
	// that leak. IPIfNonMatch remains safe because the scoped terminal rule
	// always matches before the resolver's second pass.
	strategy, _ := routing["domainStrategy"].(string)
	if strategy != "" && strategy != "AsIs" && strategy != "IPIfNonMatch" {
		return RouteChange{}, errors.New("landing: original routing requires local DNS resolution")
	}
	rules, ok := routing["rules"].([]any)
	if !ok {
		return RouteChange{}, errors.New("landing: missing original routing rules")
	}
	prefix := []any{}
	for _, item := range rules {
		rule, ok := item.(map[string]any)
		if !ok {
			return RouteChange{}, errors.New("landing: invalid original routing rule")
		}
		// A balancer can select a blackhole or a custom policy dynamically. It
		// cannot safely be treated as an ordinary direct route to bypass.
		if balancer, exists := rule["balancerTag"]; exists && balancer != "" {
			return RouteChange{}, errors.New("landing: original routing uses a balancer")
		}
		outbound, _ := rule["outboundTag"].(string)
		if !seen[outbound] {
			return RouteChange{}, errors.New("landing: original routing references an unknown outbound")
		}
		if !blocked[outbound] {
			continue
		}
		intersection := slices.Clone(tags)
		if value, exists := rule["inboundTag"]; exists {
			values, ok := value.([]any)
			if !ok {
				return RouteChange{}, errors.New("landing: invalid original inbound scope")
			}
			// Xray's empty list is unscoped, not an empty matching set.
			if len(values) > 0 {
				intersection = nil
				for _, value := range values {
					tag, ok := value.(string)
					if !ok {
						return RouteChange{}, errors.New("landing: invalid original inbound scope")
					}
					if selected[tag] && !slices.Contains(intersection, tag) {
						intersection = append(intersection, tag)
					}
				}
			}
		}
		if len(intersection) == 0 {
			continue
		}
		clone := make(map[string]any, len(rule))
		for key, value := range rule {
			clone[key] = value
		}
		clone["inboundTag"] = intersection
		prefix = append(prefix, clone)
	}
	prefix = append(prefix, map[string]any{
		"type": "field", "inboundTag": slices.Clone(tags), "network": "tcp,udp", "outboundTag": proxyOutboundTag,
	})
	routing["rules"] = append(prefix, rules...)
	config["outbounds"] = append(outbounds, map[string]any{
		"tag": proxyOutboundTag, "protocol": "socks",
		"settings": map[string]any{"servers": []any{map[string]any{
			"address": peer.Address, "port": SOCKSPort,
		}}},
	})
	after, err := json.Marshal(config)
	if err != nil {
		return RouteChange{}, errors.New("landing: cannot encode route settings")
	}
	return RouteChange{Revision: revision, Before: before, After: after}, nil
}

// NextWrite permits idempotent retry after a lost response, but never replaces
// settings edited by another operation. Callers still serialize the complete
// read/check/write/readback sequence and explicitly terminate old connections.
func (change RouteChange) NextWrite(current json.RawMessage, enable bool) (json.RawMessage, bool, error) {
	if change.Revision == 0 {
		return nil, false, errors.New("landing: invalid route checkpoint")
	}
	beforeHash, err := RouteSettingsHash(change.Before)
	if err != nil {
		return nil, false, err
	}
	afterHash, err := RouteSettingsHash(change.After)
	if err != nil || beforeHash == afterHash {
		return nil, false, errors.New("landing: invalid route checkpoint")
	}
	currentHash, err := RouteSettingsHash(current)
	if err != nil {
		return nil, false, err
	}
	want, wantHash, otherHash := change.After, afterHash, beforeHash
	if !enable {
		want, wantHash, otherHash = change.Before, beforeHash, afterHash
	}
	if currentHash == wantHash {
		return slices.Clone(want), false, nil
	}
	if currentHash != otherHash {
		if len(change.Replaces) > 0 {
			replacedHash, err := RouteSettingsHash(change.Replaces)
			if err == nil && currentHash == replacedHash {
				return slices.Clone(want), true, nil
			}
		}
		return nil, false, errors.New("landing: local routing changed outside the pending operation")
	}
	return slices.Clone(want), true, nil
}

func RouteSettingsHash(raw json.RawMessage) (string, error) {
	_, canonical, err := decodeRouteSettings(raw)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func decodeRouteSettings(raw json.RawMessage) (map[string]any, json.RawMessage, error) {
	var config map[string]any
	if len(raw) == 0 || len(raw) > 4<<20 || !json.Valid(raw) {
		return nil, nil, errors.New("landing: invalid route settings")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&config) != nil || config == nil {
		return nil, nil, errors.New("landing: invalid route settings")
	}
	// 3x-ui puts its dedicated API rule first when saving Xray settings.
	// Canonicalize only that exact rule, never reorder business rules or
	// discard fields: unrelated configuration changes must still conflict.
	if api, ok := config["api"].(map[string]any); ok {
		tag, _ := api["tag"].(string)
		if routing, ok := config["routing"].(map[string]any); ok && tag != "" {
			if rules, ok := routing["rules"].([]any); ok {
				for i, item := range rules {
					rule, ok := item.(map[string]any)
					if !ok || len(rule) != 3 || rule["type"] != "field" || rule["outboundTag"] != tag {
						continue
					}
					inbounds, ok := rule["inboundTag"].([]any)
					if ok && len(inbounds) == 1 && inbounds[0] == tag {
						copy(rules[1:i+1], rules[:i])
						rules[0] = item
						break
					}
				}
			}
		}
	}
	canonical, err := json.Marshal(config)
	if err != nil {
		return nil, nil, errors.New("landing: cannot encode route settings")
	}
	return config, canonical, nil
}
