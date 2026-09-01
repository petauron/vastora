package center

import (
	"strings"
	"testing"
)

func TestRandomDNSLabelUses128BitLowercaseBase32(t *testing.T) {
	label, err := randomDNSLabel(16)
	if err != nil {
		t.Fatal(err)
	}
	if len(label) != 26 || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz234567") != "" {
		t.Fatalf("random DNS label = %q", label)
	}
}
