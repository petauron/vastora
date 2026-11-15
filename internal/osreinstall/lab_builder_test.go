package osreinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLabVerifiedInstallerBuilder(t *testing.T) {
	sourceDir := os.Getenv("VASTORA_REINSTALL_LAB_SOURCE_DIR")
	if sourceDir == "" {
		t.Skip("opt-in acceptance requires locally verified pinned installer sources")
	}
	source := map[string][]byte{}
	for name := range reinstallSourceHashes {
		b, e := os.ReadFile(filepath.Join(sourceDir, name))
		if e != nil {
			t.Fatal(e)
		}
		source[name] = b
	}
	spec := InstallerSpec{StageDir: "/var/lib/vastora/os-reinstall/test-task", Architecture: "amd64", BootMode: "bios", Username: "debian", SSHPort: 22, TaskID: "test-task", ExecutionID: "test-execution", SSHHostKeySHA256: "SHA256:" + strings.Repeat("a", 43), SudoPolicy: "passwordless_all", Hostname: "reinstall-lab", AgentID: "test-agent", IdentitySHA256: "sha256:" + strings.Repeat("a", 64), Disk: Disk{Path: "/dev/sda", Bytes: 8 << 30, MajorMinor: "8:0", PartitionTableID: "00000001"}, Network: StaticNetworkPlan{Interface: "eth0", MAC: "02:00:00:00:00:01", Address: "192.0.2.10/24", Gateway: "192.0.2.1", DNS: []string{"192.0.2.1"}, Search: []string{"example.test"}, Resolver: []byte("nameserver 192.0.2.1\nsearch example.test\n")}}
	out, err := BuildInstallerScripts(source, spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out.Preseed), "late_command string true; \\\\") {
		t.Fatal("preseed uses doubled continuation")
	}
	if !strings.Contains(string(out.Preseed), "chmod 0644 /target/etc/resolv.conf") || !strings.Contains(string(out.Installer), "umask 077") {
		t.Fatal("unsafe modes")
	}
	if !strings.Contains(string(out.Preseed), "blkid -s PTUUID") || !strings.Contains(string(out.Preseed), "network.interfaces") || !strings.Contains(string(out.Interfaces), "dns-search example.test") {
		t.Fatal("missing disk or network restoration")
	}
	spec.Network.Address = "192.0.2.10/32"
	spec.Network.Gateway = "192.0.2.1"
	spec.Network.GatewayOnLink = true
	spec.Network.IPv6 = []string{"2001:db8::10/128"}
	spec.Network.Gateway6 = "2001:db8::1"
	spec.Network.Gateway6OnLink = true
	dual, err := BuildInstallerScripts(source, spec)
	if err != nil {
		t.Fatal(err)
	}
	command := "echo \"'/initrd-network.sh' '02:00:00:00:00:01' '192.0.2.10/32' '192.0.2.1' '' '' 'false' ''\""
	if !strings.Contains(string(dual.Installer), command) || !strings.Contains(string(dual.Interfaces), "2001:db8::10/128") {
		t.Fatal("installer must use reviewed primary IPv4 while restoring static IPv6")
	}
	spec.SudoPolicy = "none"
	noSudo, err := BuildInstallerScripts(source, spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(noSudo.Preseed), "NOPASSWD:ALL") {
		t.Fatal("unexpected sudo grant")
	}
	spec.SudoPolicy = ""
	if _, err := BuildInstallerScripts(source, spec); err == nil {
		t.Fatal("unspecified sudo policy accepted")
	}
	spec.SudoPolicy = "none"
	source["trans.sh"] = append(source["trans.sh"], '\n')
	if _, err := BuildInstallerScripts(source, spec); err == nil {
		t.Fatal("tampered source accepted")
	}
}
