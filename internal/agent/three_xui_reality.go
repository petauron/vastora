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
	Listen         string          `json:"listen"`
	Port           int             `json:"port"`
	Settings       json.RawMessage `json:"settings"`
	StreamSettings json.RawMessage `json:"streamSettings"`
}

type realityScanResult struct {
	Target      string   `json:"target"`
	Host        string   `json:"host"`
	Feasible    bool     `json:"feasible"`
	ServerNames []string `json:"serverNames"`
}

func applyRealityCommand(ctx context.Context, store *Store, commandID string, command RealityCommandTask) (RealityCommandResult, error) {
	installation, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil {
		return RealityCommandResult{}, errors.New("agent: official 3x-ui is not installed")
	}
	config, err := decodeThreeXUIConfig(installation.Config)
	if err != nil {
		return RealityCommandResult{}, err
	}
	var secrets map[string]string
	if json.Unmarshal(installation.Secrets, &secrets) != nil || strings.TrimSpace(secrets["api_token"]) == "" {
		return RealityCommandResult{}, errors.New("agent: 3x-ui API token is unavailable")
	}
	listen := installation.ServiceAddress
	if net.ParseIP(listen) == nil {
		return RealityCommandResult{}, errors.New("agent: 3x-ui private service address is invalid")
	}
	baseURL := "http://" + net.JoinHostPort(listen, strconv.Itoa(config.PanelPort))
	remark := "vastora-reality-" + strings.TrimPrefix(commandID, "application-command-")
	if existing, ok, err := findRealityInbound(ctx, baseURL, secrets["api_token"], remark); err != nil {
		return RealityCommandResult{}, err
	} else if ok {
		return realityResultFromInbound(existing, command.ConnectHostname, command.Name)
	}

	target, sni, err := selectRealityTarget(ctx, baseURL, secrets["api_token"], command)
	if err != nil {
		return RealityCommandResult{}, err
	}
	keys, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/server/getNewX25519Cert", secrets["api_token"], "", nil)
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
	port, err := availableTCPPort(listen)
	if err != nil {
		return RealityCommandResult{}, err
	}
	payload := map[string]any{
		"enable": true, "remark": remark, "listen": listen, "port": port, "protocol": "vless", "expiryTime": 0, "total": 0,
		"settings":       map[string]any{"clients": []map[string]any{{"id": clientID, "email": threeXUIClientEmail(command.Name, commandID), "flow": "xtls-rprx-vision", "limitIp": 0, "totalGB": 0, "expiryTime": 0, "enable": true}}, "decryption": "none", "encryption": "none", "fallbacks": []any{}},
		"streamSettings": map[string]any{"network": "tcp", "tcpSettings": map[string]any{"header": map[string]string{"type": "none"}}, "security": "reality", "realitySettings": map[string]any{"show": false, "xver": 0, "target": target, "serverNames": []string{sni}, "privateKey": keyPair.PrivateKey, "minClientVer": "", "maxClientVer": "", "maxTimediff": 0, "shortIds": []string{shortID}, "settings": map[string]any{"publicKey": keyPair.PublicKey, "fingerprint": "chrome", "serverName": "", "spiderX": "/"}}},
		"sniffing":       map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "metadataOnly": false, "routeOnly": false},
	}
	added, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/add", secrets["api_token"], "application/json", payload)
	if err != nil {
		return RealityCommandResult{}, fmt.Errorf("agent: create 3x-ui REALITY inbound: %w", err)
	}
	var inbound threeXUIRealityInbound
	if json.Unmarshal(added, &inbound) != nil || inbound.ID < 1 {
		return RealityCommandResult{}, errors.New("agent: 3x-ui returned an invalid REALITY inbound")
	}
	return RealityCommandResult{InboundID: inbound.ID, Name: command.Name, Listen: listen, Port: port, Target: target, SNIHostname: sni, ConnectHostname: command.ConnectHostname, ShareURI: realityShareURI(clientID, command.ConnectHostname, command.Name, sni, keyPair.PublicKey, shortID)}, nil
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

func findRealityInbound(ctx context.Context, baseURL, token, remark string) (threeXUIRealityInbound, bool, error) {
	result, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/list", token, "", nil)
	if err != nil {
		return threeXUIRealityInbound{}, false, fmt.Errorf("agent: list 3x-ui inbounds: %w", err)
	}
	var inbounds []threeXUIRealityInbound
	if json.Unmarshal(result, &inbounds) != nil {
		return threeXUIRealityInbound{}, false, errors.New("agent: 3x-ui returned invalid inbound data")
	}
	for _, inbound := range inbounds {
		if inbound.Remark == remark {
			return inbound, true, nil
		}
	}
	return threeXUIRealityInbound{}, false, nil
}

func realityResultFromInbound(inbound threeXUIRealityInbound, connectHostname, name string) (RealityCommandResult, error) {
	var settings struct {
		Clients []struct {
			ID string `json:"id"`
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
	if json.Unmarshal(inbound.Settings, &settings) != nil || json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Security != "reality" || len(settings.Clients) == 0 || len(stream.Reality.ServerNames) == 0 || len(stream.Reality.ShortIDs) == 0 || stream.Reality.Settings.PublicKey == "" {
		return RealityCommandResult{}, errors.New("agent: existing Vastora REALITY inbound is incomplete")
	}
	return RealityCommandResult{InboundID: inbound.ID, Name: name, Listen: inbound.Listen, Port: inbound.Port, Target: stream.Reality.Target, SNIHostname: stream.Reality.ServerNames[0], ConnectHostname: connectHostname, ShareURI: realityShareURI(settings.Clients[0].ID, connectHostname, name, stream.Reality.ServerNames[0], stream.Reality.Settings.PublicKey, stream.Reality.ShortIDs[0])}, nil
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
