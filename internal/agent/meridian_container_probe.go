package agent

import (
	"context"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

// VerifyMeridianClientInContainer runs the HTTPS verifier in the temporary
// client's network namespace. A bridged Deployer cannot use host loopback.
// Neither container exposes a port or receives a host mount; only Xray receives
// the subscription secret, over stdin. The helper uses the installed immutable
// Vastora image and returns only an exit status.
func (e ApplicationExecutor) VerifyMeridianClientInContainer(ctx context.Context, spec meridianruntime.AcceptanceClient, expectedExit, runtimeImage string) error {
	address, err := netip.ParseAddr(expectedExit)
	if err != nil || !landing.PublicIP(address) {
		return errors.New("agent: invalid recovery exit expectation")
	}
	repository, digest, pinned := strings.Cut(runtimeImage, "@sha256:")
	decoded, digestErr := hex.DecodeString(digest)
	if !pinned || repository == "" || digestErr != nil || len(decoded) != 32 {
		return errors.New("agent: immutable probe runtime image required")
	}
	return e.runMeridianClient(ctx, spec, xrayWorkerImageReference, func(ctx context.Context, docker *client.Client, id string) error {
		return runMeridianContainerProbe(ctx, docker, id, runtimeImage, expectedExit)
	}, nil)
}

func meridianContainerProbeOptions(name, clientID, image, expectedExit string) client.ContainerCreateOptions {
	pids := int64(32)
	return client.ContainerCreateOptions{Name: name,
		Config:     &container.Config{Image: image, User: "65532:65532", Cmd: []string{"deployer", "meridian-entry-probe", "--expected-exit", expectedExit}, Labels: map[string]string{acceptanceOwnerLabel: name}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("container:" + clientID), ReadonlyRootfs: true, Tmpfs: map[string]string{"/var/lib/vastora": "rw,noexec,nosuid,size=1m"}, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, LogConfig: container.LogConfig{Type: "none"}, Resources: container.Resources{Memory: 128 << 20, MemorySwap: 128 << 20, NanoCPUs: 1000000000, PidsLimit: &pids}},
	}
}

func runMeridianContainerProbe(parent context.Context, docker *client.Client, id, image, expectedExit string) (err error) {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	name := "vastora-entry-probe-" + id
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
		defer done()
		err = errors.Join(err, cleanupMeridianAcceptance(cleanup, docker, name))
	}()
	created, err := docker.ContainerCreate(ctx, meridianContainerProbeOptions(name, id, image, expectedExit))
	if err != nil {
		return errors.New("agent: entry probe creation failed")
	}
	if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return errors.New("agent: entry probe startup failed")
	}
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	// Always drain Moby's unbuffered completion channels, even on cancellation.
	completed := make(chan container.WaitResponse, 1)
	go func() {
		select {
		case result, ok := <-wait.Result:
			if !ok {
				result.StatusCode = -1
			}
			completed <- result
		case <-wait.Error:
			completed <- container.WaitResponse{StatusCode: -1}
		}
	}()
	select {
	case result := <-completed:
		if result.Error != nil || result.StatusCode != 0 {
			return errors.New("agent: authenticated entry probe failed")
		}
		return nil
	case <-ctx.Done():
		return errors.New("agent: entry probe timed out")
	}
}

// RunMeridianEntryProbe is the bounded helper command. Retry only SOCKS startup;
// business HTTPS and exact exit checks execute once, with no direct fallback.
func RunMeridianEntryProbe(parent context.Context, expectedExit string) error {
	address, err := netip.ParseAddr(expectedExit)
	if err != nil || !landing.PublicIP(address) {
		return errors.New("agent: invalid recovery exit expectation")
	}
	ctx, cancel := context.WithTimeout(parent, 35*time.Second)
	defer cancel()
	ready, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if meridianSOCKSReady(ready, "127.0.0.1:1080") {
			return meridianruntime.ProbeAcceptance(ctx, "127.0.0.1:1080", expectedExit)
		}
		select {
		case <-ready.Done():
			return errors.New("agent: entry probe SOCKS readiness timed out")
		case <-ticker.C:
		}
	}
}
