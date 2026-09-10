package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/nodeprotocol"
)

func (c Client) applyNodeProtocols(ctx context.Context, store *Store, task nodeprotocol.Task) (nodeprotocol.Result, error) {
	result := nodeprotocol.Result{Phase: task.Phase}
	if task.Selection.Validate() != nil || task.InboundID < 1 || task.InboundTag == "" || task.ApplicationID == "" {
		return result, errors.New("agent: invalid node protocol request")
	}
	if task.Phase == "prepare" {
		executor, ok := c.Executor.(interface {
			ConfigureHY2Port(context.Context, *Store, string, bool) error
		})
		if !ok {
			return result, errors.New("agent: protocol configuration is unavailable")
		}
		return result, executor.ConfigureHY2Port(ctx, store, task.ApplicationID, task.HY2)
	}
	if task.Phase == "verify" {
		routes, err := store.localThreeXUILandingRoutes(ctx, task.ApplicationID)
		if err != nil {
			return result, err
		}
		verifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			all, err := listRealityInbounds(verifyCtx, routes.baseURL, routes.token)
			if err == nil && nodeProtocolsMatch(all, task) {
				result.HY2InboundID = task.HY2InboundID
				return result, nil
			}
			select {
			case <-verifyCtx.Done():
				return result, deferTaskUntilReconciled(errors.New("agent: node protocols have not converged"))
			case <-ticker.C:
			}
		}
	}
	if task.Phase != "apply" {
		return result, errors.New("agent: invalid node protocol phase")
	}
	baseURL, token, err := threeXUIClientAPIConnection(ctx, store)
	if err != nil {
		return result, err
	}
	if task.HY2 {
		if err := validateHY2Certificate(task); err != nil {
			return result, err
		}
	}
	result.HY2InboundID, err = configureThreeXUIProtocols(ctx, baseURL, token, task)
	if err != nil {
		// Each write uses stable tags and preserves clients. Reconcile a lost
		// response instead of reporting a false success or creating duplicates.
		return result, deferTaskUntilReconciled(errors.New("agent: node protocol update needs reconciliation"))
	}
	return result, nil
}

func nodeProtocolsMatch(all []threeXUIRealityInbound, task nodeprotocol.Task) bool {
	vlessFound, hy2Found := false, !task.HY2
	for _, inbound := range all {
		tag := normalizedThreeXUIInboundTag(inbound.Tag, task.TargetNodeID)
		if tag == normalizedThreeXUIInboundTag(task.InboundTag, task.TargetNodeID) && inbound.Protocol == "vless" {
			expected, err := nodeProtocolVLESSRunState(inbound, task.VLESS)
			vlessFound = err == nil && inbound.Enable == expected
		}
		if tag == normalizedThreeXUIInboundTag(nodeprotocol.HY2Tag(task.InboundTag), task.TargetNodeID) && inbound.Protocol == "hysteria" {
			hy2Found = inbound.Enable == task.HY2 && (!task.HY2 || validHY2Runtime(inbound, task.Hostname))
		}
	}
	return vlessFound && hy2Found
}

func validateHY2Certificate(task nodeprotocol.Task) error {
	pair, err := tls.X509KeyPair([]byte(task.CertificatePEM), []byte(task.PrivateKeyPEM))
	if err != nil || len(pair.Certificate) == 0 {
		return errors.New("agent: HY2 certificate is invalid")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.VerifyHostname(task.Hostname) != nil || leaf.NotBefore.After(time.Now()) || !leaf.NotAfter.After(time.Now().Add(time.Hour)) {
		return errors.New("agent: HY2 certificate does not cover the node domain")
	}
	return nil
}

func configureThreeXUIProtocols(ctx context.Context, baseURL, token string, task nodeprotocol.Task) (int, error) {
	all, err := listRealityInbounds(ctx, baseURL, token)
	if err != nil {
		return 0, err
	}
	var primary, hy2 *threeXUIRealityInbound
	matchingTag := func(actual, expected string) bool {
		return normalizedThreeXUIInboundTag(actual, task.TargetNodeID) == normalizedThreeXUIInboundTag(expected, task.TargetNodeID)
	}
	for i := range all {
		inbound := &all[i]
		if !threeXUIInboundMatchesNode(*inbound, task.TargetNodeID) {
			continue
		}
		if inbound.ID == task.InboundID && matchingTag(inbound.Tag, task.InboundTag) && inbound.Protocol == "vless" {
			primary = inbound
		}
		if matchingTag(inbound.Tag, nodeprotocol.HY2Tag(task.InboundTag)) {
			if inbound.Protocol != "hysteria" {
				return 0, errors.New("agent: HY2 inbound identity conflicts with another protocol")
			}
			hy2 = inbound
		}
		var transport struct {
			Network string `json:"network"`
		}
		_ = json.Unmarshal(inbound.StreamSettings, &transport)
		if task.HY2 && inbound.Port == 443 && inbound.Enable && !matchingTag(inbound.Tag, nodeprotocol.HY2Tag(task.InboundTag)) && (inbound.Protocol == "hysteria" || inbound.Protocol == "shadowsocks" || inbound.Protocol == "socks" || transport.Network == "hysteria" || transport.Network == "quic" || transport.Network == "kcp") {
			return 0, errors.New("agent: UDP 443 is already used by another inbound")
		}
	}
	if primary == nil {
		return 0, errors.New("agent: managed subscription node identity changed")
	}
	if task.HY2 && hy2 == nil {
		payload := map[string]any{"tag": nodeprotocol.HY2Tag(task.InboundTag), "remark": task.DisplayName + " · HY2", "protocol": "hysteria", "listen": "0.0.0.0", "port": 443, "enable": false, "settings": map[string]any{"version": 2, "clients": []any{}}, "streamSettings": hy2StreamSettings(task), "trafficReset": "never", "trafficResetDay": 1, "total": 0}
		if task.TargetNodeID > 0 {
			payload["nodeId"] = task.TargetNodeID
		}
		if _, err = threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/add", token, "application/json", payload); err != nil {
			return 0, errors.New("agent: could not create the managed HY2 inbound")
		}
		all, err = listRealityInbounds(ctx, baseURL, token)
		if err != nil {
			return 0, err
		}
		for i := range all {
			if matchingTag(all[i].Tag, nodeprotocol.HY2Tag(task.InboundTag)) && all[i].Protocol == "hysteria" && threeXUIInboundMatchesNode(all[i], task.TargetNodeID) {
				hy2 = &all[i]
				break
			}
		}
		if hy2 == nil {
			return 0, errors.New("agent: HY2 inbound creation was not confirmed")
		}
	}
	if hy2 != nil {
		if task.HY2 {
			// Attach only clients already assigned to this node, not every global
			// subscriber. 3x-ui owns auth generation and cross-protocol accounting.
			clients, err := listThreeXUIClients(ctx, baseURL, token)
			if err != nil {
				return 0, err
			}
			for _, client := range clients {
				if !containsInt(client.InboundIDs, primary.ID) || containsInt(client.InboundIDs, hy2.ID) {
					continue
				}
				if err := syncThreeXUIClientInbounds(ctx, baseURL, token, client.Email, client.InboundIDs, append(append([]int(nil), client.InboundIDs...), hy2.ID)); err != nil {
					return 0, err
				}
			}
			update, err := readThreeXUIInboundUpdate(ctx, baseURL, token, hy2.ID)
			if err != nil {
				return 0, err
			}
			update["streamSettings"] = hy2StreamSettings(task)
			update["remark"] = task.DisplayName + " · HY2"
			update["enable"] = true
			// The existing inbound plan is explicitly a VLESS plan. HY2 uses
			// the same global client quota, not a second copy of that node limit.
			update["total"] = 0
			if err := writeThreeXUIInboundUpdate(ctx, baseURL, token, hy2.ID, update); err != nil {
				return 0, errors.New("agent: HY2 configuration could not be confirmed")
			}
			if err := syncThreeXUIRealityHost(ctx, baseURL, token, hy2.ID, task.Hostname, task.Hostname); err != nil {
				return 0, err
			}
		} else if err := setThreeXUIProtocolEnabled(ctx, baseURL, token, hy2.ID, false); err != nil {
			return 0, err
		}
	}
	// Enable HY2 and complete its subscription mapping before disabling VLESS.
	vlessEnabled, err := nodeProtocolVLESSRunState(*primary, task.VLESS)
	if err != nil {
		return 0, err
	}
	if err := setThreeXUIProtocolEnabled(ctx, baseURL, token, primary.ID, vlessEnabled); err != nil {
		return 0, err
	}
	if hy2 != nil {
		return hy2.ID, nil
	}
	return 0, nil
}

func nodeProtocolVLESSRunState(inbound threeXUIRealityInbound, selected bool) (bool, error) {
	if !selected || inbound.Total <= 0 {
		return selected, nil
	}
	used, err := threeXUIInboundUsedBytes(inbound)
	if err != nil {
		return false, err
	}
	return used < inbound.Total, nil
}

func hy2StreamSettings(task nodeprotocol.Task) map[string]any {
	// Never leave an empty transport password when the last user is removed.
	// Real subscriptions use the independently generated per-client auth values.
	mac := hmac.New(sha256.New, []byte(task.PrivateKeyPEM))
	mac.Write([]byte("vastora/hy2/empty-users/" + task.Hostname))
	return map[string]any{
		"network": "hysteria", "security": "tls",
		"hysteriaSettings": map[string]any{"version": 2, "auth": hex.EncodeToString(mac.Sum(nil)), "udpIdleTimeout": 60, "masquerade": map[string]any{"type": ""}},
		"tlsSettings":      map[string]any{"serverName": task.Hostname, "alpn": []string{"h3"}, "minVersion": "1.3", "certificates": []any{map[string]any{"certificate": strings.Split(strings.TrimSpace(task.CertificatePEM), "\n"), "key": strings.Split(strings.TrimSpace(task.PrivateKeyPEM), "\n"), "usage": "encipherment"}}},
	}
}

func setThreeXUIProtocolEnabled(ctx context.Context, baseURL, token string, id int, enabled bool) error {
	update, err := readThreeXUIInboundUpdate(ctx, baseURL, token, id)
	if err != nil {
		return err
	}
	if update["enable"] != enabled {
		update["enable"] = enabled
		if err := writeThreeXUIInboundUpdate(ctx, baseURL, token, id, update); err != nil {
			return err
		}
	}
	actual, err := readThreeXUIInboundUpdate(ctx, baseURL, token, id)
	if err != nil {
		return err
	}
	if actual["enable"] != enabled {
		return errors.New("agent: node protocol state did not match the request")
	}
	return nil
}

func validHY2Runtime(inbound threeXUIRealityInbound, hostname string) bool {
	var stream struct {
		Network  string `json:"network"`
		Security string `json:"security"`
		TLS      struct {
			ServerName   string `json:"serverName"`
			Certificates []struct {
				Certificate []string `json:"certificate"`
				Key         []string `json:"key"`
			} `json:"certificates"`
		} `json:"tlsSettings"`
		Hysteria struct {
			Version int    `json:"version"`
			Auth    string `json:"auth"`
		} `json:"hysteriaSettings"`
	}
	if inbound.Port != 443 || json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Network != "hysteria" || stream.Security != "tls" || stream.Hysteria.Version != 2 || stream.Hysteria.Auth == "" || stream.TLS.ServerName != hostname || len(stream.TLS.Certificates) == 0 {
		return false
	}
	return validateHY2Certificate(nodeprotocol.Task{Hostname: hostname, CertificatePEM: strings.Join(stream.TLS.Certificates[0].Certificate, "\n"), PrivateKeyPEM: strings.Join(stream.TLS.Certificates[0].Key, "\n")}) == nil
}
