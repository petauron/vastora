package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/projectdiscovery/cdncheck"
)

const (
	legacyRealityGuardPort         = 21000
	legacyRealityGuardRemark       = "Vastora REALITY fallback guard"
	legacyRealityGuardDirectTag    = "vastora-reality-direct"
	legacyRealityGuardBlackholeTag = "vastora-reality-blackhole"
)

type realityTargetVerification struct {
	TargetHost       string
	TargetIP         string
	ServerName       string
	NodeASN          int64
	TargetASN        int64
	CDNProvider      string
	TLS13            bool
	X25519           bool
	HTTP2            bool
	CertificateValid bool
}

type threeXUIXraySettings struct {
	XraySetting     map[string]any `json:"xraySetting"`
	OutboundTestURL string         `json:"outboundTestUrl"`
}

type realityTargetNetworkChecker interface {
	CheckCDN(net.IP) (bool, string, error)
	CheckWAF(net.IP) (bool, string, error)
}

var (
	realityTargetVerifier                              = verifyRealityTarget
	realityInboundHardener                             = hardenThreeXUIRealityInbound
	realityNetworkChecker  realityTargetNetworkChecker = cdncheck.New()
	realityASNLookup                                   = lookupTeamCymruASN
)

func verifyRealityTarget(ctx context.Context, targetHost, serverName, nodePublicAddress string) (realityTargetVerification, error) {
	targetHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(targetHost), "."))
	serverName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(serverName), "."))
	if !validRealityTargetHostname(targetHost) || !validRealityTargetHostname(serverName) {
		return realityTargetVerification{}, errors.New("agent: REALITY targetHost and serverName must be valid .com hostnames")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, targetHost)
	if err != nil || len(addresses) == 0 {
		return realityTargetVerification{}, errors.New("agent: resolve REALITY target: no address was returned")
	}
	unique := map[string]net.IP{}
	for _, address := range addresses {
		ip := address.IP
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			continue
		}
		unique[ip.String()] = ip
	}
	ordered := make([]string, 0, len(unique))
	for value := range unique {
		ordered = append(ordered, value)
	}
	sort.Strings(ordered)
	if len(ordered) == 0 {
		return realityTargetVerification{}, errors.New("agent: REALITY target resolved only to non-public addresses")
	}
	rejections := make([]string, 0, len(ordered))
	for _, value := range ordered {
		ip := unique[value]
		if provider, policyErr := checkRealityTargetNetworkPolicy(ip); policyErr != nil {
			rejections = append(rejections, value+": "+policyErr.Error())
			continue
		} else if provider != "" {
			rejections = append(rejections, value+": shared CDN/WAF provider "+provider+" is not allowed")
			continue
		}
		verification := realityTargetVerification{
			TargetHost: targetHost, TargetIP: value, ServerName: serverName,
		}
		if tlsErr := verifyPinnedRealityTLS(ctx, &verification); tlsErr != nil {
			rejections = append(rejections, value+": "+tlsErr.Error())
			continue
		}
		// ASN describes the network, not the fallback authorization boundary.
		// Collect it only for a target that already passed the security checks.
		verification.NodeASN = realityASNHint(ctx, net.ParseIP(strings.TrimSpace(nodePublicAddress)))
		verification.TargetASN = realityASNHint(ctx, ip)
		if err := ctx.Err(); err != nil {
			return realityTargetVerification{}, err
		}
		return verification, nil
	}
	message := "agent: no REALITY target address passed the security policy"
	if len(rejections) != 0 {
		message += ": " + strings.Join(rejections, "; ")
	}
	return realityTargetVerification{}, errors.New(message)
}

func checkRealityTargetNetworkPolicy(ip net.IP) (string, error) {
	if realityNetworkChecker == nil {
		return "", errors.New("CDN/WAF validation is unavailable")
	}
	matched, provider, err := realityNetworkChecker.CheckCDN(ip)
	if err != nil {
		return "", fmt.Errorf("CDN validation failed: %w", err)
	}
	if matched {
		if provider == "" {
			provider = "unknown"
		}
		return provider, nil
	}
	matched, provider, err = realityNetworkChecker.CheckWAF(ip)
	if err != nil {
		return "", fmt.Errorf("WAF validation failed: %w", err)
	}
	if matched {
		if provider == "" {
			provider = "unknown"
		}
		return provider, nil
	}
	return "", nil
}

func realityASNHint(ctx context.Context, ip net.IP) int64 {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return 0
	}
	lookupCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	asn, err := realityASNLookup(lookupCtx, ip)
	if err != nil || asn <= 0 {
		return 0
	}
	return asn
}

func validRealityTargetHostname(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	return validThreeXUIShareHostname(hostname) && strings.HasSuffix(hostname, ".com")
}

func lookupTeamCymruASN(ctx context.Context, ip net.IP) (int64, error) {
	name := ""
	if v4 := ip.To4(); v4 != nil {
		name = fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", v4[3], v4[2], v4[1], v4[0])
	} else if v6 := ip.To16(); v6 != nil {
		hex := fmt.Sprintf("%032x", v6)
		parts := make([]string, 0, len(hex))
		for index := len(hex) - 1; index >= 0; index-- {
			parts = append(parts, string(hex[index]))
		}
		name = strings.Join(parts, ".") + ".origin6.asn.cymru.com"
	} else {
		return 0, errors.New("invalid IP address")
	}
	records, err := net.DefaultResolver.LookupTXT(ctx, name)
	if err != nil || len(records) == 0 {
		return 0, errors.New("no ASN record returned by Team Cymru")
	}
	return parseTeamCymruASN(records[0])
}

func parseTeamCymruASN(record string) (int64, error) {
	field := strings.TrimSpace(strings.Split(record, "|")[0])
	field = strings.TrimPrefix(strings.ToUpper(field), "AS")
	asn, err := strconv.ParseInt(field, 10, 64)
	if err != nil || asn <= 0 {
		return 0, errors.New("invalid ASN record returned by Team Cymru")
	}
	return asn, nil
}

func verifyPinnedRealityTLS(ctx context.Context, result *realityTargetVerification) error {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 12 * time.Second},
		Config: &tls.Config{
			MinVersion:       tls.VersionTLS13,
			MaxVersion:       tls.VersionTLS13,
			ServerName:       result.ServerName,
			NextProtos:       []string{"h2"},
			CurvePreferences: []tls.CurveID{tls.X25519},
		},
	}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(result.TargetIP, "443"))
	if err != nil {
		return errors.New("TLS 1.3/X25519/SNI certificate validation failed")
	}
	defer connection.Close()
	tlsConnection, ok := connection.(*tls.Conn)
	if !ok {
		return errors.New("TLS validation did not return a TLS connection")
	}
	state := tlsConnection.ConnectionState()
	if state.Version != tls.VersionTLS13 || state.NegotiatedProtocol != "h2" || len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || state.PeerCertificates[0].VerifyHostname(result.ServerName) != nil {
		return errors.New("target must negotiate TLS 1.3, X25519, h2, and a valid SNI certificate")
	}
	result.TLS13 = true
	result.X25519 = true
	result.HTTP2 = true
	result.CertificateValid = true
	return nil
}

func legacyRealityGuardTag(inboundTag string) string {
	return strings.TrimSpace(inboundTag) + "-guard"
}

func hardenThreeXUIRealityInbound(ctx context.Context, baseURL, token string, inbound threeXUIRealityInbound, nodeID int, inboundTag string, verification realityTargetVerification) (threeXUIRealityInbound, error) {
	if inbound.ID < 1 || inbound.Protocol != "vless" {
		return threeXUIRealityInbound{}, errors.New("agent: selected REALITY inbound is invalid")
	}
	if inbound.Port != threeXUIRealityPort {
		return threeXUIRealityInbound{}, errors.New("agent: REALITY inbound must use the managed port 443")
	}
	update, err := readThreeXUIInboundUpdate(ctx, baseURL, token, inbound.ID)
	if err != nil {
		return threeXUIRealityInbound{}, err
	}
	update["enable"] = false
	if err := writeThreeXUIInboundUpdate(ctx, baseURL, token, inbound.ID, update); err != nil {
		return threeXUIRealityInbound{}, fmt.Errorf("agent: disable REALITY inbound before hardening: %w", err)
	}
	streamSettings, ok := update["streamSettings"].(map[string]any)
	if !ok || streamSettings["security"] != "reality" {
		return threeXUIRealityInbound{}, errors.New("agent: selected inbound is not VLESS REALITY")
	}
	realitySettings, ok := streamSettings["realitySettings"].(map[string]any)
	if !ok {
		return threeXUIRealityInbound{}, errors.New("agent: selected REALITY inbound settings are incomplete")
	}
	realitySettings["target"] = net.JoinHostPort(verification.TargetIP, "443")
	realitySettings["serverNames"] = []any{verification.ServerName}
	update["enable"] = false
	if err := writeThreeXUIInboundUpdate(ctx, baseURL, token, inbound.ID, update); err != nil {
		return threeXUIRealityInbound{}, fmt.Errorf("agent: pin REALITY fallback to the verified target: %w", err)
	}
	observed, err := getThreeXUIInbound(ctx, baseURL, token, inbound.ID)
	if err != nil || !realityInboundUsesVerifiedTarget(observed, verification.TargetIP, verification.ServerName) || !realityInboundAcceptsProxyProtocol(observed) || observed.Enable {
		return threeXUIRealityInbound{}, errors.New("agent: disabled REALITY guard configuration failed read-back")
	}
	update["enable"] = true
	if err := writeThreeXUIInboundUpdate(ctx, baseURL, token, inbound.ID, update); err != nil {
		return threeXUIRealityInbound{}, fmt.Errorf("agent: enable hardened REALITY inbound: %w", err)
	}
	observed, err = getThreeXUIInbound(ctx, baseURL, token, inbound.ID)
	if err != nil || !observed.Enable || !realityInboundUsesVerifiedTarget(observed, verification.TargetIP, verification.ServerName) || !realityInboundAcceptsProxyProtocol(observed) {
		return threeXUIRealityInbound{}, errors.New("agent: hardened REALITY inbound failed final read-back")
	}
	if err := removeLegacyRealityGuard(ctx, baseURL, token, nodeID, inboundTag); err != nil {
		return threeXUIRealityInbound{}, err
	}
	return observed, nil
}

func readThreeXUIInboundUpdate(ctx context.Context, baseURL, token string, inboundID int) (map[string]any, error) {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/get/"+strconv.Itoa(inboundID), token, "", nil)
	if err != nil {
		return nil, err
	}
	var update map[string]any
	if json.Unmarshal(payload, &update) != nil {
		return nil, errors.New("agent: 3x-ui returned invalid inbound data")
	}
	delete(update, "id")
	delete(update, "clientStats")
	delete(update, "fallbackParent")
	return update, nil
}

func writeThreeXUIInboundUpdate(ctx context.Context, baseURL, token string, inboundID int, update map[string]any) error {
	_, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/update/"+strconv.Itoa(inboundID), token, "application/json", update)
	return err
}

func getThreeXUIInbound(ctx context.Context, baseURL, token string, inboundID int) (threeXUIRealityInbound, error) {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/get/"+strconv.Itoa(inboundID), token, "", nil)
	if err != nil {
		return threeXUIRealityInbound{}, err
	}
	var inbound threeXUIRealityInbound
	if json.Unmarshal(payload, &inbound) != nil || inbound.ID != inboundID {
		return threeXUIRealityInbound{}, errors.New("agent: 3x-ui returned invalid inbound data")
	}
	return inbound, nil
}

func realityInboundUsesVerifiedTarget(inbound threeXUIRealityInbound, targetIP, serverName string) bool {
	var stream struct {
		Security string `json:"security"`
		Reality  struct {
			Target      string   `json:"target"`
			ServerNames []string `json:"serverNames"`
		} `json:"realitySettings"`
	}
	return json.Unmarshal(inbound.StreamSettings, &stream) == nil && stream.Security == "reality" &&
		stream.Reality.Target == net.JoinHostPort(targetIP, "443") && len(stream.Reality.ServerNames) == 1 && stream.Reality.ServerNames[0] == serverName
}

func guardedRealityResult(result RealityCommandResult, verification realityTargetVerification) RealityCommandResult {
	result.TargetHost = verification.TargetHost
	result.TargetIP = verification.TargetIP
	result.ServerName = verification.ServerName
	result.NodeASN = verification.NodeASN
	result.TargetASN = verification.TargetASN
	result.CDNProvider = verification.CDNProvider
	result.TLS13 = verification.TLS13
	result.X25519 = verification.X25519
	result.HTTP2 = verification.HTTP2
	result.CertificateValid = verification.CertificateValid
	result.GuardStatus = "ready"
	result.ProxyProtocol = true
	return result
}

func realityVerificationResult(verification realityTargetVerification) RealityCommandResult {
	return RealityCommandResult{
		Action: "verify", TargetHost: verification.TargetHost, TargetIP: verification.TargetIP, ServerName: verification.ServerName,
		NodeASN: verification.NodeASN, TargetASN: verification.TargetASN, CDNProvider: verification.CDNProvider,
		TLS13: verification.TLS13, X25519: verification.X25519, HTTP2: verification.HTTP2, CertificateValid: verification.CertificateValid,
		GuardStatus: "pending",
	}
}

func removeLegacyRealityGuard(ctx context.Context, baseURL, token string, nodeID int, inboundTag string) error {
	inbounds, err := listRealityInbounds(ctx, baseURL, token)
	if err != nil {
		return fmt.Errorf("agent: inspect obsolete REALITY guard: %w", err)
	}
	expectedTag := normalizedThreeXUIInboundTag(legacyRealityGuardTag(inboundTag), nodeID)
	legacy := make([]threeXUIRealityInbound, 0, 1)
	for _, candidate := range inbounds {
		if isLegacyRealityGuardInbound(candidate) && threeXUIInboundMatchesNode(candidate, nodeID) && normalizedThreeXUIInboundTag(candidate.Tag, nodeID) == expectedTag {
			legacy = append(legacy, candidate)
		}
	}
	if len(legacy) == 0 {
		return nil
	}
	legacyTags := make([]string, 0, len(legacy))
	for _, candidate := range legacy {
		legacyTags = append(legacyTags, candidate.Tag)
	}
	if err := removeLegacyRealityGuardRouting(ctx, baseURL, token, legacyTags, false); err != nil {
		return err
	}
	for _, candidate := range legacy {
		if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/del/"+strconv.Itoa(candidate.ID), token, "application/json", map[string]any{}); err != nil {
			return fmt.Errorf("agent: remove obsolete REALITY guard inbound: %w", err)
		}
	}
	inbounds, err = listRealityInbounds(ctx, baseURL, token)
	if err != nil {
		return fmt.Errorf("agent: verify obsolete REALITY guard cleanup: %w", err)
	}
	remainingLegacy := false
	for _, candidate := range inbounds {
		if isLegacyRealityGuardInbound(candidate) {
			remainingLegacy = true
		}
		if isLegacyRealityGuardInbound(candidate) && threeXUIInboundMatchesNode(candidate, nodeID) && normalizedThreeXUIInboundTag(candidate.Tag, nodeID) == expectedTag {
			return errors.New("agent: obsolete REALITY guard inbound survived cleanup")
		}
	}
	if !remainingLegacy {
		if err := removeLegacyRealityGuardRouting(ctx, baseURL, token, legacyTags, true); err != nil {
			return err
		}
	}
	return nil
}

func isLegacyRealityGuardInbound(inbound threeXUIRealityInbound) bool {
	return inbound.Remark == legacyRealityGuardRemark && inbound.Protocol == "tunnel" && inbound.Listen == "127.0.0.1" && inbound.Port == legacyRealityGuardPort && strings.HasSuffix(strings.TrimSpace(inbound.Tag), "-guard")
}

func removeLegacyRealityGuardRouting(ctx context.Context, baseURL, token string, legacyTags []string, removeOutbounds bool) error {
	payload, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/xray/", token, "application/json", map[string]any{})
	if err != nil {
		return fmt.Errorf("agent: read 3x-ui Xray settings for obsolete guard cleanup: %w", err)
	}
	settings, err := decodeThreeXUIXraySettings(payload)
	if err != nil {
		return err
	}
	original, err := json.Marshal(settings.XraySetting)
	if err != nil {
		return err
	}
	routing, _ := settings.XraySetting["routing"].(map[string]any)
	if routing != nil {
		rules, _ := routing["rules"].([]any)
		preserved := make([]any, 0, len(rules))
		for _, raw := range rules {
			rule, _ := raw.(map[string]any)
			outboundTag, _ := rule["outboundTag"].(string)
			if routingRuleUsesAnyInbound(raw, legacyTags) || (removeOutbounds && (outboundTag == legacyRealityGuardDirectTag || outboundTag == legacyRealityGuardBlackholeTag)) {
				continue
			}
			preserved = append(preserved, raw)
		}
		routing["rules"] = preserved
	}
	if removeOutbounds {
		outbounds, _ := settings.XraySetting["outbounds"].([]any)
		preserved := make([]any, 0, len(outbounds))
		for _, raw := range outbounds {
			outbound, _ := raw.(map[string]any)
			tag, _ := outbound["tag"].(string)
			if tag != legacyRealityGuardDirectTag && tag != legacyRealityGuardBlackholeTag {
				preserved = append(preserved, raw)
			}
		}
		settings.XraySetting["outbounds"] = preserved
	}
	encoded, err := json.Marshal(settings.XraySetting)
	if err != nil {
		return err
	}
	if string(encoded) == string(original) {
		return nil
	}
	form := url.Values{"xraySetting": {string(encoded)}, "outboundTestUrl": {settings.OutboundTestURL}}
	if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/xray/update", token, "application/x-www-form-urlencoded", form); err != nil {
		if verifyErr := verifyLegacyRealityGuardRoutingRemoved(ctx, baseURL, token, legacyTags, removeOutbounds); verifyErr == nil {
			return nil
		}
		return fmt.Errorf("agent: remove obsolete REALITY guard Xray routes: %w", err)
	}
	if err := verifyLegacyRealityGuardRoutingRemoved(ctx, baseURL, token, legacyTags, removeOutbounds); err != nil {
		return errors.Join(err, restoreThreeXUIXraySettings(ctx, baseURL, token, original, settings.OutboundTestURL))
	}
	return nil
}

func decodeThreeXUIXraySettings(payload json.RawMessage) (threeXUIXraySettings, error) {
	var encodedSettings string
	if json.Unmarshal(payload, &encodedSettings) != nil || strings.TrimSpace(encodedSettings) == "" {
		return threeXUIXraySettings{}, errors.New("agent: 3x-ui returned invalid Xray settings")
	}
	var settings threeXUIXraySettings
	if json.Unmarshal([]byte(encodedSettings), &settings) != nil || settings.XraySetting == nil {
		return threeXUIXraySettings{}, errors.New("agent: 3x-ui returned invalid Xray settings")
	}
	return settings, nil
}

func restoreThreeXUIXraySettings(ctx context.Context, baseURL, token string, original []byte, outboundTestURL string) error {
	form := url.Values{"xraySetting": {string(original)}, "outboundTestUrl": {outboundTestURL}}
	if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/xray/update", token, "application/x-www-form-urlencoded", form); err != nil {
		return fmt.Errorf("agent: restore prior 3x-ui Xray settings: %w", err)
	}
	return nil
}

func routingRuleUsesAnyInbound(raw any, expected []string) bool {
	for _, tag := range expected {
		if routingRuleUsesInbound(raw, tag) {
			return true
		}
	}
	return false
}

func routingRuleUsesInbound(raw any, expected string) bool {
	rule, _ := raw.(map[string]any)
	switch values := rule["inboundTag"].(type) {
	case []any:
		for _, value := range values {
			if tag, _ := value.(string); tag == expected {
				return true
			}
		}
	case []string:
		for _, tag := range values {
			if tag == expected {
				return true
			}
		}
	}
	return false
}

func verifyLegacyRealityGuardRoutingRemoved(ctx context.Context, baseURL, token string, legacyTags []string, removeOutbounds bool) error {
	payload, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/xray/", token, "application/json", map[string]any{})
	if err != nil {
		return err
	}
	settings, err := decodeThreeXUIXraySettings(payload)
	if err != nil {
		return errors.New("agent: obsolete REALITY guard Xray read-back is invalid")
	}
	routing, _ := settings.XraySetting["routing"].(map[string]any)
	rules, _ := routing["rules"].([]any)
	for _, rule := range rules {
		value, _ := rule.(map[string]any)
		outboundTag, _ := value["outboundTag"].(string)
		if routingRuleUsesAnyInbound(rule, legacyTags) || (removeOutbounds && (outboundTag == legacyRealityGuardDirectTag || outboundTag == legacyRealityGuardBlackholeTag)) {
			return errors.New("agent: obsolete REALITY guard routes survived cleanup")
		}
	}
	if removeOutbounds {
		outbounds, _ := settings.XraySetting["outbounds"].([]any)
		for _, raw := range outbounds {
			outbound, _ := raw.(map[string]any)
			tag, _ := outbound["tag"].(string)
			if tag == legacyRealityGuardDirectTag || tag == legacyRealityGuardBlackholeTag {
				return errors.New("agent: obsolete REALITY guard outbounds survived cleanup")
			}
		}
	}
	return nil
}
