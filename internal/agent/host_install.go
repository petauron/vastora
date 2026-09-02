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
	path := filepath.Join(dataDir, HostInstallStateName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		// Missing provenance cannot establish ownership. Preserve host
		// packages rather than guessing that Vastora installed them.
		return state, nil
	}
	if err != nil {
		return HostInstallState{}, fmt.Errorf("agent: inspect host install state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return HostInstallState{}, errors.New("agent: host install state is not a protected regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return HostInstallState{}, fmt.Errorf("agent: read host install state: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return HostInstallState{}, fmt.Errorf("agent: inspect opened host install state: %w", err)
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 {
		return HostInstallState{}, errors.New("agent: host install state changed while opening")
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || (key != "HOST_STATE_VERSION" && key != "TAILSCALE_OWNERSHIP" && key != "TAILSCALE_ENROLLED") {
			return HostInstallState{}, errors.New("agent: invalid host install state entry")
		}
		if _, exists := values[key]; exists {
			return HostInstallState{}, errors.New("agent: duplicate host install state entry")
		}
		values[key] = value
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
	if values["TAILSCALE_ENROLLED"] != "0" && values["TAILSCALE_ENROLLED"] != "1" {
		return HostInstallState{}, errors.New("agent: invalid Tailscale enrollment state")
	}
	state.TailscaleOwnership = ownership
	state.TailscaleEnrolled = values["TAILSCALE_ENROLLED"] == "1"
	return state, nil
}
