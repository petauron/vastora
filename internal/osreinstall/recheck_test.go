package osreinstall

import (
	"strings"
	"testing"
)

func TestRecheckObservedHostRejectsChangedIdentityDiskAndNetwork(t *testing.T) {
	reviewed, err := InspectEvidence(exampleEvidence())
	if err != nil {
		t.Fatal(err)
	}
	reviewed.Hostname = "test-host"
	reviewed.OSID, reviewed.OSVersion = "debian", "12"
	reviewed.BootID = "f0a3421b-7a2e-4c47-8f3b-948c3b63424a"
	reviewed.BootMode = "bios"
	reviewed.SSHHostKeySHA256 = "SHA256:" + strings.Repeat("a", 43)
	if err := RecheckObservedHost(reviewed, reviewed); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Inspection){
		"boot":      func(i *Inspection) { i.BootID = "07e175ea-1e37-426c-9b50-22f366f06f88" },
		"host key":  func(i *Inspection) { i.SSHHostKeySHA256 = "SHA256:" + strings.Repeat("b", 43) },
		"disk":      func(i *Inspection) { i.Disk.PartitionTableID = "aabbccde-1234-4321-affe-aabbccddeeff" },
		"interface": func(i *Inspection) { i.Network.Interface = "ens19" },
	} {
		t.Run(name, func(t *testing.T) {
			current := reviewed
			change(&current)
			if err := RecheckObservedHost(reviewed, current); err == nil {
				t.Fatal("changed host was accepted")
			}
		})
	}
}
