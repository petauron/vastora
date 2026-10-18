package landing

import (
	"context"
	"errors"
	"regexp"
	"strconv"
)

var nftVersionPattern = regexp.MustCompile(`^nftables v([0-9]+)\.([0-9]+)\.([0-9]+)(?:\s|$)`)

// RequireNFTables checks the JSON table-comment support used to prove ownership.
// nftables before 1.0.8 silently drops these comments on both write and read.
// Check before closing gates or stopping an existing proxy, not after cutover.
func RequireNFTables(ctx context.Context) error {
	output, err := runNFT(ctx, nil, "--version")
	if err != nil {
		return errors.New("landing: cannot check nftables; nftables >= 1.0.8 is required before changing the proxy")
	}
	return checkNFTablesVersion(string(output))
}

func checkNFTablesVersion(output string) error {
	match := nftVersionPattern.FindStringSubmatch(output)
	if len(match) == 4 {
		var version [3]uint64
		for i := range version {
			value, err := strconv.ParseUint(match[i+1], 10, 64)
			if err != nil {
				return errors.New("landing: invalid nftables version")
			}
			version[i] = value
		}
		if version[0] > 1 || version[0] == 1 && (version[1] > 0 || version[2] >= 8) {
			return nil
		}
	}
	return errors.New("landing: nftables >= 1.0.8 is required for JSON firewall ownership; upgrade the node operating system before changing the proxy")
}
