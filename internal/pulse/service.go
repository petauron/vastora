package pulse

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

type ServiceConfig struct {
	PublicURL string `json:"public_url"`
}

type ServiceSecrets struct {
	SetupToken string `json:"setup_token"`
}

// DecodeServiceConfig is shared by Center and Agent so invalid authentication
// configuration is rejected before a task is stored or a container is replaced.
func DecodeServiceConfig(rawConfig, rawSecrets json.RawMessage) (ServiceConfig, ServiceSecrets, error) {
	var config ServiceConfig
	var secrets ServiceSecrets
	if json.Unmarshal(rawConfig, &config) != nil || json.Unmarshal(rawSecrets, &secrets) != nil {
		return config, secrets, errors.New("pulse: invalid service authentication configuration")
	}
	u, err := url.Parse(config.PublicURL)
	if err != nil || len(config.PublicURL) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(config.PublicURL, "\r\n\x00#") {
		return config, secrets, errors.New("pulse: public_url must be the dashboard's HTTPS origin without a path, query, or credentials")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return config, secrets, errors.New("pulse: public_url has an invalid HTTPS port")
		}
	}
	if len(secrets.SetupToken) < 32 || len(secrets.SetupToken) > 4096 || strings.TrimSpace(secrets.SetupToken) != secrets.SetupToken || strings.ContainsAny(secrets.SetupToken, "\r\n\x00") {
		return config, secrets, errors.New("pulse: setup_token must contain 32 to 4096 bytes without surrounding whitespace or line breaks")
	}
	return config, secrets, nil
}
