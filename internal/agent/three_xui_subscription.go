package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

const (
	threeXUIRawSubscriptionPath   = "/sub/"
	threeXUIClashSubscriptionPath = "/clash/"
)

func applySubscriptionCommand(ctx context.Context, store *Store, command SubscriptionCommandTask) (SubscriptionCommandResult, error) {
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(command.Domain), "."))
	baseURI := strings.TrimSpace(command.BaseURI)
	parsed, err := url.Parse(baseURI)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != domain || parsed.Port() != "" || parsed.Path != threeXUIRawSubscriptionPath || parsed.RawQuery != "" || parsed.Fragment != "" {
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
	if _, err := configureThreeXUIPublicSubscription(ctx, endpoint, secrets["api_token"], domain, baseURI); err != nil {
		return SubscriptionCommandResult{}, err
	}
	return SubscriptionCommandResult{Domain: domain, BaseURI: baseURI}, nil
}

func configureThreeXUIPublicSubscription(ctx context.Context, endpoint, token, domain, rawBaseURI string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawBaseURI))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != domain || parsed.Port() != "" || parsed.Path != threeXUIRawSubscriptionPath || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("agent: invalid 3x-ui public subscription address")
	}
	clashURI := *parsed
	clashURI.Path = threeXUIClashSubscriptionPath
	settings, err := threeXUIRequest(ctx, http.MethodPost, endpoint+"/panel/api/setting/all", token, map[string]any{})
	if err != nil {
		return "", fmt.Errorf("agent: read 3x-ui subscription settings: %w", err)
	}
	desired := map[string]any{
		"subEnable":          true,
		"subPath":            threeXUIRawSubscriptionPath,
		"subDomain":          domain,
		"subURI":             parsed.String(),
		"subClashEnable":     true,
		"subClashPath":       threeXUIClashSubscriptionPath,
		"subClashURI":        clashURI.String(),
		"subClashAutoDetect": true,
	}
	changed := false
	for key, value := range desired {
		if !reflect.DeepEqual(settings[key], value) {
			settings[key] = value
			changed = true
		}
	}
	if changed {
		if _, err := threeXUIRequest(ctx, http.MethodPost, endpoint+"/panel/api/setting/update", token, settings); err != nil {
			return "", fmt.Errorf("agent: update 3x-ui public subscription formats: %w", err)
		}
	}
	return clashURI.String(), nil
}
