package main

import (
	"strings"
	"testing"
)

func TestOpenRCTailscaleSectionsPreserveLocalSettingsAndAreIdempotent(t *testing.T) {
	raw := "# administrator setting\nextra=keep\n"
	first, err := renderOpenRCTailscaleSection(raw, "PRIVACY", openRCPrivacy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderOpenRCTailscaleSection(first, "ENDPOINT", "port=41641")
	if err != nil {
		t.Fatal(err)
	}
	again, err := renderOpenRCTailscaleSection(second, "PRIVACY", openRCPrivacy)
	if err != nil || again != second {
		t.Fatal("privacy update reordered another managed section", err)
	}
	restored, err := renderOpenRCTailscaleSection(again, "ENDPOINT", "")
	if err != nil || restored != first || !strings.Contains(restored, "extra=keep") {
		t.Fatal("lost unrelated local settings", err)
	}
	if _, err := renderOpenRCTailscaleSection("# BEGIN VASTORA PRIVACY\nbroken", "PRIVACY", openRCPrivacy); err == nil {
		t.Fatal("accepted incomplete ownership section")
	}
}
