package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentHeadscaleControlIPv6IsExplicitAndFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	if address, err := readAgentHeadscaleControlIPv6(path); err != nil || address != "" {
		t.Fatal("absent setting changed Center policy", err)
	}
	write := func(raw string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"ipv6Address":"2001:db8::20"}`)
	if address, err := readAgentHeadscaleControlIPv6(path); err != nil || address != "2001:db8::20" {
		t.Fatal("explicit IPv6 not selected", err)
	}
	for _, raw := range []string{`{"ipv6Address":"192.0.2.20"}`, `{"ipv6Address":"::1"}`, `{"ipv6Address":"2001:db8::20","controlUrl":"https://other.example.com"}`, `{"ipv6Address":"2001:db8::20"}{}`} {
		write(raw)
		if _, err := readAgentHeadscaleControlIPv6(path); err == nil {
			t.Fatal("invalid setting silently accepted")
		}
	}
	write(`{"ipv6Address":"2001:db8::20"}`)
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentHeadscaleControlIPv6(path); err == nil {
		t.Fatal("writable policy accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentHeadscaleControlIPv6(link); err == nil {
		t.Fatal("symlink policy accepted")
	}
}
