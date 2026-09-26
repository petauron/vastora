package landing

import (
	"errors"
	"strings"
)

// EgressAddress is assigned to the host, not an externally observed NAT address.
// It is a selection hint; the executor checks local ownership again at apply time.
type EgressAddress struct {
	Address   string `json:"address"`
	Interface string `json:"interface"`
}

func ValidateEgressAddresses(addresses []EgressAddress) error {
	if len(addresses) > 128 {
		return errors.New("landing: too many egress addresses")
	}
	seen := map[string]bool{}
	for _, candidate := range addresses {
		if !ValidEgressIP(candidate.Address) || candidate.Interface == "" || len(candidate.Interface) > 64 || strings.ContainsAny(candidate.Interface, "\x00\r\n") || seen[candidate.Address] {
			return errors.New("landing: invalid or duplicate egress address")
		}
		seen[candidate.Address] = true
	}
	return nil
}
