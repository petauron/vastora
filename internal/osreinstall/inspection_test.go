package osreinstall

import (
	"encoding/json"
	"strings"
	"testing"
)

func exampleEvidence() Evidence {
	return Evidence{
		Virtualization: "kvm", Architecture: "amd64",
		BlockDevices: []byte(`{"blockdevices":[{"path":"/dev/vda","type":"disk","size":8589934592,"maj:min":"252:0","ptuuid":"aabbccdd-1234-4321-affe-aabbccddeeff","mountpoints":[null],"children":[{"path":"/dev/vda1","type":"part","size":8587837440,"maj:min":"252:1","mountpoints":["/"]}]},{"path":"/dev/vdb","type":"disk","size":4294967296,"maj:min":"252:16","mountpoints":[null]}]}`),
		Addresses:    []byte(`[{"ifname":"ens18","address":"02:00:00:00:00:01","addr_info":[{"family":"inet","local":"192.0.2.10","prefixlen":24,"scope":"global"},{"family":"inet6","local":"2001:db8:1234::10","prefixlen":64,"scope":"global","flags":["dynamic"]},{"family":"inet6","local":"fe80::1234","prefixlen":64,"scope":"link"}]}]`),
		RoutesV4:     []byte(`[{"dst":"default","gateway":"192.0.2.1","dev":"ens18"}]`),
		RoutesV6:     []byte(`[{"dst":"default","gateway":"fe80::1","dev":"ens18"}]`),
		ResolvConf:   []byte("nameserver 192.0.2.53\nnameserver 2001:db8::53\n"),
	}
}

func TestInspectionKeepsSystemDiskAndBothAddressFamilies(t *testing.T) {
	got, err := InspectEvidence(exampleEvidence())
	if err != nil {
		t.Fatal(err)
	}
	if got.Disk.Path != "/dev/vda" || got.Disk.MajorMinor != "252:0" || got.Network.Interface != "ens18" {
		t.Fatalf("wrong disk or interface: %+v", got)
	}
	if len(got.Network.Addresses) != 2 || got.Network.Addresses[0].Prefix != "192.0.2.10/24" ||
		got.Network.Addresses[1].Prefix != "2001:db8:1234::10/64" || !got.Network.Addresses[1].Dynamic ||
		len(got.Network.Gateways) != 2 || len(got.Network.DNS) != 2 {
		t.Fatalf("network evidence was lost: %+v", got.Network)
	}
	serialized, _ := json.Marshal(got)
	if strings.Contains(string(serialized), "/dev/vdb") {
		t.Fatal("unrelated disk became the reinstall target")
	}
}

func TestInspectionRejectsUnsupportedOrAmbiguousTargets(t *testing.T) {
	for name, change := range map[string]func(*Evidence){
		"container": func(e *Evidence) { e.Virtualization = "lxc" },
		"unknown disk": func(e *Evidence) {
			e.BlockDevices = []byte(`{"blockdevices":[{"path":"/dev/sdz","type":"disk","size":8589934592,"maj:min":"8:400","mountpoints":[null]}]}`)
		},
		"multiple default routes": func(e *Evidence) {
			e.RoutesV4 = []byte(`[{"dst":"default","gateway":"192.0.2.1","dev":"ens18"},{"dst":"default","gateway":"192.0.2.2","dev":"ens18"}]`)
		},
		"different interfaces": func(e *Evidence) { e.RoutesV6 = []byte(`[{"dst":"default","gateway":"fe80::1","dev":"ens19"}]`) },
		"missing DNS":          func(e *Evidence) { e.ResolvConf = []byte("# unavailable\n") },
	} {
		t.Run(name, func(t *testing.T) {
			e := exampleEvidence()
			change(&e)
			if _, err := InspectEvidence(e); err == nil {
				t.Fatal("expected refusal")
			}
		})
	}
}

func TestInspectionRecordsLocalDNSAsReviewRequired(t *testing.T) {
	e := exampleEvidence()
	e.ResolvConf = []byte("nameserver 127.0.0.53\n")
	got, err := InspectEvidence(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Limitations) != 2 || !contains(got.Limitations, "local_dns_requires_review") || !contains(got.Limitations, "network_configuration_requires_review") {
		t.Fatalf("limitations: %v", got.Limitations)
	}
}

func TestInspectionDoesNotSilentlyIgnoreSecondNetwork(t *testing.T) {
	e := exampleEvidence()
	e.Addresses = []byte(`[{"ifname":"ens18","address":"02:00:00:00:00:01","addr_info":[{"family":"inet","local":"192.0.2.10","prefixlen":24,"scope":"global"}]},{"ifname":"ens19","address":"02:00:00:00:00:02","addr_info":[{"family":"inet","local":"198.51.100.10","prefixlen":24,"scope":"global"}]}]`)
	got, err := InspectEvidence(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Network.Additional) != 1 || got.Network.Additional[0].Interface != "ens19" {
		t.Fatal("secondary network was ignored")
	}
}
