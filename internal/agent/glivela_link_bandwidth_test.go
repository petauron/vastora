package agent

import (
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/nodediagnostics"
)

func TestGlivelaLinkContainerIsPrivateAndBounded(t *testing.T) {
	task := nodediagnostics.Task{Link: &nodediagnostics.LinkBandwidthTask{SourceNodeID: "source", LandingNodeID: "landing", SourceIP: "100.64.0.8", LandingIP: "100.64.0.9", Port: 34567}}
	for _, server := range []bool{true, false} {
		options := glivelaIperfOptions(task, server)
		if options.HostConfig.NetworkMode != "host" || !options.HostConfig.ReadonlyRootfs || len(options.HostConfig.Binds) != 0 || len(options.HostConfig.Mounts) != 0 || options.HostConfig.Memory != 64*1024*1024 || options.HostConfig.MemorySwap != options.HostConfig.Memory || options.HostConfig.NanoCPUs != 1000000000 || options.Config.User != "1000:1000" {
			t.Fatal("diagnostic container is not constrained")
		}
		if strings.Contains(strings.Join(options.Config.Cmd, " "), "203.0.113") || options.Config.Labels["io.vastora.application"] != "glivela" {
			t.Fatal("unexpected diagnostic target")
		}
		if len(options.Config.Entrypoint) != 5 || options.Config.Entrypoint[0] != "/bin/sh" || options.Config.Entrypoint[2] != glivelaIperfDeadlineScript || !strings.Contains(glivelaIperfDeadlineScript, "; result=$?; exit") {
			t.Fatal("probe can outlive the Agent process without an independent deadline")
		}
		if server && strings.Join(options.Config.Cmd, " ") != "100.64.0.9 34567" {
			t.Fatalf("server did not bind to landing private address: %v", options.Config.Cmd)
		}
	}
	if !strings.Contains(glivelaIperfClientScript, "-t 10") || !strings.Contains(glivelaIperfClientScript, "unable to connect to server' /tmp/sample.err /tmp/sample.json") || strings.Contains(glivelaIperfClientScript, "-n ") || strings.Contains(glivelaIperfClientScript, "-P ") {
		t.Fatal("link bandwidth parameters changed unexpectedly")
	}
}
