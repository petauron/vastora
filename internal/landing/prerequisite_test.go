package landing

import "testing"

func TestNFTablesJSONOwnershipVersion(t *testing.T) {
	for _, tc := range []struct {
		version   string
		supported bool
	}{
		{"nftables v1.0.6 (Lester Gooch)\n", false},
		{"nftables v1.0.7 (Old Doc Yak)\n", false},
		{"nftables v1.0.8 (Old Doc Yak)\n", true},
		{"nftables v1.1.3 (Commodore Bullmoose)\n", true},
		{"nftables v2.0.0\n", true},
		{"nftables v0.9.9\n", false},
		{"nftables v1.0.8garbage", false},
		{"nftables v18446744073709551616.0.0", false},
		{"", false}, {"error", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			if err := checkNFTablesVersion(tc.version); (err == nil) != tc.supported {
				t.Fatalf("supported=%v, error=%v", tc.supported, err)
			}
		})
	}
}
