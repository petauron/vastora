package agent

import (
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/ipquality"
)

func TestIPQualityContainerIsBoundedAndDoesNotMountHost(t *testing.T) {
	options := ipQualityContainerOptions(ipquality.Task{Address: "203.0.113.8", BindAddress: "10.0.0.8"})
	host := options.HostConfig
	if !host.AutoRemove || !host.ReadonlyRootfs || host.Privileged || len(host.Binds) != 0 || len(host.Mounts) != 0 || host.Memory != 128*1024*1024 || host.MemorySwap != host.Memory || host.PidsLimit == nil || *host.PidsLimit > 96 {
		t.Fatalf("unsafe diagnostic container: %#v", host)
	}
	if options.Config.User != "65534:65534" || options.Config.Cmd[0] != "10.0.0.8" || options.Config.Cmd[1] != "-4" || !strings.Contains(options.Config.Image, "@sha256:") {
		t.Fatal("container identity or address not pinned")
	}
	for _, required := range []string{"sha256sum -c", "-E -n -p -f -j", "/^check_mail$/d", "/^countRunTimes$/d", "check_dnsbl", "ad222ab16778be2a13a174cd1acbd69fb4cac6b7"} {
		if !strings.Contains(ipQualityScript, required) {
			t.Fatalf("missing runner restriction %q", required)
		}
	}
	if options.Config.Entrypoint[0] != "timeout" || options.Config.Entrypoint[3] != "210" {
		t.Fatal("container lost independent lifetime limit")
	}
}
