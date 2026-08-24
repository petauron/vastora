package platform

import "testing"

func TestParseSupportedTargets(t *testing.T) {
	for _, architecture := range []string{AMD64, ARM64} {
		target, err := Parse(Linux, architecture)
		if err != nil {
			t.Fatalf("Parse(linux, %s): %v", architecture, err)
		}
		if target.AgentBinaryName() != "linux-"+architecture {
			t.Fatalf("binary name = %q", target.AgentBinaryName())
		}
	}
}

func TestParseRejectsUnsupportedTargets(t *testing.T) {
	for _, input := range [][2]string{{"darwin", ARM64}, {Linux, "386"}, {Linux, "aarch64"}, {"", ""}} {
		if _, err := Parse(input[0], input[1]); err == nil {
			t.Fatalf("Parse(%q, %q) succeeded", input[0], input[1])
		}
	}
}
