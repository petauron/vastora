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
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/nodediagnostics"
)

// Multi-architecture, immutable diagnostic image. The network listener binds
// only the managed private peer address and exists for one explicit check.
// The pinned image installs iperf3 mode 0700, owned by UID/GID 1000.
// Run as that unprivileged owner while retaining the container restrictions.
const glivelaIperfImage = "ghcr.io/userdocs/iperf3-static@sha256:c61d33698fd938a1334af93c9b59ee042dad90657df9de827dc5b53d5f8894f2"

// BusyBox timeout must not become namespace PID 1: its watchdog cannot kill
// that process from inside the namespace. Keep a waiting shell as PID 1.
const glivelaIperfDeadlineScript = `timeout -s KILL 235 /bin/sh -c "$1" glivela-iperf "$2" "$3" "${4:-}"; result=$?; exit "$result"`

const glivelaIperfServerScript = `set -eu
umask 077
printf '%s' "$GLIVELA_IPERF_KEY" >/tmp/key.pem
printf 'meridian,%s\n' "$GLIVELA_IPERF_HASH" >/tmp/users.csv
unset GLIVELA_IPERF_KEY GLIVELA_IPERF_HASH
for run in 1 2; do
  timeout -s KILL 115 iperf3 -s -1 --idle-timeout 105 --server-max-duration 12 --rsa-private-key-path /tmp/key.pem --authorized-users-path /tmp/users.csv -4 -B "$1" -p "$2" >/dev/null
done`

const glivelaIperfClientScript = `set -eu
umask 077
printf '%s' "$GLIVELA_IPERF_KEY" >/tmp/key.pem
unset GLIVELA_IPERF_KEY
for direction in upload download; do
  attempt=0
  while :; do
    if [ "$direction" = download ]; then reverse=-R; else reverse=; fi
    if iperf3 -c "$1" -p "$2" -4 -B "$3" --connect-timeout 3000 --username meridian --rsa-public-key-path /tmp/key.pem -J -i 0 -t 10 $reverse >/tmp/sample.json 2>/tmp/sample.err; then break; fi
    if ! grep -qi 'unable to connect to server' /tmp/sample.err /tmp/sample.json; then exit 1; fi
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 60 ]; then exit 1; fi
    sleep 1
  done
  printf '%s\n' '__GLIVELA_IPERF_SAMPLE__'
  cat /tmp/sample.json
done`

func glivelaIperfOptions(task nodediagnostics.Task, server bool) client.ContainerCreateOptions {
	address := task.Link.SourceIP
	script := glivelaIperfClientScript
	cmd := []string{task.Link.LandingIP, strconv.Itoa(task.Link.Port), address}
	if server {
		address = task.Link.LandingIP
		script = glivelaIperfServerScript
		cmd = []string{address, strconv.Itoa(task.Link.Port)}
	}
	env := []string{"HOME=/tmp"}
	if task.LinkAuth != nil {
		env = append(env, "GLIVELA_IPERF_KEY="+task.LinkAuth.KeyPEM)
		if server {
			env = append(env, "GLIVELA_IPERF_HASH="+task.LinkAuth.PasswordHash)
		} else {
			env = append(env, "IPERF3_PASSWORD="+task.LinkAuth.Password)
		}
	}
	pids := int64(32)
	return client.ContainerCreateOptions{
		Config: &container.Config{Image: glivelaIperfImage, User: "1000:1000", WorkingDir: "/tmp", Env: env,
			Entrypoint: []string{"/bin/sh", "-c", glivelaIperfDeadlineScript, "glivela-deadline", script}, Cmd: cmd,
			Labels: map[string]string{"io.vastora.application": "glivela", "io.vastora.diagnostic": "link-bandwidth"}},
		HostConfig: &container.HostConfig{NetworkMode: "host", AutoRemove: false, ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
			Tmpfs: map[string]string{"/tmp": "rw,nosuid,size=2m,mode=1777"}, LogConfig: container.LogConfig{Type: "none"},
			Resources: container.Resources{Memory: 64 * 1024 * 1024, MemorySwap: 64 * 1024 * 1024, NanoCPUs: 1000000000, PidsLimit: &pids}},
	}
}

func (e ApplicationExecutor) runGlivelaIperf(parent context.Context, task nodediagnostics.Task, server bool) (out []byte, err error) {
	kind := nodediagnostics.LinkBandwidthKind
	if server {
		kind = nodediagnostics.LinkServerKind
	}
	if err := task.ValidateLinkIdentity(kind); err != nil {
		return nil, err
	}
	if err := validateGlivelaLinkAuth(task.LinkAuth, server); err != nil {
		return nil, err
	}
	checker := landing.NewLinkChecker()
	defer checker.Close()
	return guardGlivelaLink(parent, checker, *task.Link, server, landing.CheckInterval, func(ctx context.Context) ([]byte, error) {
		return e.runGlivelaIperfContainer(ctx, task, server)
	})
}

func (e ApplicationExecutor) runGlivelaIperfContainer(parent context.Context, task nodediagnostics.Task, server bool) (out []byte, err error) {
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
	if _, err := docker.ImageInspect(ctx, glivelaIperfImage); err != nil {
		if !errdefs.IsNotFound(err) {
			return nil, err
		}
		pull, err := docker.ImagePull(ctx, glivelaIperfImage, client.ImagePullOptions{})
		if err != nil {
			return nil, err
		}
		err = pull.Wait(ctx)
		pull.Close()
		if err != nil {
			return nil, err
		}
	}
	options := glivelaIperfOptions(task, server)
	options.Name = "vastora-glivela-iperf-" + rand.Text()
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
		return nil, errors.New("agent: invalid Glivela iPerf output")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-exited:
		if result.Error != nil || result.StatusCode != 0 {
			return nil, errors.New("agent: Glivela iPerf failed")
		}
	}
	return output.buffer.Bytes(), nil
}

func (e ApplicationExecutor) CheckGlivelaLinkServer(ctx context.Context, task nodediagnostics.Task) (nodediagnostics.Result, error) {
	_, err := e.runGlivelaIperf(ctx, task, true)
	if errors.Is(err, errIPQualityCleanupUnconfirmed) {
		return nodediagnostics.Result{}, err
	}
	var linkFailure glivelaLinkFailure
	if errors.As(err, &linkFailure) {
		return nodediagnostics.Result{Error: string(linkFailure)}, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nodediagnostics.Result{Error: "timeout"}, nil
	}
	if err != nil {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	return nodediagnostics.Result{LinkServerCompleted: true}, nil
}

func (e ApplicationExecutor) CheckGlivelaLinkBandwidth(ctx context.Context, task nodediagnostics.Task) (nodediagnostics.Result, error) {
	raw, err := e.runGlivelaIperf(ctx, task, false)
	if errors.Is(err, errIPQualityCleanupUnconfirmed) {
		return nodediagnostics.Result{}, err
	}
	var linkFailure glivelaLinkFailure
	if errors.As(err, &linkFailure) {
		return nodediagnostics.Result{Error: string(linkFailure)}, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nodediagnostics.Result{Error: "timeout"}, nil
	}
	if err != nil {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	parts := bytes.Split(raw, []byte("__GLIVELA_IPERF_SAMPLE__\n"))
	if len(parts) != 3 || len(bytes.TrimSpace(parts[0])) != 0 {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	var upload, download iperfJSONResult
	if json.Unmarshal(parts[1], &upload) != nil || json.Unmarshal(parts[2], &download) != nil || upload.Error != "" || download.Error != "" {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	u, d := upload.End.SumSent, download.End.SumReceived
	measurement := &nodediagnostics.LinkBandwidthMeasurement{TransportState: "direct", SourceNodeID: task.Link.SourceNodeID, LandingNodeID: task.Link.LandingNodeID,
		UploadMbps: u.BitsPerSecond / 1_000_000, DownloadMbps: d.BitsPerSecond / 1_000_000,
		UploadBytes: u.Bytes, DownloadBytes: d.Bytes, UploadSeconds: u.Seconds, DownloadSeconds: d.Seconds}
	result := nodediagnostics.Result{Link: measurement}
	if result.Validate(nodediagnostics.LinkBandwidthKind) != nil {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	return result, nil
}
