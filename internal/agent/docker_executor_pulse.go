package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/pulse"
)

const (
	pulseContainer          = "vastora-pulse"
	pulseBootstrapDirectory = "pulse-bootstrap"
	pulseSetupTokenPath     = "/" + pulseBootstrapDirectory + "/setup-token"
)

func deployPulse(ctx context.Context, docker *client.Client, task DeploymentTask, bindAddress string) error {
	settings, secrets, err := pulse.DecodeServiceConfig(task.Config, task.Secrets)
	if err != nil {
		return err
	}
	image, err := pullDeclaredImage(ctx, docker, task, "pulse")
	if err != nil {
		return err
	}
	if err := ensureOwnedApplicationVolumes(ctx, docker, applicationVolumes[pulse.ServiceKey], pulse.ServiceKey, task.ApplicationID); err != nil {
		return err
	}
	inspected, exists, err := inspectOwnedApplicationContainer(ctx, docker, pulseContainer, pulse.ServiceKey, "pulse", task.ApplicationID, anyApplicationDeployment)
	if err != nil {
		return err
	}
	if exists && task.Operation == "upgrade" {
		// Pulse performs forward-only migrations at startup. Take its supported
		// online SQLite backup before replacing the running service; never start
		// an old image against a database touched by the new one.
		if _, err := pulseServiceCLI(ctx, docker, inspected.Container.ID, []string{"backup"}); err != nil {
			return errors.New("agent: Pulse backup failed; upgrade was not started")
		}
	}
	if err := removeOwnedApplicationContainer(ctx, docker, pulseContainer, pulse.ServiceKey, "pulse", task.ApplicationID, anyApplicationDeployment); err != nil {
		return err
	}
	port := network.MustParsePort("8080/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   pulseContainer,
		Config: &container.Config{Image: image, User: "65532:65532", Labels: applicationResourceLabels(pulse.ServiceKey, "pulse", task.ApplicationID, task.ID), ExposedPorts: network.PortSet{port: struct{}{}}, Env: []string{"PULSE_PUBLIC_URL=" + settings.PublicURL, "PULSE_SETUP_TOKEN_FILE=" + pulseSetupTokenPath}},
		HostConfig: &container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			PortBindings:  network.PortMap{port: []network.PortBinding{{HostIP: netip.MustParseAddr(bindAddress), HostPort: "18080"}}},
			Mounts:        []mount.Mount{{Type: mount.TypeVolume, Source: "vastora-pulse-data", Target: "/var/lib/pulse"}},
			SecurityOpt:   []string{"no-new-privileges:true"},
			CapDrop:       []string{"ALL"},
			LogConfig:     container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
		},
	})
	if err != nil {
		return err
	}
	if err := copyPulseSetupToken(ctx, docker, created.ID, secrets.SetupToken); err != nil {
		// This is the newly created, never-started container, not a data volume.
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		return err
	}
	_, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
	return err
}

// Reuse CPA's in-memory archive -> Docker copy pattern. This is a private file,
// not a read-only mount. The root-owned parent prevents path replacement by
// Pulse; only UID 65532 can read the 0400 file. No secret enters Docker Env.
func copyPulseSetupToken(ctx context.Context, docker *client.Client, containerID, token string) error {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, header := range []*tar.Header{
		{Name: pulseBootstrapDirectory + "/", Typeflag: tar.TypeDir, Mode: 0o711, Uid: 0, Gid: 0},
		{Name: pulseBootstrapDirectory + "/setup-token", Typeflag: tar.TypeReg, Mode: 0o400, Uid: 65532, Gid: 65532, Size: int64(len(token))},
	} {
		if err := writer.WriteHeader(header); err != nil {
			return errors.New("agent: archive Pulse setup token failed")
		}
	}
	if _, err := writer.Write([]byte(token)); err != nil {
		return errors.New("agent: archive Pulse setup token failed")
	}
	if err := writer.Close(); err != nil {
		return errors.New("agent: archive Pulse setup token failed")
	}
	// Leave CopyUIDGID false: Docker otherwise replaces all archive ownership
	// with the container user, including the deliberately root-owned parent.
	if _, err := docker.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{DestinationPath: "/", Content: &archive}); err != nil {
		// Never expose daemon errors that may echo request data.
		return errors.New("agent: write private Pulse setup token failed")
	}
	return nil
}

// pulseServiceCLI is only used by fixed Pulse administration operations. Neither an
// HTTP caller nor a catalog may supply an executable or arbitrary arguments.
func pulseServiceCLI(ctx context.Context, docker *client.Client, containerID string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	execution, err := docker.ExecCreate(ctx, containerID, client.ExecCreateOptions{Cmd: append([]string{"/usr/local/bin/pulse-service"}, args...), AttachStdout: true, AttachStderr: true})
	if err != nil {
		return nil, errors.New("agent: Pulse administration could not start")
	}
	attached, err := docker.ExecAttach(ctx, execution.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, errors.New("agent: Pulse administration connection failed")
	}
	defer attached.Close()
	stopClose := context.AfterFunc(ctx, func() { attached.Close() })
	defer stopClose()
	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, io.LimitReader(attached.Reader, 64<<10)); err != nil {
		return nil, errors.New("agent: Pulse administration response failed")
	}
	status, err := docker.ExecInspect(ctx, execution.ID, client.ExecInspectOptions{})
	if err != nil || status.Running || status.ExitCode != 0 {
		return nil, errors.New("agent: Pulse administration failed")
	}
	return stdout.Bytes(), nil
}

func (e ApplicationExecutor) InspectPulse(ctx context.Context, task pulse.InspectionTask) (pulse.InspectionResult, error) {
	result := pulse.InspectionResult{}
	if err := task.Validate(); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return result, err
	}
	defer docker.Close()
	inspected, exists, err := inspectOwnedApplicationContainer(ctx, docker, pulseContainer, pulse.ServiceKey, "pulse", task.ApplicationID, task.DeploymentID)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, errors.New("agent: reviewed Pulse service is unavailable")
	}
	return inspectPulseEnrollments(ctx, docker, inspected.Container.ID, task)
}

func inspectPulseEnrollments(ctx context.Context, docker *client.Client, containerID string, task pulse.InspectionTask) (pulse.InspectionResult, error) {
	result := pulse.InspectionResult{}
	help, err := pulseServiceCLI(ctx, docker, containerID, []string{"--help"})
	if err != nil {
		return result, err
	}
	if !strings.Contains(string(help), "enrollment inspect ID") {
		return result, errors.New("agent: installed Pulse does not support enrollment inspection; upgrade the managed service explicitly")
	}
	for _, id := range task.EnrollmentIDs {
		output, err := pulseServiceCLI(ctx, docker, containerID, []string{"enrollment", "inspect", id})
		if err != nil {
			return pulse.InspectionResult{}, err
		}
		var record pulse.EnrollmentRecord
		decoder := json.NewDecoder(bytes.NewReader(output))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&record) != nil || record.ID != id {
			return pulse.InspectionResult{}, errors.New("agent: invalid Pulse inspection response")
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return pulse.InspectionResult{}, errors.New("agent: invalid Pulse inspection response")
		}
		result.Records = append(result.Records, record)
	}
	if err := result.Validate(task); err != nil {
		return pulse.InspectionResult{}, err
	}
	return result, nil
}

func (e ApplicationExecutor) EnrollPulse(ctx context.Context, task pulse.EnrollmentTask) (pulse.EnrollmentResult, error) {
	if task.ApplicationID == "" || task.DeploymentID == "" {
		return pulse.EnrollmentResult{}, errors.New("agent: invalid Pulse enrollment task")
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return pulse.EnrollmentResult{}, err
	}
	defer docker.Close()
	inspected, exists, err := inspectOwnedApplicationContainer(ctx, docker, pulseContainer, pulse.ServiceKey, "pulse", task.ApplicationID, anyApplicationDeployment)
	if err != nil {
		return pulse.EnrollmentResult{}, err
	}
	if !exists {
		return pulse.EnrollmentResult{}, errors.New("agent: managed Pulse service is unavailable")
	}
	output, err := pulseServiceCLI(ctx, docker, inspected.Container.ID, []string{"enrollment", "create", "--ttl-seconds", "3600"})
	if err != nil {
		return pulse.EnrollmentResult{}, err
	}
	var result pulse.EnrollmentResult
	if json.Unmarshal(output, &result) != nil || result.ID == "" || len(result.Token) < 16 || len(result.Token) > 512 || result.ExpiresAtUnixMS <= time.Now().UnixMilli() {
		return pulse.EnrollmentResult{}, errors.New("agent: invalid Pulse enrollment response")
	}
	return result, nil
}

// A failed invocation may already have replaced the credential. The execution
// journal retains an uncertain outcome; it must never be retried automatically.
func (e ApplicationExecutor) RotatePulse(ctx context.Context, task pulse.RotationTask) (pulse.RotationResult, error) {
	if err := task.Validate(); err != nil {
		return pulse.RotationResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return pulse.RotationResult{}, err
	}
	defer docker.Close()
	inspected, exists, err := inspectOwnedApplicationContainer(ctx, docker, pulseContainer, pulse.ServiceKey, "pulse", task.Inspection.ApplicationID, task.Inspection.DeploymentID)
	if err != nil || !exists {
		return pulse.RotationResult{}, errors.New("agent: reviewed Pulse service is unavailable")
	}
	// Pin every read and the write to this container ID, even if the managed name
	// is reassigned while the operation is running.
	records, err := inspectPulseEnrollments(ctx, docker, inspected.Container.ID, task.Inspection)
	if err != nil {
		return pulse.RotationResult{}, err
	}
	id, err := records.OriginalNodeID(task.Inspection)
	if err != nil || id != task.NodeID {
		return pulse.RotationResult{}, errors.New("agent: original Pulse identity changed before credential rotation")
	}
	output, err := pulseServiceCLI(ctx, docker, inspected.Container.ID, []string{"node", "rotate", task.NodeID})
	if err != nil {
		return pulse.RotationResult{}, uncertainTaskOutcome(errors.New("agent: Pulse credential rotation outcome needs inspection"))
	}
	result := pulse.RotationResult{NodeID: task.NodeID, Token: strings.TrimSpace(string(output))}
	if result.Validate(task) != nil {
		return pulse.RotationResult{}, uncertainTaskOutcome(errors.New("agent: Pulse credential rotation response needs inspection"))
	}
	return result, nil
}
