package dantebundle

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseBundleIncludesExecutableAndNotices(t *testing.T) {
	if os.Getenv("VASTORA_DANTE_BUNDLE_INTEGRATION") != "1" {
		t.Skip("release-built artifact required")
	}
	binary, license, notice, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(binary) < 4096 || len(binary) > MaxBinarySize {
		t.Fatal("invalid executable size")
	}
	if !strings.Contains(string(license), "Redistribution and use") || !strings.Contains(string(notice), "Inferno Nettverk") {
		t.Fatal("upstream distribution notices are missing")
	}
}
