package agent

import (
	"bytes"
	"errors"
	"strconv"
	"strings"

	"github.com/petauron/vastora/internal/senulun"
)

// Historical paths are audit evidence, not a parallel native installer.
const (
	senulunBinary          = "/opt/vastora/pulse-agent/pulse-agent"
	senulunDigest          = "/opt/vastora/pulse-agent/package.json"
	senulunArchive         = "/opt/vastora/pulse-agent/release.tar.gz"
	senulunEnv             = "/etc/vastora/pulse-agent.env"
	senulunToken           = "/etc/vastora/pulse-enrollment-token"
	senulunRuntimeToken    = "/run/vastora-pulse-agent/enrollment-token"
	senulunUnitPath        = "/etc/systemd/system/vastora-pulse-agent.service"
	senulunUnitName        = "vastora-pulse-agent.service"
	senulunState           = "/var/lib/vastora-pulse-agent"
	senulunCredentialsPath = senulunState + "/agent-credentials.json"
	senulunUser            = "vastora-pulse"
)

func senulunSupportedOS(raw []byte) bool {
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = strings.Trim(value, "\"'")
		}
	}
	return values["ID"] == "debian" && (values["VERSION_ID"] == "12" || values["VERSION_ID"] == "13") || values["ID"] == "ubuntu" && (values["VERSION_ID"] == "24.04" || values["VERSION_ID"] == "26.04")
}

func senulunEnvironment(config senulun.AgentConfig) []byte {
	region, provider := "", senulun.DefaultGeoIPProvider
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
		"\nPULSE_CREDENTIALS_PATH=" + senulunCredentialsPath + "\n")
}

// Retain only the two supported local location choices. Never source the file,
// import credentials, or copy arbitrary environment entries into managed output.
func senulunRetainLocation(config senulun.AgentConfig, environment []byte) (senulun.AgentConfig, error) {
	if len(environment) > 16*1024 {
		return config, errors.New("agent: retained Senulun environment exceeds 16 KiB")
	}
	seen := make(map[string]bool, 2)
	for _, line := range strings.Split(string(environment), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		key = strings.TrimSpace(key)
		// Normalize an already converted file for this one-shot location import.
		// The running collector itself only reads its canonical environment.
		if strings.HasPrefix(key, "SENULUN_") {
			key = "PULSE_" + strings.TrimPrefix(key, "SENULUN_")
		}
		if !ok || key != "PULSE_NODE_REGION" && key != "PULSE_GEOIP_PROVIDER" {
			continue
		}
		if seen[key] {
			return config, errors.New("agent: retained Senulun location setting is duplicated")
		}
		seen[key] = true
		if key == "PULSE_NODE_REGION" && config.NodeRegion != nil || key == "PULSE_GEOIP_PROVIDER" && config.GeoIPProvider != nil {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				return config, errors.New("agent: retained Senulun location setting has invalid quoting")
			}
			value = decoded
		} else if strings.HasPrefix(value, "'") {
			if len(value) < 2 || !strings.HasSuffix(value, "'") || strings.Contains(value[1:len(value)-1], "'") {
				return config, errors.New("agent: retained Senulun location setting has invalid quoting")
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

func senulunUnitOwnedBy(unit []byte, applicationID string) bool {
	return applicationID != "" && !strings.ContainsAny(applicationID, "\r\n") &&
		bytes.HasPrefix(unit, []byte("# Managed by Vastora\n# Application: "+applicationID+"\n"))
}
