package platform

import (
	"errors"
	"strings"
)

const (
	Linux = "linux"
	AMD64 = "amd64"
	ARM64 = "arm64"

	// ApplicationRuntimeGeneration advances only when an Agent upgrade requires
	// installed applications and network components to be reconciled in place.
	ApplicationRuntimeGeneration = 1
)

type Target struct {
	OS           string
	Architecture string
}

func Parse(operatingSystem, architecture string) (Target, error) {
	target := Target{
		OS:           strings.TrimSpace(operatingSystem),
		Architecture: strings.TrimSpace(architecture),
	}
	if target.OS != Linux || target.Architecture != AMD64 && target.Architecture != ARM64 {
		return Target{}, errors.New("unsupported platform; Vastora supports linux/amd64 and linux/arm64")
	}
	return target, nil
}

func (target Target) AgentBinaryName() string {
	return target.OS + "-" + target.Architecture
}
