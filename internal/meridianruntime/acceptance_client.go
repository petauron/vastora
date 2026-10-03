package meridianruntime

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"

	"github.com/petauron/meridian"
)

// AcceptanceClient carries the original business credential and public entry
// identity over the encrypted task channel. It must never enter a public DTO,
// log, command-line argument or unencrypted execution record.
type AcceptanceClient struct {
	Protocol meridian.ProtocolKind       `json:"protocol"`
	Reality  *meridian.RealityEndpoint   `json:"reality,omitempty"`
	Hysteria *meridian.HysteriaEndpoint  `json:"hysteria,omitempty"`
	Material meridian.CredentialMaterial `json:"material"`
}

// Config creates one real Xray client, using the same validated credential
// rendering as subscriptions. There is exactly one outbound: no selector,
// direct fallback, environment proxy, client API or public SOCKS listener.
// Native and fixed-route credentials retain their distinct protocol identity.
func (c AcceptanceClient) Config(socksPort int) ([]byte, error) {
	if socksPort < 1024 || socksPort > 65535 || !c.Material.Credential.Enabled {
		return nil, errors.New("acceptance: invalid client configuration")
	}
	var link string
	var err error
	switch c.Protocol {
	case meridian.VLESSReality:
		if c.Reality == nil || c.Hysteria != nil || c.Reality.PrivateKey != "" {
			return nil, errors.New("acceptance: invalid public REALITY identity")
		}
		link, err = meridian.LinkForCredential(*c.Reality, c.Material, "recovery")
	case meridian.Hysteria2:
		if c.Hysteria == nil || c.Reality != nil || c.Hysteria.PrivateKeyPEM != "" {
			return nil, errors.New("acceptance: invalid public Hysteria identity")
		}
		link, err = meridian.Hysteria2LinkForCredential(*c.Hysteria, c.Material, "recovery")
	default:
		return nil, errors.New("acceptance: unsupported client protocol")
	}
	if err != nil {
		return nil, errors.New("acceptance: invalid saved business identity")
	}
	parsed, err := url.Parse(link)
	if err != nil {
		return nil, errors.New("acceptance: invalid subscription address")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		return nil, errors.New("acceptance: invalid subscription port")
	}
	query := parsed.Query()
	outbound := map[string]any{"tag": "verified-route"}
	if c.Protocol == meridian.VLESSReality {
		outbound["protocol"] = "vless"
		outbound["settings"] = map[string]any{"address": parsed.Hostname(), "port": port, "id": parsed.User.Username(), "encryption": "none", "flow": query.Get("flow")}
		outbound["streamSettings"] = map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"serverName": query.Get("sni"), "fingerprint": query.Get("fp"), "password": query.Get("pbk"), "shortId": query.Get("sid"), "spiderX": query.Get("spx")}}
	} else {
		outbound["protocol"] = "hysteria"
		outbound["settings"] = map[string]any{"version": 2, "address": parsed.Hostname(), "port": port}
		outbound["streamSettings"] = map[string]any{"method": "hysteria", "security": "tls", "tlsSettings": map[string]any{"serverName": query.Get("sni"), "alpn": []string{"h3"}, "allowInsecure": false}, "hysteriaSettings": map[string]any{"version": 2, "auth": parsed.User.Username()}}
	}
	return json.Marshal(map[string]any{
		"log":       map[string]any{"loglevel": "none"},
		"inbounds":  []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}},
		"outbounds": []any{outbound},
	})
}
