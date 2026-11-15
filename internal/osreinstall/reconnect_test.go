package osreinstall

import (
	"strings"
	"testing"
)

func reconnectFixture(t *testing.T) ReconnectEvidence {
	t.Helper()
	source := exampleEvidence()
	source.Addresses = []byte("[{\"ifname\":\"eth0\",\"address\":\"02:00:00:00:00:01\",\"addr_info\":[{\"family\":\"inet\",\"local\":\"192.0.2.10\",\"prefixlen\":24,\"scope\":\"global\"}]}]")
	source.RoutesV4 = []byte("[{\"dst\":\"default\",\"gateway\":\"192.0.2.1\",\"dev\":\"eth0\"}]")
	source.RoutesV6 = []byte("[]")
	source.ResolvConf = []byte("nameserver 192.0.2.53\nsearch example.test\n")
	before, err := InspectEvidence(source)
	if err != nil {
		t.Fatal(err)
	}
	before.Hostname, before.BootMode = "test-host", "bios"
	before.BootID = "f0a3421b-7a2e-4c47-8f3b-948c3b63424a"
	before.SSHHostKeySHA256 = "SHA256:" + strings.Repeat("a", 43)
	after := before
	after.BootID, after.OSID, after.OSVersion = "07e175ea-1e37-426c-9b50-22f366f06f88", "debian", "13"
	plan, err := ParseStaticNetplan([]byte(testStaticNetplan), source.ResolvConf, before.Network)
	if err != nil {
		t.Fatal(err)
	}
	return ReconnectEvidence{ExpectedAgentID: "agent-1", ReportedAgentID: "agent-1",
		ExpectedIdentityHash: "sha256:" + strings.Repeat("a", 64), ReportedIdentityHash: "sha256:" + strings.Repeat("a", 64),
		Before: before, After: after, BeforeNetwork: plan, AfterNetwork: plan}
}

func TestReconnectRequiresNewBootSameIdentityAndNetwork(t *testing.T) {
	value := reconnectFixture(t)
	if err := VerifyReconnect(value); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ReconnectEvidence){
		"same boot":            func(v *ReconnectEvidence) { v.After.BootID = v.Before.BootID },
		"wrong OS":             func(v *ReconnectEvidence) { v.After.OSVersion = "12" },
		"other agent":          func(v *ReconnectEvidence) { v.ReportedAgentID = "agent-2" },
		"ssh host key changed": func(v *ReconnectEvidence) { v.After.SSHHostKeySHA256 = "SHA256:" + strings.Repeat("b", 43) },
		"other key":            func(v *ReconnectEvidence) { v.ReportedIdentityHash = "sha256:" + strings.Repeat("b", 64) },
		"interface changed":    func(v *ReconnectEvidence) { v.After.Network.Interface = "eth1" },
		"resolver changed":     func(v *ReconnectEvidence) { v.AfterNetwork.Resolver = []byte("nameserver 192.0.2.54\n") },
		"search changed":       func(v *ReconnectEvidence) { v.AfterNetwork.Search = []string{"different.test"} },
		"extra network": func(v *ReconnectEvidence) {
			v.After.Limitations = append([]string{}, v.After.Limitations...)
			v.After.Limitations = append(v.After.Limitations, "additional_network_requires_review")
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := reconnectFixture(t)
			change(&v)
			if VerifyReconnect(v) == nil {
				t.Fatal("unverified reconnect accepted")
			}
		})
	}
}

func TestReadOSReleaseIdentityDoesNotExecuteOrGuess(t *testing.T) {
	if id, version := readOSReleaseIdentity([]byte("ID=debian\nVERSION_ID=\"13\"\n")); id != "debian" || version != "13" {
		t.Fatal("valid Debian identity lost")
	}
	if id, version := readOSReleaseIdentity([]byte("ID=debian\nID=ubuntu\nVERSION_ID=13\n")); id != "" || version != "" {
		t.Fatal("duplicate identity accepted")
	}
	if id, version := readOSReleaseIdentity([]byte("ID=$(echo debian)\nVERSION_ID=13\n")); id != "" || version != "" {
		t.Fatal("shell value accepted")
	}
}
