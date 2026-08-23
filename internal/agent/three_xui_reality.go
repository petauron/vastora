package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type threeXUIRealityInbound struct {
	ID             int             `json:"id"`
	Remark         string          `json:"remark"`
	Protocol       string          `json:"protocol"`
	Listen         string          `json:"listen"`
	Port           int             `json:"port"`
	Settings       json.RawMessage `json:"settings"`
	StreamSettings json.RawMessage `json:"streamSettings"`
	NodeID         *int            `json:"nodeId,omitempty"`
}

type threeXUIHostGroup struct {
	GroupID           string   `json:"groupId"`
	InboundIDs        []int    `json:"inboundIds"`
	Hosts             []string `json:"hosts"`
	Remark            string   `json:"remark"`
	ServerDescription string   `json:"serverDescription"`
	IsDisabled        bool     `json:"isDisabled"`
	IsHidden          bool     `json:"isHidden"`
	Tags              []string `json:"tags"`
	Port              int      `json:"port"`
	Security          string   `json:"security"`
	SNI               string   `json:"sni"`
	Fingerprint       string   `json:"fingerprint"`
	MihomoIPVersion   string   `json:"mihomoIpVersion"`
}

type realityScanResult struct {
	Target      string   `json:"target"`
	Host        string   `json:"host"`
	Feasible    bool     `json:"feasible"`
	ServerNames []string `json:"serverNames"`
}

const threeXUIMihomoMinClientVersion = "1.8.2"

func applyRealityCommand(ctx context.Context, store *Store, commandID string, command RealityCommandTask) (RealityCommandResult, error) {
	baseURL, masterToken, err := threeXUIClientAPIConnection(ctx, store)
	if err != nil {
		return RealityCommandResult{}, err
	}
	if command.Action == "rename" {
		return renameThreeXUIRealityInbound(ctx, baseURL, masterToken, command)
	}
	if command.Action != "create" || !validRealityDisplayName(command.DisplayName) || !validRealityDisplayName(command.ClientName) {
		return RealityCommandResult{}, errors.New("agent: REALITY creation parameters are invalid")
	}
	listen := strings.TrimSpace(command.TargetAddress)
	if net.ParseIP(listen) == nil {
		return RealityCommandResult{}, errors.New("agent: target VLESS node private service address is invalid")
	}
	scanURL, scanToken := baseURL, masterToken
	if command.TargetNodeID > 0 {
		if command.TargetPanelPort < 1024 || command.TargetPanelPort > 65535 || strings.TrimSpace(command.TargetAPIToken) == "" {
			return RealityCommandResult{}, errors.New("agent: target VLESS node API connection is unavailable")
		}
		scanURL = "http://" + net.JoinHostPort(listen, strconv.Itoa(command.TargetPanelPort))
		scanToken = strings.TrimSpace(command.TargetAPIToken)
	}
	clientEmail := threeXUIClientEmail(command.ClientName, commandID)
	if existing, ok, err := findRealityInbound(ctx, baseURL, masterToken, clientEmail, command.TargetNodeID); err != nil {
		return RealityCommandResult{}, err
	} else if ok {
		existing, err = updateThreeXUIRealityInboundName(ctx, baseURL, masterToken, existing.ID, command.TargetNodeID, command.DisplayName)
		if err != nil {
			return RealityCommandResult{}, err
		}
		existing, err = ensureThreeXUIRealityClientVersion(ctx, baseURL, masterToken, existing.ID)
		if err != nil {
			return RealityCommandResult{}, err
		}
		result, err := realityResultFromInbound(existing, command.ConnectHostname, command.DisplayName, command.ClientName, clientEmail)
		if err != nil {
			return RealityCommandResult{}, err
		}
		if err := syncThreeXUIRealityHost(ctx, baseURL, masterToken, result.InboundID, result.ConnectHostname, result.SNIHostname); err != nil {
			return RealityCommandResult{}, err
		}
		if err := attachAllThreeXUIClientsToInbound(ctx, baseURL, masterToken, result.InboundID); err != nil {
			return RealityCommandResult{}, err
		}
		return result, nil
	}

	target, sni, err := selectRealityTarget(ctx, scanURL, scanToken, command)
	if err != nil {
		return RealityCommandResult{}, err
	}
	keys, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/server/getNewX25519Cert", masterToken, "", nil)
	if err != nil {
		return RealityCommandResult{}, fmt.Errorf("agent: generate REALITY keys: %w", err)
	}
	var keyPair struct {
		PrivateKey string `json:"privateKey"`
		PublicKey  string `json:"publicKey"`
	}
	if json.Unmarshal(keys, &keyPair) != nil || keyPair.PrivateKey == "" || keyPair.PublicKey == "" {
		return RealityCommandResult{}, errors.New("agent: 3x-ui returned invalid REALITY keys")
	}
	clientID, err := randomUUID()
	if err != nil {
		return RealityCommandResult{}, err
	}
	shortBytes := make([]byte, 8)
	if _, err := rand.Read(shortBytes); err != nil {
		return RealityCommandResult{}, fmt.Errorf("agent: generate REALITY short id: %w", err)
	}
	shortID := hex.EncodeToString(shortBytes)
	port, err := availableRealityPort(ctx, baseURL, masterToken, command.TargetNodeID, listen)
	if err != nil {
		return RealityCommandResult{}, err
	}
	payload := map[string]any{
		"enable": true, "remark": command.DisplayName, "listen": listen, "port": port, "protocol": "vless", "expiryTime": 0, "total": 0,
		"settings":       map[string]any{"clients": []map[string]any{{"id": clientID, "email": clientEmail, "flow": "xtls-rprx-vision", "limitIp": 0, "totalGB": 0, "expiryTime": 0, "enable": true}}, "decryption": "none", "encryption": "none", "fallbacks": []any{}},
		"streamSettings": threeXUIRealityStreamSettings(target, sni, keyPair.PrivateKey, keyPair.PublicKey, shortID),
		"sniffing":       map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "metadataOnly": false, "routeOnly": false},
	}
	if command.TargetNodeID > 0 {
		payload["nodeId"] = command.TargetNodeID
	}
	added, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/add", masterToken, "application/json", payload)
	if err != nil {
		return RealityCommandResult{}, fmt.Errorf("agent: create 3x-ui REALITY inbound: %w", err)
	}
	var inbound threeXUIRealityInbound
	if json.Unmarshal(added, &inbound) != nil || inbound.ID < 1 {
		return RealityCommandResult{}, errors.New("agent: 3x-ui returned an invalid REALITY inbound")
	}
	result := RealityCommandResult{Action: "create", InboundID: inbound.ID, DisplayName: command.DisplayName, ClientName: command.ClientName, Listen: listen, Port: port, Target: target, SNIHostname: sni, ConnectHostname: command.ConnectHostname, ShareURI: realityShareURI(clientID, command.ConnectHostname, command.DisplayName, sni, keyPair.PublicKey, shortID)}
	if err := syncThreeXUIRealityHost(ctx, baseURL, masterToken, result.InboundID, result.ConnectHostname, result.SNIHostname); err != nil {
		return RealityCommandResult{}, err
	}
	if err := attachAllThreeXUIClientsToInbound(ctx, baseURL, masterToken, result.InboundID); err != nil {
		return RealityCommandResult{}, err
	}
	return result, nil
}

func renameThreeXUIRealityInbound(ctx context.Context, baseURL, token string, command RealityCommandTask) (RealityCommandResult, error) {
	if command.InboundID < 1 || !validRealityDisplayName(command.DisplayName) {
		return RealityCommandResult{}, errors.New("agent: REALITY rename parameters are invalid")
	}
	inbound, err := updateThreeXUIRealityInboundName(ctx, baseURL, token, command.InboundID, command.TargetNodeID, command.DisplayName)
	if err != nil {
		return RealityCommandResult{}, err
	}
	if command.ConnectHostname != "" || command.SNIHostname != "" {
		if err := syncThreeXUIRealityHost(ctx, baseURL, token, command.InboundID, command.ConnectHostname, command.SNIHostname); err != nil {
			return RealityCommandResult{}, err
		}
	}
	return RealityCommandResult{Action: "rename", InboundID: inbound.ID, DisplayName: inbound.Remark}, nil
}

func updateThreeXUIRealityInboundName(ctx context.Context, baseURL, token string, inboundID, nodeID int, displayName string) (threeXUIRealityInbound, error) {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/get/"+strconv.Itoa(inboundID), token, "", nil)
	if err != nil {
		return threeXUIRealityInbound{}, fmt.Errorf("agent: get 3x-ui REALITY inbound for rename: %w", err)
	}
	var inbound threeXUIRealityInbound
	var update map[string]any
	if json.Unmarshal(payload, &inbound) != nil || json.Unmarshal(payload, &update) != nil || inbound.ID != inboundID || inbound.Protocol != "vless" || !threeXUIInboundMatchesNode(inbound, nodeID) {
		return threeXUIRealityInbound{}, errors.New("agent: selected REALITY inbound is unavailable on this node")
	}
	var stream struct {
		Security string `json:"security"`
	}
	if json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Security != "reality" {
		return threeXUIRealityInbound{}, errors.New("agent: selected inbound is not VLESS REALITY")
	}
	if inbound.Remark == displayName {
		return inbound, nil
	}
	update["remark"] = displayName
	delete(update, "id")
	delete(update, "clientStats")
	delete(update, "fallbackParent")
	if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/update/"+strconv.Itoa(inboundID), token, "application/json", update); err != nil {
		return threeXUIRealityInbound{}, fmt.Errorf("agent: rename 3x-ui REALITY inbound: %w", err)
	}
	inbound.Remark = displayName
	return inbound, nil
}

func validRealityDisplayName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 64 || strings.ContainsAny(value, "/\\") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func threeXUIInboundMatchesNode(inbound threeXUIRealityInbound, nodeID int) bool {
	return (nodeID == 0 && inbound.NodeID == nil) || (nodeID > 0 && inbound.NodeID != nil && *inbound.NodeID == nodeID)
}

func attachAllThreeXUIClientsToInbound(ctx context.Context, baseURL, token string, inboundID int) error {
	clients, err := listThreeXUIClients(ctx, baseURL, token)
	if err != nil {
		return fmt.Errorf("agent: list clients for automatic node synchronization: %w", err)
	}
	emails := make([]string, 0, len(clients))
	for _, client := range clients {
		if !containsInt(client.InboundIDs, inboundID) {
			emails = append(emails, client.Email)
		}
	}
	if len(emails) == 0 {
		return nil
	}
	payload, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/bulkAttach", token, "application/json", map[string]any{"emails": emails, "inboundIds": []int{inboundID}})
	if err != nil {
		return fmt.Errorf("agent: attach existing clients to the new REALITY node: %w", err)
	}
	var result struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(payload, &result) != nil || len(result.Errors) != 0 {
		return errors.New("agent: 3x-ui could not attach every existing client to the new REALITY node")
	}
	return nil
}

func containsInt(values []int, expected int) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func threeXUIRealityStreamSettings(target, sni, privateKey, publicKey, shortID string) map[string]any {
	return map[string]any{
		"network":     "tcp",
		"tcpSettings": map[string]any{"header": map[string]string{"type": "none"}},
		"security":    "reality",
		"realitySettings": map[string]any{
			"show": false, "xver": 0, "target": target, "serverNames": []string{sni},
			"privateKey": privateKey, "minClientVer": threeXUIMihomoMinClientVersion,
			"maxClientVer": "", "maxTimediff": 0, "shortIds": []string{shortID},
			"settings": map[string]any{"publicKey": publicKey, "fingerprint": "chrome", "serverName": "", "spiderX": "/"},
		},
	}
}

func ensureThreeXUIRealityClientVersion(ctx context.Context, baseURL, token string, inboundID int) (threeXUIRealityInbound, error) {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/get/"+strconv.Itoa(inboundID), token, "", nil)
	if err != nil {
		return threeXUIRealityInbound{}, fmt.Errorf("agent: get 3x-ui REALITY inbound: %w", err)
	}
	var inbound threeXUIRealityInbound
	var update map[string]any
	if json.Unmarshal(payload, &inbound) != nil || json.Unmarshal(payload, &update) != nil || inbound.ID != inboundID {
		return threeXUIRealityInbound{}, errors.New("agent: 3x-ui returned invalid REALITY inbound data")
	}
	streamSettings, ok := update["streamSettings"].(map[string]any)
	if !ok || streamSettings["security"] != "reality" || update["protocol"] != "vless" {
		return threeXUIRealityInbound{}, errors.New("agent: selected inbound is not VLESS REALITY")
	}
	realitySettings, ok := streamSettings["realitySettings"].(map[string]any)
	if !ok {
		return threeXUIRealityInbound{}, errors.New("agent: selected REALITY inbound is incomplete")
	}
	if realitySettings["minClientVer"] == threeXUIMihomoMinClientVersion {
		return inbound, nil
	}
	realitySettings["minClientVer"] = threeXUIMihomoMinClientVersion
	delete(update, "id")
	delete(update, "clientStats")
	delete(update, "fallbackParent")
	if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/update/"+strconv.Itoa(inboundID), token, "application/json", update); err != nil {
		return threeXUIRealityInbound{}, fmt.Errorf("agent: enable Mihomo compatibility for 3x-ui REALITY inbound: %w", err)
	}
	encodedStreamSettings, err := json.Marshal(streamSettings)
	if err != nil {
		return threeXUIRealityInbound{}, err
	}
	inbound.StreamSettings = encodedStreamSettings
	return inbound, nil
}

func syncThreeXUIRealityHost(ctx context.Context, baseURL, token string, inboundID int, connectHostname, sniHostname string) error {
	connectHostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(connectHostname), "."))
	sniHostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(sniHostname), "."))
	if inboundID < 1 || !validThreeXUIShareHostname(connectHostname) || !validThreeXUIShareHostname(sniHostname) {
		return errors.New("agent: invalid public REALITY subscription endpoint")
	}
	groupID := "vastora-public-" + strconv.Itoa(inboundID)
	desired := threeXUIHostGroup{
		GroupID: groupID, InboundIDs: []int{inboundID}, Hosts: []string{connectHostname},
		Remark: "{{INBOUND}}", ServerDescription: "Managed by Vastora",
		Tags: []string{"vastora"}, Port: 443, Security: "same", SNI: sniHostname,
		Fingerprint: "chrome", MihomoIPVersion: "dual",
	}
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/hosts/byInbound/"+strconv.Itoa(inboundID), token, "", nil)
	if err != nil {
		return fmt.Errorf("agent: list 3x-ui REALITY subscription hosts: %w", err)
	}
	var groups []threeXUIHostGroup
	if json.Unmarshal(payload, &groups) != nil {
		return errors.New("agent: 3x-ui returned invalid subscription host data")
	}
	endpoint := baseURL + "/panel/api/hosts/add"
	for _, group := range groups {
		if group.GroupID != groupID {
			continue
		}
		if threeXUIRealityHostMatches(group, desired) {
			return nil
		}
		endpoint = baseURL + "/panel/api/hosts/update/" + url.PathEscape(groupID)
		break
	}
	if _, err := threeXUIAPI(ctx, http.MethodPost, endpoint, token, "application/json", desired); err != nil {
		return fmt.Errorf("agent: synchronize 3x-ui REALITY subscription host: %w", err)
	}
	return nil
}

func validThreeXUIShareHostname(value string) bool {
	if value == "" || len(value) > 253 || strings.ContainsAny(value, "/\\?#@:") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func threeXUIRealityHostMatches(actual, desired threeXUIHostGroup) bool {
	return actual.GroupID == desired.GroupID && len(actual.InboundIDs) == 1 && actual.InboundIDs[0] == desired.InboundIDs[0] &&
		len(actual.Hosts) == 1 && strings.EqualFold(strings.TrimSuffix(actual.Hosts[0], "."), desired.Hosts[0]) &&
		actual.Remark == desired.Remark && actual.ServerDescription == desired.ServerDescription && !actual.IsDisabled && !actual.IsHidden &&
		actual.Port == desired.Port && actual.Security == desired.Security && strings.EqualFold(strings.TrimSuffix(actual.SNI, "."), desired.SNI) &&
		actual.Fingerprint == desired.Fingerprint && actual.MihomoIPVersion == desired.MihomoIPVersion && len(actual.Tags) == 1 && actual.Tags[0] == "vastora"
}

func threeXUIClientEmail(name, commandID string) string {
	var value strings.Builder
	separator := false
	for _, character := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(character) || unicode.IsNumber(character) || character == '_' || character == '.' || character == '-' {
			if separator && value.Len() != 0 {
				value.WriteByte('-')
			}
			value.WriteRune(character)
			separator = false
		} else {
			separator = true
		}
	}
	base := strings.Trim(value.String(), "-._")
	if base == "" {
		base = "client"
	}
	if characters := []rune(base); len(characters) > 48 {
		base = strings.TrimRight(string(characters[:48]), "-._")
	}
	suffix := strings.TrimPrefix(commandID, "application-command-")
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return base + "-" + suffix
}

func selectRealityTarget(ctx context.Context, baseURL, token string, command RealityCommandTask) (string, string, error) {
	excluded := map[string]bool{}
	for _, hostname := range command.ExcludedSNI {
		excluded[strings.ToLower(strings.TrimSpace(hostname))] = true
	}
	if command.Target != "" {
		form := url.Values{"target": {command.Target}, "sni": {command.SNIHostname}, "xver": {"0"}}
		result, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/server/scanRealityTarget", token, "application/x-www-form-urlencoded", form)
		if err != nil {
			return "", "", fmt.Errorf("agent: scan custom REALITY target: %w", err)
		}
		var scan realityScanResult
		if json.Unmarshal(result, &scan) != nil || !scan.Feasible || excluded[command.SNIHostname] || !realityScanAllowsSNI(scan, command.SNIHostname) {
			return "", "", errors.New("agent: custom REALITY target is not feasible, its certificate does not cover the SNI, or the SNI is already in use")
		}
		return scan.Target, command.SNIHostname, nil
	}
	result, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/server/scanRealityTargets", token, "application/x-www-form-urlencoded", url.Values{"targets": {""}})
	if err != nil {
		return "", "", fmt.Errorf("agent: scan REALITY targets: %w", err)
	}
	var scans []realityScanResult
	if json.Unmarshal(result, &scans) != nil {
		return "", "", errors.New("agent: 3x-ui returned invalid REALITY target candidates")
	}
	for _, scan := range scans {
		if !scan.Feasible {
			continue
		}
		candidates := append([]string(nil), scan.ServerNames...)
		candidates = append(candidates, scan.Host)
		for _, candidate := range candidates {
			candidate = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(candidate), "."))
			if candidate != "" && !strings.HasPrefix(candidate, "*.") && !excluded[candidate] {
				return scan.Target, candidate, nil
			}
		}
	}
	return "", "", errors.New("agent: no feasible unused REALITY target was found from this node")
}

func realityScanAllowsSNI(scan realityScanResult, hostname string) bool {
	hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	for _, candidate := range append(append([]string(nil), scan.ServerNames...), scan.Host) {
		candidate = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(candidate), "."))
		if candidate == hostname {
			return true
		}
		if strings.HasPrefix(candidate, "*.") {
			suffix := strings.TrimPrefix(candidate, "*")
			prefix := strings.TrimSuffix(hostname, suffix)
			if prefix != hostname && prefix != "" && !strings.Contains(prefix, ".") {
				return true
			}
		}
	}
	return false
}

func findRealityInbound(ctx context.Context, baseURL, token, clientEmail string, nodeID int) (threeXUIRealityInbound, bool, error) {
	result, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/list", token, "", nil)
	if err != nil {
		return threeXUIRealityInbound{}, false, fmt.Errorf("agent: list 3x-ui inbounds: %w", err)
	}
	var inbounds []threeXUIRealityInbound
	if json.Unmarshal(result, &inbounds) != nil {
		return threeXUIRealityInbound{}, false, errors.New("agent: 3x-ui returned invalid inbound data")
	}
	for _, inbound := range inbounds {
		var settings struct {
			Clients []struct {
				Email string `json:"email"`
			} `json:"clients"`
		}
		if inbound.Protocol != "vless" || !threeXUIInboundMatchesNode(inbound, nodeID) || json.Unmarshal(inbound.Settings, &settings) != nil {
			continue
		}
		for _, client := range settings.Clients {
			if client.Email == clientEmail {
				return inbound, true, nil
			}
		}
	}
	return threeXUIRealityInbound{}, false, nil
}

func realityResultFromInbound(inbound threeXUIRealityInbound, connectHostname, displayName, clientName, clientEmail string) (RealityCommandResult, error) {
	var settings struct {
		Clients []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"clients"`
	}
	var stream struct {
		Security string `json:"security"`
		Reality  struct {
			Target      string   `json:"target"`
			ServerNames []string `json:"serverNames"`
			ShortIDs    []string `json:"shortIds"`
			Settings    struct {
				PublicKey string `json:"publicKey"`
			} `json:"settings"`
		} `json:"realitySettings"`
	}
	if json.Unmarshal(inbound.Settings, &settings) != nil || json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Security != "reality" || len(stream.Reality.ServerNames) == 0 || len(stream.Reality.ShortIDs) == 0 || stream.Reality.Settings.PublicKey == "" {
		return RealityCommandResult{}, errors.New("agent: existing Vastora REALITY inbound is incomplete")
	}
	clientID := ""
	for _, client := range settings.Clients {
		if client.Email == clientEmail {
			clientID = client.ID
			break
		}
	}
	if clientID == "" {
		return RealityCommandResult{}, errors.New("agent: existing Vastora REALITY client is unavailable")
	}
	return RealityCommandResult{Action: "create", InboundID: inbound.ID, DisplayName: displayName, ClientName: clientName, Listen: inbound.Listen, Port: inbound.Port, Target: stream.Reality.Target, SNIHostname: stream.Reality.ServerNames[0], ConnectHostname: connectHostname, ShareURI: realityShareURI(clientID, connectHostname, displayName, stream.Reality.ServerNames[0], stream.Reality.Settings.PublicKey, stream.Reality.ShortIDs[0])}, nil
}

func threeXUIAPI(ctx context.Context, method, endpoint, token, contentType string, payload any) (json.RawMessage, error) {
	var body io.Reader
	if payload != nil {
		switch value := payload.(type) {
		case url.Values:
			body = strings.NewReader(value.Encode())
		default:
			encoded, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			body = bytes.NewReader(encoded)
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := (&http.Client{Timeout: 45 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var envelope struct {
		Success bool            `json:"success"`
		Message string          `json:"msg"`
		Object  json.RawMessage `json:"obj"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&envelope) != nil || response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		return nil, fmt.Errorf("3x-ui rejected the request: %s", strings.TrimSpace(envelope.Message))
	}
	return envelope.Object, nil
}

func availableTCPPort(address string) (int, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if err != nil {
		return 0, fmt.Errorf("agent: allocate private REALITY port: %w", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func availableRealityPort(ctx context.Context, baseURL, token string, nodeID int, address string) (int, error) {
	if nodeID == 0 {
		return availableTCPPort(address)
	}
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/list", token, "", nil)
	if err != nil {
		return 0, fmt.Errorf("agent: inspect remote REALITY ports: %w", err)
	}
	var inbounds []struct {
		Port   int  `json:"port"`
		NodeID *int `json:"nodeId,omitempty"`
	}
	if json.Unmarshal(payload, &inbounds) != nil {
		return 0, errors.New("agent: 3x-ui returned invalid remote inbound data")
	}
	used := map[int]bool{}
	for _, inbound := range inbounds {
		if inbound.NodeID != nil && *inbound.NodeID == nodeID {
			used[inbound.Port] = true
		}
	}
	for attempt := 0; attempt < 32; attempt++ {
		value := make([]byte, 2)
		if _, err := rand.Read(value); err != nil {
			return 0, fmt.Errorf("agent: generate remote REALITY port: %w", err)
		}
		port := 20000 + ((int(value[0])<<8)|int(value[1]))%40000
		if !used[port] {
			return port, nil
		}
	}
	return 0, errors.New("agent: could not allocate an unused remote REALITY port")
}

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("agent: generate REALITY client id: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func realityShareURI(clientID, hostname, name, sni, publicKey, shortID string) string {
	query := url.Values{"encryption": {"none"}, "flow": {"xtls-rprx-vision"}, "security": {"reality"}, "sni": {sni}, "fp": {"chrome"}, "pbk": {publicKey}, "sid": {shortID}, "spx": {"/"}, "type": {"tcp"}, "headerType": {"none"}}
	return "vless://" + clientID + "@" + net.JoinHostPort(hostname, "443") + "?" + query.Encode() + "#" + url.PathEscape(name)
}
