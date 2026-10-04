package agent

import (
	"bytes"
	"errors"
	"strconv"
	"strings"

	"github.com/petauron/vastora/internal/pulse"
)

// Historical paths are audit evidence, not a parallel native installer.
const (
	pulseBinary          = "/opt/vastora/pulse-agent/pulse-agent"
	pulseDigest          = "/opt/vastora/pulse-agent/package.json"
	pulseArchive         = "/opt/vastora/pulse-agent/release.tar.gz"
	pulseEnv             = "/etc/vastora/pulse-agent.env"
	pulseToken           = "/etc/vastora/pulse-enrollment-token"
	pulseRuntimeToken    = "/run/vastora-pulse-agent/enrollment-token"
	pulseUnitPath        = "/etc/systemd/system/vastora-pulse-agent.service"
	pulseUnitName        = "vastora-pulse-agent.service"
	pulseState           = "/var/lib/vastora-pulse-agent"
	pulseCredentialsPath = pulseState + "/agent-credentials.json"
	pulseUser            = "vastora-pulse"
)

func pulseSupportedOS(raw []byte) bool {
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = strings.Trim(value, "\"'")
		}
	}
	return values["ID"] == "debian" && (values["VERSION_ID"] == "12" || values["VERSION_ID"] == "13") || values["ID"] == "ubuntu" && (values["VERSION_ID"] == "24.04" || values["VERSION_ID"] == "26.04")
}

func pulseEnvironment(config pulse.AgentConfig) []byte {
	region, provider := "", pulse.DefaultGeoIPProvider
	if config.NodeRegion != nil {
		region = strings.ToUpper(*config.NodeRegion)
	}
	if config.GeoIPProvider != nil && *config.GeoIPProvider != "" {
		provider = *config.GeoIPProvider
	}
	return []byte("PULSE_SERVICE_URL=" + strconv.Quote(config.ServiceURL) +
		"\nPULSE_NODE_NAME=" + strconv.Quote(config.NodeName) +
		"\nPULSE_NODE_GROUP=" + strconv.Quote(config.NodeGroup) +
		"\nPULSE_NODE_REGION=" + strconv.Quote(region) +
		"\nPULSE_GEOIP_PROVIDER=" + strconv.Quote(provider) +
		"\nPULSE_CREDENTIALS_PATH=" + pulseCredentialsPath + "\n")
}

// Retain only the two supported local location choices. Never source the file,
// import credentials, or copy arbitrary environment entries into managed output.
func pulseRetainLocation(config pulse.AgentConfig, environment []byte) (pulse.AgentConfig, error) {
	if len(environment) > 16*1024 {
		return config, errors.New("agent: retained Pulse environment exceeds 16 KiB")
	}
	seen := make(map[string]bool, 2)
	for _, line := range strings.Split(string(environment), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		key = strings.TrimSpace(key)
		if !ok || key != "PULSE_NODE_REGION" && key != "PULSE_GEOIP_PROVIDER" {
			continue
		}
		if seen[key] {
			return config, errors.New("agent: retained Pulse location setting is duplicated")
		}
		seen[key] = true
		if key == "PULSE_NODE_REGION" && config.NodeRegion != nil || key == "PULSE_GEOIP_PROVIDER" && config.GeoIPProvider != nil {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				return config, errors.New("agent: retained Pulse location setting has invalid quoting")
			}
			value = decoded
		} else if strings.HasPrefix(value, "'") {
			if len(value) < 2 || !strings.HasSuffix(value, "'") || strings.Contains(value[1:len(value)-1], "'") {
				return config, errors.New("agent: retained Pulse location setting has invalid quoting")
			}
			value = value[1 : len(value)-1]
		}
		if key == "PULSE_NODE_REGION" {
			config.NodeRegion = &value
		} else {
			config.GeoIPProvider = &value
		}
	}
	return config, config.Validate()
}

func pulseUnitOwnedBy(unit []byte, applicationID string) bool {
	return applicationID != "" && !strings.ContainsAny(applicationID, "\r\n") &&
		bytes.HasPrefix(unit, []byte("# Managed by Vastora\n# Application: "+applicationID+"\n"))
}
