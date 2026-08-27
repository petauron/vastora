package agent

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const HostInstallStateName = "host-install.env"

type HostInstallState struct {
	TailscaleOwnership string
	TailscaleEnrolled  bool
}

func ReadHostInstallState(dataDir string) (HostInstallState, error) {
	state := HostInstallState{TailscaleOwnership: "external"}
	file, err := os.Open(filepath.Join(dataDir, HostInstallStateName))
	if errors.Is(err, os.ErrNotExist) {
		// Old installations have no trustworthy provenance. Preserve host
		// packages rather than guessing that Vastora owns them.
		return state, nil
	}
	if err != nil {
		return HostInstallState{}, fmt.Errorf("agent: read host install state: %w", err)
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return HostInstallState{}, fmt.Errorf("agent: read host install state: %w", err)
	}
	if values["HOST_STATE_VERSION"] != "1" {
		return HostInstallState{}, errors.New("agent: unsupported host install state")
	}
	ownership := values["TAILSCALE_OWNERSHIP"]
	if ownership != "managed" && ownership != "external" && ownership != "none" {
		return HostInstallState{}, errors.New("agent: invalid Tailscale ownership state")
	}
	state.TailscaleOwnership = ownership
	state.TailscaleEnrolled = values["TAILSCALE_ENROLLED"] == "1"
	return state, nil
}
