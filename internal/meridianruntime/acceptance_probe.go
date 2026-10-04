package meridianruntime

import (
	"context"
	"errors"
	"github.com/petauron/vastora/internal/landing"
	"net/netip"
)

// ProbeAcceptance reuses the application's HTTPS egress check through a
// separately prepared real protocol client's local SOCKS listener. The caller
// must bind the result to its recovery operation, exact client configuration
// and replacement machine identity. This function does not release recovery.
func ProbeAcceptance(ctx context.Context, socksAddress, expectedEgress string) error {
	expected, err := netip.ParseAddr(expectedEgress)
	if err != nil || expected.Zone() != "" || !landing.PublicIP(expected) {
		return errors.New("acceptance: invalid expected egress")
	}
	exit, err := (landing.Probe{TCPOnly: true}).CheckClientTCP(ctx, socksAddress)
	if err != nil {
		return errors.New("acceptance: authenticated request failed")
	}
	observed, err := netip.ParseAddr(exit)
	if err != nil || observed.Unmap() != expected.Unmap() {
		return errors.New("acceptance: observed egress mismatch")
	}
	return nil
}
