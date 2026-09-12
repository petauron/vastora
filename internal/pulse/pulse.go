// Package pulse defines the fixed Pulse integration contract shared by Center
// and Agent. Pulse remains a separate monitoring service and native collector.
package pulse

import (
	"errors"
	"net/url"
	"strings"

	"golang.org/x/text/language"
)

const (
	ServiceKey             = "vastora-official/pulse"
	AgentKey               = "vastora-official/pulse-agent"
	EnrollmentKind         = "pulse.enrollment.create"
	DefaultIntervalSeconds = 3
	DefaultGeoIPProvider   = "geojs"
)

type AgentConfig struct {
	ServiceURL           string `json:"service_url"`
	ServiceApplicationID string `json:"service_application_id"`
	NodeName             string `json:"node_name"`
	NodeGroup            string `json:"node_group"`
	// Nil preserves an existing host override; a supplied empty region selects automatic discovery.
	NodeRegion      *string `json:"node_region,omitempty"`
	GeoIPProvider   *string `json:"geoip_provider,omitempty"`
	IntervalSeconds int     `json:"interval_seconds,omitempty"`
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
		return errors.New("pulse: a private HTTPS service URL is required")
	}
	if config.ServiceApplicationID == "" || strings.TrimSpace(config.NodeName) == "" || len(config.NodeName) > 128 || len(config.NodeGroup) > 128 || strings.ContainsAny(config.NodeName+config.NodeGroup, "\r\n\x00") {
		return errors.New("pulse: invalid monitoring node identity")
	}
	if config.NodeRegion != nil && *config.NodeRegion != "" {
		code := *config.NodeRegion
		region, err := language.ParseRegion(code)
		if len(code) != 2 || err != nil || !region.IsCountry() || region.IsPrivateUse() {
			return errors.New("pulse: node region must be an ISO 3166-1 alpha-2 country code or empty")
		}
	}
	if config.GeoIPProvider != nil {
		switch *config.GeoIPProvider {
		case "", "geojs", "ipinfo", "disabled":
		default:
			return errors.New("pulse: GeoIP provider must be geojs, ipinfo, or disabled")
		}
	}
	if config.IntervalSeconds != 0 && (config.IntervalSeconds < 1 || config.IntervalSeconds > 300) {
		return errors.New("pulse: metrics interval must be between 1 and 300 seconds")
	}
	return nil
}
