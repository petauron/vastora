package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/ipquality"
)

// The upstream entrypoint downloads a mutable script. Override it: both the
// dependency image and script are pinned, and no host files are mounted.
const ipQualityImage = "xykt/ipquality@sha256:26258a197cc03c186d41c26bd871d36ccc6ee269baf97762cfdd2b7d694c8dda"

var errIPQualityCleanupUnconfirmed = errors.New("agent: IP quality container cleanup could not be confirmed")

type cappedIPQualityOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (w *cappedIPQualityOutput) Write(value []byte) (int, error) {
	remaining := w.limit + 1 - w.buffer.Len()
	if remaining > 0 {
		if remaining > len(value) {
			remaining = len(value)
		}
		_, _ = w.buffer.Write(value[:remaining])
	}
	return len(value), nil
}

func readIPQualityOutput(reader io.Reader) ([]byte, error) {
	stdout := cappedIPQualityOutput{limit: ipquality.MaxReportBytes}
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, reader); err != nil {
		return nil, err
	}
	if stdout.buffer.Len() > ipquality.MaxReportBytes {
		return nil, errors.New("ipquality: report too large")
	}
	return stdout.buffer.Bytes(), nil
}

const ipQualityScript = `set -eu
curl --proto '=https' --tlsv1.2 -fsSL --max-time 30 https://raw.githubusercontent.com/xykt/IPQuality/ad222ab16778be2a13a174cd1acbd69fb4cac6b7/ip.sh -o /tmp/upstream.sh
echo 'ffb17dae790341c13023a94c5141775974dd73a3653ca5fba5c4648fc5588402  /tmp/upstream.sh' | sha256sum -c - >/dev/null
sed -e '/^check_mail$/d' -e '/^\[\[ \$2 -eq 4 \]\]&&check_dnsbl /d' -e '/^show_mail \$2$/d' -e '/^countRunTimes$/d' -e '/^show_ad$/d' -e 's/\${rawgithub}main\//\${rawgithub}ad222ab16778be2a13a174cd1acbd69fb4cac6b7\//g' /tmp/upstream.sh > /tmp/check.sh
bash /tmp/check.sh -i "$1" "$2" -E -n -p -f -j > /tmp/report.json || [ "$?" -eq 1 ]
cat /tmp/report.json
if [ "$2" = '-4' ] && curl --proto '=https' --tlsv1.2 --ipv4 --interface "$1" --noproxy '*' --max-time 12 --max-filesize 16384 -fsS https://my.ippure.com/v1/info -o /tmp/ippure.json 2>/dev/null; then
  printf '\n__MERIDIAN_IPPURE__\n'
  cat /tmp/ippure.json
fi
`

func ipQualityContainerOptions(task ipquality.Task) client.ContainerCreateOptions {
	family := "-4"
	if net.ParseIP(task.Address).To4() == nil {
		family = "-6"
	}
	pids := int64(96)
	return client.ContainerCreateOptions{
		Config: &container.Config{Image: ipQualityImage, User: "65534:65534", WorkingDir: "/tmp", Env: []string{"TERM=dumb", "HOME=/tmp"},
			Entrypoint: []string{"timeout", "-s", "KILL", "210", "/bin/sh", "-c", ipQualityScript, "ip-quality"}, Cmd: []string{task.BindAddress, family},
			Labels: map[string]string{"io.vastora.diagnostic": "ip-quality", "io.vastora.application": "meridian"}},
		HostConfig: &container.HostConfig{NetworkMode: "host", AutoRemove: false, ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
			Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,size=32m,mode=1777"}, LogConfig: container.LogConfig{Type: "none"},
			Resources: container.Resources{Memory: 128 * 1024 * 1024, MemorySwap: 128 * 1024 * 1024, NanoCPUs: 500000000, PidsLimit: &pids}},
	}
}

func (e ApplicationExecutor) CheckIPQuality(parent context.Context, task ipquality.Task) (out ipquality.Result, err error) {
	if err := task.Validate(); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Minute)
	defer cancel()
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return ipquality.Result{Error: "docker_unavailable"}, nil
	}
	defer docker.Close()
	if _, err = docker.ImageInspect(ctx, ipQualityImage); err != nil {
		if !errdefs.IsNotFound(err) {
			return ipquality.Result{Error: "docker_unavailable"}, nil
		}
		pull, pullErr := docker.ImagePull(ctx, ipQualityImage, client.ImagePullOptions{})
		if pullErr != nil {
			return ipquality.Result{Error: "download_failed"}, nil
		}
		pullErr = pull.Wait(ctx)
		pull.Close()
		if pullErr != nil {
			return ipquality.Result{Error: "download_failed"}, nil
		}
	}
	options := ipQualityContainerOptions(task)
	options.Name = "vastora-ip-quality-" + rand.Text()
	cleanupID := options.Name
	// A known unique name also permits exact cleanup if Docker accepted Create
	// but the response was lost before we received the container ID.
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
		defer done()
		if cleanupErr := cleanupIPQualityContainer(cleanup, docker, cleanupID); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	created, err := docker.ContainerCreate(ctx, options)
	if err != nil {
		return ipquality.Result{Error: "docker_unavailable"}, nil
	}
	cleanupID = created.ID
	stream, err := docker.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{Stdout: true, Stream: true})
	if err != nil {
		return ipquality.Result{Error: "detection_failed"}, nil
	}
	defer stream.Close()
	stopClose := context.AfterFunc(ctx, stream.Close)
	defer stopClose()
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	// Moby's result channel is unbuffered. Always consume its single response,
	// even when output parsing or container startup fails before we await it.
	exited := make(chan container.WaitResponse, 1)
	go func() {
		select {
		case result := <-wait.Result:
			exited <- result
		case <-wait.Error:
			exited <- container.WaitResponse{StatusCode: -1}
		}
	}()
	if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return ipquality.Result{Error: "detection_failed"}, nil
	}
	stdout, readErr := readIPQualityOutput(stream.Reader)
	if ctx.Err() != nil {
		return ipquality.Result{Error: "timeout"}, nil
	}
	if readErr != nil {
		return ipquality.Result{Error: "invalid_report"}, nil
	}
	select {
	case <-ctx.Done():
		return ipquality.Result{Error: "timeout"}, nil
	case result := <-exited:
		// Upstream ends with a conditional IPv6 check: -4 can exit 1 after
		// emitting valid JSON. A complete, IP-bound report is still required.
		if result.Error != nil || (result.StatusCode != 0 && result.StatusCode != 1) {
			return ipquality.Result{Error: "detection_failed"}, nil
		}
	}
	parts := bytes.SplitN(stdout, []byte("\n__MERIDIAN_IPPURE__\n"), 2)
	report, parseErr := ipquality.Parse(parts[0], task.Address)
	if parseErr != nil {
		return ipquality.Result{Error: parseErr.Error()}, nil
	}
	pure := missingIPPure(task.Address, time.Now())
	if len(parts) == 2 {
		pure = parseIPPure(parts[1], task.Address, time.Now())
	}
	report.IPPure = &pure
	return ipquality.Result{Report: &report}, nil
}

type ipQualityCleanupEngine interface {
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}

func cleanupIPQualityContainer(ctx context.Context, docker ipQualityCleanupEngine, containerID string) error {
	_, err := docker.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
	if err == nil || errdefs.IsNotFound(err) {
		return nil
	}
	if !errdefs.IsConflict(err) {
		return errIPQualityCleanupUnconfirmed
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, inspectErr := docker.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(inspectErr) {
			return nil
		}
		if inspectErr != nil {
			return errIPQualityCleanupUnconfirmed
		}
		select {
		case <-ctx.Done():
			return errIPQualityCleanupUnconfirmed
		case <-ticker.C:
		}
	}
}
