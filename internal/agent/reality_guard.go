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
	threeXUIRealityGuardPort = 21000
	realityGuardRemark       = "Vastora REALITY fallback guard"
	realityGuardDirectTag    = "vastora-reality-direct"
	realityGuardBlackholeTag = "vastora-reality-blackhole"
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
	realityTargetVerifier                               = verifyRealityTarget
	realityCompanionEnsurer                             = ensureRealityGuardCompanion
	realityRoutingEnsurer                               = ensureRealityGuardRouting
	realityInboundHardener                              = hardenThreeXUIRealityInbound
	realityNetworkChecker   realityTargetNetworkChecker = cdncheck.New()
)

func verifyRealityTarget(ctx context.Context, targetHost, serverName, nodePublicAddress string) (realityTargetVerification, error) {
	targetHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(targetHost), "."))
	serverName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(serverName), "."))
	if !validRealityTargetHostname(targetHost) || !validRealityTargetHostname(serverName) {
		return realityTargetVerification{}, errors.New("agent: REALITY targetHost and serverName must be valid .com hostnames")
	}
	nodeIP := net.ParseIP(strings.TrimSpace(nodePublicAddress))
	if nodeIP == nil || !nodeIP.IsGlobalUnicast() || nodeIP.IsPrivate() {
		return realityTargetVerification{}, errors.New("agent: target VLESS node public address is unavailable for ASN validation")
	}
	nodeASN, err := lookupTeamCymruASN(ctx, nodeIP)
	if err != nil {
		return realityTargetVerification{}, fmt.Errorf("agent: resolve VLESS node ASN: %w", err)
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
		targetASN, lookupErr := lookupTeamCymruASN(ctx, ip)
		if lookupErr != nil {
			rejections = append(rejections, value+": ASN lookup failed")
			continue
		}
		if provider, policyErr := checkRealityTargetNetworkPolicy(nodeASN, targetASN, ip); policyErr != nil {
			rejections = append(rejections, value+": "+policyErr.Error())
			continue
		} else if provider != "" {
			rejections = append(rejections, value+": shared CDN/WAF provider "+provider+" is not allowed")
			continue
		}
		verification := realityTargetVerification{
			TargetHost: targetHost, TargetIP: value, ServerName: serverName,
			NodeASN: nodeASN, TargetASN: targetASN,
		}
		if tlsErr := verifyPinnedRealityTLS(ctx, &verification); tlsErr != nil {
			rejections = append(rejections, value+": "+tlsErr.Error())
			continue
		}
		return verification, nil
	}
	message := "agent: no REALITY target address passed the security policy"
	if len(rejections) != 0 {
		message += ": " + strings.Join(rejections, "; ")
	}
	return realityTargetVerification{}, errors.New(message)
}

func checkRealityTargetNetworkPolicy(nodeASN, targetASN int64, ip net.IP) (string, error) {
	if nodeASN <= 0 || targetASN <= 0 || nodeASN != targetASN {
		return "", errors.New("target ASN does not match the VLESS node ASN")
	}
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

func realityGuardTag(inboundTag string) string {
	return strings.TrimSpace(inboundTag) + "-guard"
}

func realityGuardPort(realityPort int) (int, error) {
	if realityPort != threeXUIRealityPort {
		return 0, errors.New("agent: REALITY inbound must use the managed port 443")
	}
	return threeXUIRealityGuardPort, nil
}

func ensureRealityGuardCompanion(ctx context.Context, baseURL, token string, nodeID, realityPort int, inboundTag, targetIP string) (threeXUIRealityInbound, error) {
	port, err := realityGuardPort(realityPort)
	if err != nil {
		return threeXUIRealityInbound{}, err
	}
	tag := realityGuardTag(inboundTag)
	inbounds, err := listRealityInbounds(ctx, baseURL, token)
	if err != nil {
		return threeXUIRealityInbound{}, err
	}
	for _, existing := range inbounds {
		if !threeXUIInboundMatchesNode(existing, nodeID) || existing.Port != port {
			continue
		}
		if normalizedThreeXUIInboundTag(existing.Tag, nodeID) == normalizedThreeXUIInboundTag(tag, nodeID) && existing.Protocol == "tunnel" && existing.Listen == "127.0.0.1" && realityGuardTunnelTargets(existing, targetIP) {
			return existing, nil
		}
		if existing.Remark != realityGuardRemark || existing.Protocol != "tunnel" || existing.Listen != "127.0.0.1" {
			return threeXUIRealityInbound{}, errors.New("agent: deterministic REALITY guard companion conflicts with an existing inbound")
		}
		if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/del/"+strconv.Itoa(existing.ID), token, "application/json", map[string]any{}); err != nil {
			return threeXUIRealityInbound{}, fmt.Errorf("agent: replace stale REALITY guard companion: %w", err)
		}
		break
	}
	payload := map[string]any{
		"enable": true, "tag": tag, "remark": realityGuardRemark, "listen": "127.0.0.1", "port": port, "protocol": "tunnel",
		"settings":       map[string]any{"network": "tcp", "address": targetIP, "port": 443},
		"streamSettings": map[string]any{},
		"sniffing":       map[string]any{"enabled": true, "destOverride": []string{"tls"}, "metadataOnly": false, "routeOnly": true},
	}
	if nodeID > 0 {
		payload["nodeId"] = nodeID
	}
	created, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/add", token, "application/json", payload)
	if err != nil {
		return threeXUIRealityInbound{}, fmt.Errorf("agent: create REALITY guard companion: %w", err)
	}
	var inbound threeXUIRealityInbound
	if json.Unmarshal(created, &inbound) != nil || inbound.ID < 1 {
		return threeXUIRealityInbound{}, errors.New("agent: 3x-ui returned an invalid REALITY guard companion")
	}
	if strings.TrimSpace(inbound.Tag) == "" {
		inbound.Tag = tag
	}
	observed, found, err := findRealityInbound(ctx, baseURL, token, tag, nodeID)
	if err != nil || !found || observed.ID != inbound.ID || observed.Protocol != "tunnel" || observed.Listen != "127.0.0.1" || observed.Port != port || !realityGuardTunnelTargets(observed, targetIP) {
		return threeXUIRealityInbound{}, errors.New("agent: REALITY guard companion failed read-back")
	}
	return observed, nil
}

func hardenThreeXUIRealityInbound(ctx context.Context, baseURL, token string, inbound threeXUIRealityInbound, nodeID int, inboundTag string, verification realityTargetVerification) (threeXUIRealityInbound, threeXUIRealityInbound, error) {
	if inbound.ID < 1 || inbound.Protocol != "vless" {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, errors.New("agent: selected REALITY inbound is invalid")
	}
	guardPort, err := realityGuardPort(inbound.Port)
	if err != nil {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, err
	}
	update, err := readThreeXUIInboundUpdate(ctx, baseURL, token, inbound.ID)
	if err != nil {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, err
	}
	update["enable"] = false
	if err := writeThreeXUIInboundUpdate(ctx, baseURL, token, inbound.ID, update); err != nil {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, fmt.Errorf("agent: disable REALITY inbound before hardening: %w", err)
	}
	companion, err := ensureRealityGuardCompanion(ctx, baseURL, token, nodeID, inbound.Port, inboundTag, verification.TargetIP)
	if err != nil {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, err
	}
	if err := ensureRealityGuardRouting(ctx, baseURL, token, companion.Tag, verification.ServerName); err != nil {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, err
	}
	streamSettings, ok := update["streamSettings"].(map[string]any)
	if !ok || streamSettings["security"] != "reality" {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, errors.New("agent: selected inbound is not VLESS REALITY")
	}
	realitySettings, ok := streamSettings["realitySettings"].(map[string]any)
	if !ok {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, errors.New("agent: selected REALITY inbound settings are incomplete")
	}
	realitySettings["target"] = net.JoinHostPort("127.0.0.1", strconv.Itoa(guardPort))
	realitySettings["serverNames"] = []any{verification.ServerName}
	update["enable"] = false
	if err := writeThreeXUIInboundUpdate(ctx, baseURL, token, inbound.ID, update); err != nil {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, fmt.Errorf("agent: point REALITY fallback at guard companion: %w", err)
	}
	observed, err := getThreeXUIInbound(ctx, baseURL, token, inbound.ID)
	if err != nil || !realityInboundUsesGuard(observed, guardPort, verification.ServerName) || !realityInboundAcceptsProxyProtocol(observed) || observed.Enable {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, errors.New("agent: disabled REALITY guard configuration failed read-back")
	}
	update["enable"] = true
	if err := writeThreeXUIInboundUpdate(ctx, baseURL, token, inbound.ID, update); err != nil {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, fmt.Errorf("agent: enable hardened REALITY inbound: %w", err)
	}
	observed, err = getThreeXUIInbound(ctx, baseURL, token, inbound.ID)
	if err != nil || !observed.Enable || !realityInboundUsesGuard(observed, guardPort, verification.ServerName) || !realityInboundAcceptsProxyProtocol(observed) {
		return threeXUIRealityInbound{}, threeXUIRealityInbound{}, errors.New("agent: hardened REALITY inbound failed final read-back")
	}
	return observed, companion, nil
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

func realityInboundUsesGuard(inbound threeXUIRealityInbound, guardPort int, serverName string) bool {
	var stream struct {
		Security string `json:"security"`
		Reality  struct {
			Target      string   `json:"target"`
			ServerNames []string `json:"serverNames"`
		} `json:"realitySettings"`
	}
	return json.Unmarshal(inbound.StreamSettings, &stream) == nil && stream.Security == "reality" &&
		stream.Reality.Target == net.JoinHostPort("127.0.0.1", strconv.Itoa(guardPort)) && len(stream.Reality.ServerNames) == 1 && stream.Reality.ServerNames[0] == serverName
}

func guardedRealityResult(result RealityCommandResult, verification realityTargetVerification, companion threeXUIRealityInbound) RealityCommandResult {
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
	result.CompanionInboundID = companion.ID
	result.CompanionTag = companion.Tag
	result.CompanionPort = companion.Port
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

func realityGuardTunnelTargets(inbound threeXUIRealityInbound, targetIP string) bool {
	var settings struct {
		Network string `json:"network"`
		Address string `json:"address"`
		Port    int    `json:"port"`
	}
	return json.Unmarshal(inbound.Settings, &settings) == nil && settings.Network == "tcp" && settings.Address == targetIP && settings.Port == 443
}

func ensureRealityGuardRouting(ctx context.Context, baseURL, token, guardTag, serverName string) error {
	payload, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/xray/", token, "application/json", map[string]any{})
	if err != nil {
		return fmt.Errorf("agent: read 3x-ui Xray settings: %w", err)
	}
	settings, err := decodeThreeXUIXraySettings(payload)
	if err != nil {
		return err
	}
	original, err := json.Marshal(settings.XraySetting)
	if err != nil {
		return err
	}
	applyRealityGuardRouting(settings.XraySetting, guardTag, serverName)
	encoded, err := json.Marshal(settings.XraySetting)
	if err != nil {
		return err
	}
	form := url.Values{"xraySetting": {string(encoded)}, "outboundTestUrl": {settings.OutboundTestURL}}
	if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/xray/update", token, "application/x-www-form-urlencoded", form); err != nil {
		rollbackErr := restoreThreeXUIXraySettings(ctx, baseURL, token, original, settings.OutboundTestURL)
		return errors.Join(fmt.Errorf("agent: apply and test REALITY guard Xray routes: %w", err), rollbackErr)
	}
	if err := verifyRealityGuardRoutingReadBack(ctx, baseURL, token, guardTag, serverName); err != nil {
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

func applyRealityGuardRouting(config map[string]any, guardTag, serverName string) {
	outbounds, _ := config["outbounds"].([]any)
	managedOutbounds := []any{
		map[string]any{"tag": realityGuardDirectTag, "protocol": "freedom", "settings": map[string]any{}},
		map[string]any{"tag": realityGuardBlackholeTag, "protocol": "blackhole", "settings": map[string]any{}},
	}
	for _, raw := range outbounds {
		outbound, _ := raw.(map[string]any)
		tag, _ := outbound["tag"].(string)
		if tag != realityGuardDirectTag && tag != realityGuardBlackholeTag {
			managedOutbounds = append(managedOutbounds, raw)
		}
	}
	config["outbounds"] = managedOutbounds
	routing, _ := config["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{"domainStrategy": "AsIs"}
		config["routing"] = routing
	}
	rules, _ := routing["rules"].([]any)
	preserved := make([]any, 0, len(rules)+2)
	preserved = append(preserved,
		map[string]any{"type": "field", "inboundTag": []any{guardTag}, "domain": []any{"full:" + serverName}, "outboundTag": realityGuardDirectTag},
		map[string]any{"type": "field", "inboundTag": []any{guardTag}, "outboundTag": realityGuardBlackholeTag},
	)
	for _, raw := range rules {
		if !isManagedRealityGuardRule(raw, guardTag) {
			preserved = append(preserved, raw)
		}
	}
	routing["rules"] = preserved
}

func isManagedRealityGuardRule(raw any, guardTag string) bool {
	rule, _ := raw.(map[string]any)
	tag, _ := rule["outboundTag"].(string)
	if routingRuleUsesInbound(raw, guardTag) {
		return true
	}
	return (tag == realityGuardDirectTag || tag == realityGuardBlackholeTag) && !routingRuleHasInboundRestriction(raw)
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

func verifyRealityGuardRoutingReadBack(ctx context.Context, baseURL, token, guardTag, serverName string) error {
	payload, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/xray/", token, "application/json", map[string]any{})
	if err != nil {
		return err
	}
	settings, err := decodeThreeXUIXraySettings(payload)
	if err != nil {
		return errors.New("agent: REALITY guard Xray read-back is invalid")
	}
	routing, _ := settings.XraySetting["routing"].(map[string]any)
	rules, _ := routing["rules"].([]any)
	if !realityGuardRulesSurvived(rules, guardTag, serverName) {
		return errors.New("agent: REALITY guard routes did not survive Xray config test/read-back")
	}
	return nil
}

func realityGuardRulesSurvived(rules []any, guardTag, serverName string) bool {
	directIndex, blackholeIndex := -1, -1
	for index, rule := range rules {
		switch {
		case routingRuleMatches(rule, guardTag, "full:"+serverName, realityGuardDirectTag):
			directIndex = index
		case routingRuleMatches(rule, guardTag, "", realityGuardBlackholeTag):
			blackholeIndex = index
		case routingRuleUsesInbound(rule, guardTag):
			return false
		case directIndex < 0 && !routingRuleHasInboundRestriction(rule):
			return false
		}
	}
	return directIndex >= 0 && blackholeIndex == directIndex+1
}

func routingRuleHasInboundRestriction(raw any) bool {
	rule, _ := raw.(map[string]any)
	switch values := rule["inboundTag"].(type) {
	case []any:
		return len(values) > 0
	case []string:
		return len(values) > 0
	default:
		return false
	}
}

func routingRuleMatches(raw any, inboundTag, domain, outboundTag string) bool {
	rule, _ := raw.(map[string]any)
	if rule["outboundTag"] != outboundTag || !routingRuleUsesInbound(raw, inboundTag) {
		return false
	}
	domains, exists := rule["domain"].([]any)
	if domain == "" {
		return !exists || len(domains) == 0
	}
	return exists && len(domains) == 1 && domains[0] == domain
}
