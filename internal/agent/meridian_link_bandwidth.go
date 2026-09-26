package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/nodediagnostics"
)

// Multi-architecture, immutable diagnostic image. The network listener binds
// only the managed private peer address and exists for one explicit check.
// The pinned image installs iperf3 mode 0700, owned by UID/GID 1000.
// Run as that unprivileged owner while retaining the container restrictions.
const meridianIperfImage = "ghcr.io/userdocs/iperf3-static@sha256:c61d33698fd938a1334af93c9b59ee042dad90657df9de827dc5b53d5f8894f2"

const meridianIperfServerScript = `set -eu
for run in 1 2; do
  timeout -s KILL 115 iperf3 -s -1 --idle-timeout 105 -4 -B "$1" -p "$2" >/dev/null
done`

const meridianIperfClientScript = `set -eu
for direction in upload download; do
  attempt=0
  while :; do
    if [ "$direction" = download ]; then reverse=-R; else reverse=; fi
    if iperf3 -c "$1" -p "$2" -4 -B "$3" --connect-timeout 3000 -J -i 0 -t 10 $reverse >/tmp/sample.json 2>/tmp/sample.err; then break; fi
    if ! grep -qi 'unable to connect to server' /tmp/sample.err /tmp/sample.json; then exit 1; fi
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 60 ]; then exit 1; fi
    sleep 1
  done
  printf '%s\n' '__MERIDIAN_IPERF_SAMPLE__'
  cat /tmp/sample.json
done`

func meridianIperfOptions(task nodediagnostics.Task, server bool) client.ContainerCreateOptions {
	address := task.Link.SourceIP
	script := meridianIperfClientScript
	cmd := []string{task.Link.LandingIP, strconv.Itoa(task.Link.Port), address}
	if server {
		address = task.Link.LandingIP
		script = meridianIperfServerScript
		cmd = []string{address, strconv.Itoa(task.Link.Port)}
	}
	pids := int64(32)
	return client.ContainerCreateOptions{
		Config: &container.Config{Image: meridianIperfImage, User: "1000:1000", WorkingDir: "/tmp", Env: []string{"HOME=/tmp"},
			Entrypoint: []string{"/bin/sh", "-c", script, "meridian-iperf"}, Cmd: cmd,
			Labels: map[string]string{"io.vastora.application": "meridian", "io.vastora.diagnostic": "link-bandwidth"}},
		HostConfig: &container.HostConfig{NetworkMode: "host", AutoRemove: false, ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
			Tmpfs: map[string]string{"/tmp": "rw,nosuid,size=2m,mode=1777"}, LogConfig: container.LogConfig{Type: "none"},
			Resources: container.Resources{Memory: 64 * 1024 * 1024, MemorySwap: 64 * 1024 * 1024, NanoCPUs: 1000000000, PidsLimit: &pids}},
	}
}

func (e ApplicationExecutor) runMeridianIperf(parent context.Context, task nodediagnostics.Task, server bool) (out []byte, err error) {
	kind := nodediagnostics.LinkBandwidthKind
	if server {
		kind = nodediagnostics.LinkServerKind
	}
	if err := task.ValidateLinkBandwidth(kind); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Minute)
	defer cancel()
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return nil, err
	}
	defer docker.Close()
	if _, err := docker.ImageInspect(ctx, meridianIperfImage); err != nil {
		if !errdefs.IsNotFound(err) {
			return nil, err
		}
		pull, err := docker.ImagePull(ctx, meridianIperfImage, client.ImagePullOptions{})
		if err != nil {
			return nil, err
		}
		err = pull.Wait(ctx)
		pull.Close()
		if err != nil {
			return nil, err
		}
	}
	options := meridianIperfOptions(task, server)
	options.Name = "vastora-meridian-iperf-" + rand.Text()
	cleanupID := options.Name
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
		defer done()
		if cleanupErr := cleanupIPQualityContainer(cleanup, docker, cleanupID); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	created, err := docker.ContainerCreate(ctx, options)
	if err != nil {
		return nil, err
	}
	cleanupID = created.ID
	stream, err := docker.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{Stdout: true, Stream: true})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	stopClose := context.AfterFunc(ctx, stream.Close)
	defer stopClose()
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	exited := make(chan container.WaitResponse, 1)
	go func() {
		select {
		case result := <-wait.Result:
			exited <- result
		case <-wait.Error:
			exited <- container.WaitResponse{StatusCode: -1}
		}
	}()
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return nil, err
	}
	output := cappedIPQualityOutput{limit: nodediagnostics.MaxResultBytes}
	_, readErr := stdcopy.StdCopy(&output, io.Discard, stream.Reader)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if readErr != nil || output.buffer.Len() > nodediagnostics.MaxResultBytes {
		return nil, errors.New("agent: invalid Meridian iPerf output")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-exited:
		if result.Error != nil || result.StatusCode != 0 {
			return nil, errors.New("agent: Meridian iPerf failed")
		}
	}
	return output.buffer.Bytes(), nil
}

func (e ApplicationExecutor) CheckMeridianLinkServer(ctx context.Context, task nodediagnostics.Task) (nodediagnostics.Result, error) {
	_, err := e.runMeridianIperf(ctx, task, true)
	if errors.Is(err, errIPQualityCleanupUnconfirmed) {
		return nodediagnostics.Result{}, err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nodediagnostics.Result{Error: "timeout"}, nil
	}
	if err != nil {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	return nodediagnostics.Result{LinkServerCompleted: true}, nil
}

func (e ApplicationExecutor) CheckMeridianLinkBandwidth(ctx context.Context, task nodediagnostics.Task) (nodediagnostics.Result, error) {
	raw, err := e.runMeridianIperf(ctx, task, false)
	if errors.Is(err, errIPQualityCleanupUnconfirmed) {
		return nodediagnostics.Result{}, err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nodediagnostics.Result{Error: "timeout"}, nil
	}
	if err != nil {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	parts := bytes.Split(raw, []byte("__MERIDIAN_IPERF_SAMPLE__\n"))
	if len(parts) != 3 || len(bytes.TrimSpace(parts[0])) != 0 {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	var upload, download iperfJSONResult
	if json.Unmarshal(parts[1], &upload) != nil || json.Unmarshal(parts[2], &download) != nil || upload.Error != "" || download.Error != "" {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	u, d := upload.End.SumSent, download.End.SumReceived
	measurement := &nodediagnostics.LinkBandwidthMeasurement{SourceNodeID: task.Link.SourceNodeID, LandingNodeID: task.Link.LandingNodeID,
		UploadMbps: u.BitsPerSecond / 1_000_000, DownloadMbps: d.BitsPerSecond / 1_000_000,
		UploadBytes: u.Bytes, DownloadBytes: d.Bytes, UploadSeconds: u.Seconds, DownloadSeconds: d.Seconds}
	result := nodediagnostics.Result{Link: measurement}
	if result.Validate(nodediagnostics.LinkBandwidthKind) != nil {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	return result, nil
}
