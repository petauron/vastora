package main

import "testing"

func TestVerificationRequiresExactApprovedRelease(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--origin", "https://example.invalid/catalog", "--root", "root.json"},
		{"--origin", "https://example.invalid/catalog", "--root", "root.json", "--revision", "1"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("accepted incomplete verifier arguments: %v", args)
		}
	}
}
