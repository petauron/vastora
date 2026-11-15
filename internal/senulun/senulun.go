// Package senulun defines the monitoring integration shared by Center and Agent.
// Immutable catalog and task identifiers are retained as protocol identities,
// not aliases or alternate product implementations.
package senulun

import (
	"errors"
	"net/url"
	"strings"

	"golang.org/x/text/language"
)

const (
	ServiceKey           = "vastora-official/senlun"
	AgentKey             = "vastora-official/senlun-agent"
	EnrollmentKind       = "pulse.enrollment.create"
	DefaultGeoIPProvider = "geojs"
)

type AgentConfig struct {
	ServiceURL string `json:"service_url"`
	// Only historical installation evidence carries a Center application ID.
	ServiceApplicationID string `json:"service_application_id,omitempty"`
	NodeName             string `json:"node_name"`
	NodeGroup            string `json:"node_group"`
	// Nil preserves an existing host override; a supplied empty region selects automatic discovery.
	NodeRegion    *string `json:"node_region,omitempty"`
	GeoIPProvider *string `json:"geoip_provider,omitempty"`
}

type EnrollmentTask struct {
	ApplicationID string `json:"applicationId"`
	DeploymentID  string `json:"deploymentId"`
}

type EnrollmentResult struct {
	ID              string `json:"id"`
	Token           string `json:"token"`
	ExpiresAtUnixMS int64  `json:"expires_at_unix_ms"`
}

func (config AgentConfig) Validate() error {
	u, err := url.Parse(config.ServiceURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/" || strings.ContainsAny(config.ServiceURL, "\r\n") {
		return errors.New("senulun: an HTTPS service URL ending in / is required")
	}
	if strings.TrimSpace(config.NodeName) == "" || len(config.NodeName) > 128 || len(config.NodeGroup) > 128 || strings.ContainsAny(config.NodeName+config.NodeGroup, "\r\n\x00") {
		return errors.New("senulun: invalid monitoring node identity")
	}
	if config.NodeRegion != nil && *config.NodeRegion != "" {
		code := *config.NodeRegion
		region, err := language.ParseRegion(code)
		if len(code) != 2 || err != nil || !region.IsCountry() || region.IsPrivateUse() {
			return errors.New("senulun: node region must be an ISO 3166-1 alpha-2 country code or empty")
		}
	}
	if config.GeoIPProvider != nil {
		switch *config.GeoIPProvider {
		case "", "geojs", "ipinfo", "disabled":
		default:
			return errors.New("senulun: GeoIP provider must be geojs, ipinfo, or disabled")
		}
	}
	return nil
}
