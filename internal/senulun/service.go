package senulun

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

// DecodeServiceConfig rejects an invalid browser origin before storing a task.
func DecodeServiceConfig(rawConfig json.RawMessage) (ServiceConfig, error) {
	var config ServiceConfig
	if json.Unmarshal(rawConfig, &config) != nil {
		return config, errors.New("senulun: invalid service authentication configuration")
	}
	u, err := url.Parse(config.PublicURL)
	if err != nil || len(config.PublicURL) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(config.PublicURL, "\r\n\x00#") {
		return config, errors.New("senulun: public_url must be the dashboard's HTTPS origin without a path, query, or credentials")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return config, errors.New("senulun: public_url has an invalid HTTPS port")
		}
	}
	return config, nil
}
