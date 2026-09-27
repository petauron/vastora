package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/petauron/vastora/internal/networking"
)

const (
	threeXUIRealityPort = 443
)

func threeXUIPorts(bindAddress string, panelPort int, role string) (dockernetwork.PortSet, dockernetwork.PortMap, error) {
	if role != "master" {
		return nil, nil, errors.New("agent: only the controller adapter may deploy 3x-ui")
	}
	if err := validateThreeXUIServiceAddress(bindAddress, panelPort, role); err != nil {
		return nil, nil, err
	}
	address := netip.MustParseAddr(bindAddress)
	exposed := dockernetwork.PortSet{}
	bindings := dockernetwork.PortMap{}
	expose := func(portNumber int) dockernetwork.Port {
		port := dockernetwork.MustParsePort(strconv.Itoa(portNumber) + "/tcp")
		exposed[port] = struct{}{}
		return port
	}
	bind := func(portNumber int) {
		port := expose(portNumber)
		bindings[port] = []dockernetwork.PortBinding{{HostIP: address.Unmap(), HostPort: strconv.Itoa(portNumber)}}
	}
	bind(panelPort)
	// REALITY is reachable only from the per-node HAProxy over the shared
	// Docker network. Never publish the raw controller adapter socket.
	expose(threeXUIRealityPort)
	return exposed, bindings, nil
}

func validateThreeXUIServiceAddress(bindAddress string, panelPort int, role string) error {
	address, err := netip.ParseAddr(bindAddress)
	if err != nil || !address.Unmap().Is4() {
		return errors.New("agent: proxy runtime requires a valid IPv4 service address")
	}
	if !networking.IsPrivateServiceAddress(address.String()) {
		return errors.New("agent: proxy runtime requires a LAN, Headscale/Tailscale, or loopback service address")
	}
	if role != "master" && role != "worker" {
		return errors.New("agent: invalid proxy topology role")
	}
	if panelPort == threeXUIRealityPort {
		return errors.New("agent: proxy management port must not use the managed REALITY port")
	}
	return nil
}

func configureThreeXUISubscriptionRole(ctx context.Context, address string, panelPort int, apiToken, role string) error {
	if role != "master" && role != "worker" {
		return errors.New("agent: invalid 3x-ui topology role")
	}
	baseURL := "http://" + net.JoinHostPort(address, strconv.Itoa(panelPort))
	settings, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/all", apiToken, map[string]any{})
	if err != nil {
		return fmt.Errorf("agent: read 3x-ui subscription settings: %w", err)
	}
	settings["subEnable"] = false
	settings["subClashEnable"] = false
	if _, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/update", apiToken, settings); err != nil {
		return fmt.Errorf("agent: disable superseded 3x-ui subscription service: %w", err)
	}
	return restartThreeXUIPanel(ctx, baseURL, apiToken, threeXUIRestartSettleTime)
}

type threeXUIConfig struct {
	Timezone        string `json:"timezone"`
	PanelPort       int    `json:"panel_port"`
	EnableFail2ban  bool   `json:"enable_fail2ban"`
	VMessAEADForced bool   `json:"vmess_aead_forced"`
}

func decodeThreeXUIConfig(raw json.RawMessage) (threeXUIConfig, error) {
	var config threeXUIConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return config, errors.New("agent: invalid 3x-ui configuration")
	}
	if config.Timezone == "" || config.PanelPort < 1024 || config.PanelPort > 65535 {
		return config, errors.New("agent: invalid 3x-ui configuration")
	}
	return config, nil
}

func threeXUIRequest(ctx context.Context, method, endpoint, token string, body any) (map[string]any, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var result struct {
		Success bool           `json:"success"`
		Message string         `json:"msg"`
		Object  map[string]any `json:"obj"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result) != nil || response.StatusCode < 200 || response.StatusCode >= 300 || !result.Success {
		return nil, fmt.Errorf("3x-ui rejected the request: %s", strings.TrimSpace(result.Message))
	}
	if result.Object == nil {
		result.Object = map[string]any{}
	}
	return result.Object, nil
}
