package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func applySubscriptionCommand(ctx context.Context, store *Store, command SubscriptionCommandTask) (SubscriptionCommandResult, error) {
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(command.Domain), "."))
	baseURI := strings.TrimSpace(command.BaseURI)
	parsed, err := url.Parse(baseURI)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != domain || parsed.Port() != "" || parsed.Path != "/sub/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return SubscriptionCommandResult{}, errors.New("agent: invalid 3x-ui public subscription address")
	}
	installation, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil {
		return SubscriptionCommandResult{}, errors.New("agent: official 3x-ui is not installed")
	}
	config, err := decodeThreeXUIConfig(installation.Config)
	if err != nil {
		return SubscriptionCommandResult{}, err
	}
	var secrets map[string]string
	if json.Unmarshal(installation.Secrets, &secrets) != nil || strings.TrimSpace(secrets["api_token"]) == "" {
		return SubscriptionCommandResult{}, errors.New("agent: 3x-ui API token is unavailable")
	}
	if net.ParseIP(installation.ServiceAddress) == nil {
		return SubscriptionCommandResult{}, errors.New("agent: 3x-ui private service address is invalid")
	}
	endpoint := "http://" + net.JoinHostPort(installation.ServiceAddress, strconv.Itoa(config.PanelPort))
	settings, err := threeXUIRequest(ctx, http.MethodPost, endpoint+"/panel/api/setting/all", secrets["api_token"], map[string]any{})
	if err != nil {
		return SubscriptionCommandResult{}, fmt.Errorf("agent: read 3x-ui subscription settings: %w", err)
	}
	settings["subEnable"] = true
	settings["subPath"] = "/sub/"
	settings["subDomain"] = domain
	settings["subURI"] = baseURI
	if _, err := threeXUIRequest(ctx, http.MethodPost, endpoint+"/panel/api/setting/update", secrets["api_token"], settings); err != nil {
		return SubscriptionCommandResult{}, fmt.Errorf("agent: update 3x-ui public subscription address: %w", err)
	}
	return SubscriptionCommandResult{Domain: domain, BaseURI: baseURI}, nil
}
