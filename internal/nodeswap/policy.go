// Package nodeswap defines the bounded, opt-in host zram and swap task contract.
// Tasks carry no arbitrary sizes, file paths, sysctl keys or shell fragments.
package nodeswap

import (
	"errors"
	"regexp"
)

const (
	Kind       = "node.swap"
	TaskPrefix = "node-swap-"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type SwapDevice struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	SizeBytes int64  `json:"sizeBytes"`
	UsedBytes int64  `json:"usedBytes"`
	Priority  int    `json:"priority"`
}

type ZramDevice struct {
	DisksizeBytes int64  `json:"disksizeBytes"`
	OrigDataBytes int64  `json:"origDataBytes"`
	Algorithm     string `json:"algorithm"`
}

type TargetPolicy struct {
	TierMiB          int64  `json:"tierMiB,omitempty"`
	ZramBytes        int64  `json:"zramBytes,omitempty"`
	ZramAlgorithm    string `json:"zramAlgorithm,omitempty"`
	ZramPriority     int    `json:"zramPriority,omitempty"`
	SwapfileBytes    int64  `json:"swapfileBytes,omitempty"`
	SwapfilePriority int    `json:"swapfilePriority,omitempty"`
	Swappiness       int    `json:"swappiness,omitempty"`
}

type Task struct {
	Action   string `json:"action"`
	Revision string `json:"revision,omitempty"`
}

type View struct {
	State                string       `json:"state"`
	Revision             string       `json:"revision"`
	MemoryTotalBytes     int64        `json:"memoryTotalBytes"`
	MemoryAvailableBytes int64        `json:"memoryAvailableBytes"`
	DiskFreeBytes        int64        `json:"diskFreeBytes"`
	Virtualization       string       `json:"virtualization,omitempty"`
	Systemd              bool         `json:"systemd"`
	Swappiness           int          `json:"swappiness"`
	Swaps                []SwapDevice `json:"swaps"`
	Zram                 *ZramDevice  `json:"zram,omitempty"`
	Target               TargetPolicy `json:"target"`
	CanApply             bool         `json:"canApply,omitempty"`
	CanRestore           bool         `json:"canRestore,omitempty"`
	Reason               string       `json:"reason,omitempty"`
}

func TierForMemoryKB(memTotalKB int64) TargetPolicy {
	const mib int64 = 1 << 20
	switch {
	case memTotalKB >= 512*896 && memTotalKB <= 512*1056:
		return TargetPolicy{
			TierMiB:          512,
			ZramBytes:        256 * mib,
			ZramAlgorithm:    "lz4",
			ZramPriority:     100,
			SwapfileBytes:    512 * mib,
			SwapfilePriority: -2,
			Swappiness:       60,
		}
	case memTotalKB >= 1024*896 && memTotalKB <= 1024*1056:
		return TargetPolicy{
			TierMiB:          1024,
			ZramBytes:        256 * mib,
			ZramAlgorithm:    "lz4",
			ZramPriority:     100,
			SwapfileBytes:    512 * mib,
			SwapfilePriority: -2,
			Swappiness:       60,
		}
	case memTotalKB >= 2048*896 && memTotalKB <= 2048*1056:
		return TargetPolicy{
			TierMiB:          2048,
			SwapfileBytes:    512 * mib,
			SwapfilePriority: -2,
			Swappiness:       10,
		}
	default:
		return TargetPolicy{}
	}
}

func (p TargetPolicy) Valid() bool {
	if p == (TargetPolicy{}) {
		return true
	}
	expected := TierForMemoryKB(p.TierMiB * 1024)
	return expected.TierMiB != 0 && p == expected
}

func (t Task) Validate() error {
	switch t.Action {
	case "inspect":
		if t.Revision != "" {
			return errors.New("swap: inspect does not accept a revision")
		}
		return nil
	case "apply", "restore":
		if !digestPattern.MatchString(t.Revision) {
			return errors.New("swap: exact inspected revision is required")
		}
		return nil
	default:
		return errors.New("swap: unsupported action")
	}
}

func (v View) Validate() error {
	switch v.State {
	case "eligible", "applied", "read_only", "action_required":
	default:
		return errors.New("swap: invalid state")
	}
	if !digestPattern.MatchString(v.Revision) {
		return errors.New("swap: invalid revision")
	}
	if v.MemoryTotalBytes <= 0 || v.MemoryAvailableBytes < 0 || v.DiskFreeBytes < 0 {
		return errors.New("swap: invalid host capacity")
	}
	if v.Swappiness < 0 || v.Swappiness > 200 {
		return errors.New("swap: invalid swappiness")
	}
	if len(v.Virtualization) > 64 || len(v.Reason) > 256 || len(v.Swaps) > 32 {
		return errors.New("swap: oversized view field")
	}
	for _, dev := range v.Swaps {
		if dev.Name == "" || len(dev.Name) > 128 || len(dev.Type) > 32 || dev.SizeBytes < 0 || dev.UsedBytes < 0 {
			return errors.New("swap: invalid swap device")
		}
	}
	if v.Zram != nil {
		if v.Zram.DisksizeBytes < 0 || v.Zram.OrigDataBytes < 0 || len(v.Zram.Algorithm) > 32 {
			return errors.New("swap: invalid zram observation")
		}
	}
	if !v.Target.Valid() {
		return errors.New("swap: invalid target policy")
	}
	if v.CanApply && v.CanRestore {
		return errors.New("swap: apply and restore are mutually exclusive")
	}
	if v.CanApply && (v.State != "eligible" || v.Target.TierMiB == 0 || !v.Systemd) {
		return errors.New("swap: apply requires an eligible systemd tier")
	}
	if v.CanRestore && (v.State != "applied" && v.State != "action_required") {
		return errors.New("swap: restore requires a managed state")
	}
	return nil
}

func (v View) ValidateFor(t Task) error {
	if err := v.Validate(); err != nil {
		return err
	}
	switch t.Action {
	case "inspect":
		return nil
	case "apply":
		if v.State != "applied" || v.CanApply {
			return errors.New("swap: apply did not reach applied state")
		}
	case "restore":
		if v.State == "applied" || v.CanRestore {
			return errors.New("swap: restore did not clear managed state")
		}
	default:
		return errors.New("swap: unsupported action")
	}
	return nil
}
