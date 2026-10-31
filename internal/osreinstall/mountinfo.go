package osreinstall

import (
	"encoding/json"
	"errors"
	"strings"
)

// BindCriticalMounts cross-checks the kernel mount table against the block
// tree using device numbers; missing or ambiguous root/boot devices fail closed.
func BindCriticalMounts(blocksJSON, mountInfo []byte) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, errors.New("reinstall: system mount inventory requires review") }
	if len(blocksJSON) == 0 || len(blocksJSON) > 2<<20 || len(mountInfo) == 0 || len(mountInfo) > 2<<20 {
		return bad()
	}
	var blocks struct {
		Blockdevices []blockDevice `json:"blockdevices"`
	}
	if json.Unmarshal(blocksJSON, &blocks) != nil || len(blocks.Blockdevices) == 0 {
		return bad()
	}
	critical := map[string]string{}
	for _, line := range strings.Split(string(mountInfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		target := fields[4]
		if target != "/" && target != "/boot" && target != "/boot/efi" {
			continue
		}
		if _, exists := critical[target]; exists || !deviceIDPattern.MatchString(fields[2]) {
			return bad()
		}
		critical[target] = fields[2]
	}
	if _, ok := critical["/"]; !ok {
		return bad()
	}
	matches := map[string]int{}
	var visit func(*blockDevice)
	visit = func(d *blockDevice) {
		d.Mountpoints = nil
		for target, device := range critical {
			if d.MajorMinor == device {
				d.Mountpoints = append(d.Mountpoints, target)
				matches[target]++
			}
		}
		for i := range d.Children {
			visit(&d.Children[i])
		}
	}
	for i := range blocks.Blockdevices {
		visit(&blocks.Blockdevices[i])
	}
	for target := range critical {
		if matches[target] != 1 {
			return bad()
		}
	}
	result, err := json.Marshal(blocks)
	if err != nil {
		return bad()
	}
	return result, nil
}
