package networking

import (
	"net"
	"testing"
)

func TestPublicAddressHelperRequiresExactHTTPSURL(t *testing.T) {
	for _, endpoint := range []string{
		"http://helper.example.com/network/public-address",
		"https://user:secret@helper.example.com/network/public-address",
		"https://helper.example.com/network/public-address?target=internal",
		"https://helper.example.com/other",
	} {
		if _, _, err := PublicAddressHTTPClient(endpoint, false); err == nil {
			t.Fatalf("invalid helper endpoint %q was accepted", endpoint)
		}
	}
}

func TestPublicAddressHelperRejectsMetadataEvenWhenPrivateIsAllowed(t *testing.T) {
	for _, raw := range []string{"169.254.169.254", "100.100.100.200"} {
		if err := validatePublicHelperAddress(net.ParseIP(raw), true); err == nil {
			t.Fatalf("metadata address %s was accepted", raw)
		}
	}
}

func TestPublicAddressHelperRequiresExplicitTrustForReservedAddresses(t *testing.T) {
	for _, raw := range []string{"192.0.2.1", "198.51.100.1", "2001:db8::1"} {
		if err := validatePublicHelperAddress(net.ParseIP(raw), false); err == nil {
			t.Fatalf("reserved address %s was accepted by default", raw)
		}
		if err := validatePublicHelperAddress(net.ParseIP(raw), true); err != nil {
			t.Fatalf("explicitly trusted reserved address %s was rejected: %v", raw, err)
		}
	}
}
