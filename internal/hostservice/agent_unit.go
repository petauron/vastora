package hostservice

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var agentUnitRuntime = regexp.MustCompile(`(?m)^ExecStart="/usr/local/bin/vastora" agent serve --data-dir "/var/lib/vastora/agent" --roles "([a-z,]+)" --capabilities "([a-z,]+)"$`)

// ParseRestoredAgentUnit accepts only the current managed unit for the
// supported reinstall paths. It keeps the original role and capability order;
// a custom unit must be reviewed rather than overwritten or silently adopted.
func ParseRestoredAgentUnit(unit []byte) (string, string, error) {
	bad := func() (string, string, error) {
		return "", "", errors.New("reinstall: Agent service differs from the current managed template; review its roles and capabilities")
	}
	if len(unit) == 0 || len(unit) > 16<<10 {
		return bad()
	}
	match := agentUnitRuntime.FindAllStringSubmatch(string(unit), -1)
	if len(match) != 1 {
		return bad()
	}
	roles, capabilities := match[0][1], match[0][2]
	roleSet, capSet := map[string]bool{}, map[string]bool{}
	for _, role := range strings.Split(roles, ",") {
		if (role != "worker" && role != "gateway") || roleSet[role] {
			return bad()
		}
		roleSet[role] = true
	}
	for _, capability := range strings.Split(capabilities, ",") {
		if (capability != "landing" && capability != "docker" && capability != "gateway" && capability != "tunnel") || capSet[capability] {
			return bad()
		}
		capSet[capability] = true
	}
	if capSet["landing"] && (len(capSet) != 1 || len(roleSet) != 1 || !roleSet["worker"]) ||
		capSet["docker"] && !roleSet["worker"] || capSet["gateway"] && !roleSet["gateway"] || capSet["tunnel"] && !roleSet["worker"] {
		return bad()
	}
	if string(unit) != SystemdAgentUnit("/usr/local/bin/vastora", "/var/lib/vastora/agent", roles, capabilities) {
		return bad()
	}
	return roles, capabilities, nil
}

// SystemdAgentUnit is the single source of truth for the managed Agent unit.
// Roles and capabilities are supplied by the existing installation; callers
// must validate them before generating or comparing a unit.
func SystemdAgentUnit(executable, dataDir, roles, capabilities string) string {
	after := "network-online.target docker.service"
	if capabilities == "landing" {
		after = "network-online.target tailscaled.service"
	}
	return `[Unit]
Description=Vastora Agent
After=` + after + `
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + strconv.Quote(executable) + ` agent serve --data-dir ` + strconv.Quote(dataDir) + ` --roles ` + strconv.Quote(roles) + ` --capabilities ` + strconv.Quote(capabilities) + `
Restart=always
RestartSec=5s
TimeoutStopSec=30s
LogRateLimitIntervalSec=30s
LogRateLimitBurst=100

[Install]
WantedBy=multi-user.target
`
}
