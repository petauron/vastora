package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/senulun"
)

const senulunContainer = "vastora-pulse"

// senulunServiceCLI is only used by fixed Pulse administration operations. Neither an
// HTTP caller nor a catalog may supply an executable or arbitrary arguments.
func senulunServiceCLI(ctx context.Context, docker *client.Client, containerID string, args []string, input io.Reader) ([]byte, error) {
	timeout := 30 * time.Second
	if len(args) == 1 && args[0] == "backup" {
		// Copying and checking the metrics database can exceed the short
		// administration deadline. Keep a bounded backup budget and still
		// require successful completion before replacing the service.
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Select the CLI from the inspected installation; never try a second
	// executable after a failure. Older signed installations remain upgradeable.
	installed, err := docker.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil || installed.Container.Config == nil {
		return nil, errors.New("agent: Senulun executable identity unavailable")
	}
	executable, err := senulunServiceExecutable(installed.Container.Config.Entrypoint)
	if err != nil {
		return nil, err
	}
	execution, err := docker.ExecCreate(ctx, containerID, client.ExecCreateOptions{Cmd: append([]string{executable}, args...), AttachStdout: true, AttachStderr: true, AttachStdin: input != nil})
	if err != nil {
		return nil, errors.New("agent: Senulun administration could not start")
	}
	attached, err := docker.ExecAttach(ctx, execution.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, errors.New("agent: Senulun administration connection failed")
	}
	defer attached.Close()
	stopClose := context.AfterFunc(ctx, func() { attached.Close() })
	defer stopClose()
	if input != nil {
		if _, err := io.Copy(attached.Conn, input); err != nil {
			return nil, errors.New("agent: Senulun administration input failed")
		}
		if err := attached.CloseWrite(); err != nil {
			return nil, errors.New("agent: Senulun administration input failed")
		}
	}
	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, io.LimitReader(attached.Reader, 64<<10)); err != nil {
		return nil, errors.New("agent: Senulun administration response failed")
	}
	status, err := docker.ExecInspect(ctx, execution.ID, client.ExecInspectOptions{})
	if err != nil || status.Running || status.ExitCode != 0 {
		return nil, errors.New("agent: Senulun administration failed")
	}
	return stdout.Bytes(), nil
}

func senulunServiceExecutable(entrypoint []string) (string, error) {
	if len(entrypoint) > 0 && (entrypoint[0] == "/usr/local/bin/senulun-service" || entrypoint[0] == "/usr/local/bin/pulse-service") {
		return entrypoint[0], nil
	}
	return "", errors.New("agent: installed Senulun executable is not a recognized signed runtime")
}

func (e ApplicationExecutor) InspectSenulun(ctx context.Context, task senulun.InspectionTask) (senulun.InspectionResult, error) {
	result := senulun.InspectionResult{}
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
	containerID, err := e.senulunServiceContainerID(ctx, docker, task.ApplicationID, task.DeploymentID)
	if err != nil {
		return result, err
	}
	return inspectSenulunEnrollments(ctx, docker, containerID, task)
}

func inspectSenulunEnrollments(ctx context.Context, docker *client.Client, containerID string, task senulun.InspectionTask) (senulun.InspectionResult, error) {
	result := senulun.InspectionResult{}
	help, err := senulunServiceCLI(ctx, docker, containerID, []string{"--help"}, nil)
	if err != nil {
		return result, err
	}
	if !strings.Contains(string(help), "enrollment inspect ID") {
		return result, errors.New("agent: installed Senulun does not support enrollment inspection; upgrade the managed service explicitly")
	}
	for _, id := range task.EnrollmentIDs {
		output, err := senulunServiceCLI(ctx, docker, containerID, []string{"enrollment", "inspect", id}, nil)
		if err != nil {
			return senulun.InspectionResult{}, err
		}
		var record senulun.EnrollmentRecord
		decoder := json.NewDecoder(bytes.NewReader(output))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&record) != nil || record.ID != id {
			return senulun.InspectionResult{}, errors.New("agent: invalid Senulun inspection response")
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return senulun.InspectionResult{}, errors.New("agent: invalid Senulun inspection response")
		}
		result.Records = append(result.Records, record)
	}
	if err := result.Validate(task); err != nil {
		return senulun.InspectionResult{}, err
	}
	return result, nil
}

func (e ApplicationExecutor) EnrollSenulun(ctx context.Context, task senulun.EnrollmentTask) (senulun.EnrollmentResult, error) {
	if task.ApplicationID == "" || task.DeploymentID == "" {
		return senulun.EnrollmentResult{}, errors.New("agent: invalid Senulun enrollment task")
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return senulun.EnrollmentResult{}, err
	}
	defer docker.Close()
	receipt, err := e.ApplicationResources(task.ApplicationID)
	if err != nil || receipt.AppKey != senulun.ServiceKey || receipt.State != "ready" {
		return senulun.EnrollmentResult{}, errors.New("agent: Senulun package ownership receipt unavailable")
	}
	resource := resourceNamed(receipt, "container", "pulse")
	if resource == nil {
		return senulun.EnrollmentResult{}, errors.New("agent: Senulun container receipt unavailable")
	}
	current, exists, err := inspectOwnedApplicationContainer(ctx, docker, resource.Name, senulun.ServiceKey, resource.Component, task.ApplicationID, anyApplicationDeployment)
	if err != nil {
		return senulun.EnrollmentResult{}, err
	}
	if !exists || current.Container.ID != resource.ID {
		return senulun.EnrollmentResult{}, errors.New("agent: managed Senulun service is unavailable")
	}
	output, err := senulunServiceCLI(ctx, docker, resource.ID, []string{"enrollment", "create", "--ttl-seconds", "3600"}, nil)
	if err != nil {
		return senulun.EnrollmentResult{}, err
	}
	var result senulun.EnrollmentResult
	if json.Unmarshal(output, &result) != nil || result.ID == "" || len(result.Token) < 16 || len(result.Token) > 512 || result.ExpiresAtUnixMS <= time.Now().UnixMilli() {
		return senulun.EnrollmentResult{}, errors.New("agent: invalid Senulun enrollment response")
	}
	return result, nil
}

// A failed invocation may already have replaced the credential. The execution
// journal retains an uncertain outcome; it must never be retried automatically.
func (e ApplicationExecutor) RotateSenulun(ctx context.Context, task senulun.RotationTask) (senulun.RotationResult, error) {
	if err := task.Validate(); err != nil {
		return senulun.RotationResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return senulun.RotationResult{}, err
	}
	defer docker.Close()
	containerID, err := e.senulunServiceContainerID(ctx, docker, task.Inspection.ApplicationID, task.Inspection.DeploymentID)
	if err != nil {
		return senulun.RotationResult{}, errors.New("agent: reviewed Senulun service is unavailable")
	}
	// Pin every read and the write to this container ID, even if the managed name
	// is reassigned while the operation is running.
	records, err := inspectSenulunEnrollments(ctx, docker, containerID, task.Inspection)
	if err != nil {
		return senulun.RotationResult{}, err
	}
	id, err := records.OriginalNodeID(task.Inspection)
	if err != nil || id != task.NodeID {
		return senulun.RotationResult{}, errors.New("agent: original Senulun identity changed before credential rotation")
	}
	output, err := senulunServiceCLI(ctx, docker, containerID, []string{"node", "rotate", task.NodeID}, nil)
	if err != nil {
		return senulun.RotationResult{}, uncertainTaskOutcome(errors.New("agent: Senulun credential rotation outcome needs inspection"))
	}
	result := senulun.RotationResult{NodeID: task.NodeID, Token: strings.TrimSpace(string(output))}
	if result.Validate(task) != nil {
		return senulun.RotationResult{}, uncertainTaskOutcome(errors.New("agent: Senulun credential rotation response needs inspection"))
	}
	return result, nil
}

func (e ApplicationExecutor) InspectSenulunReporting(ctx context.Context, task senulun.ReportingTask) (senulun.ReportingResult, error) {
	var result senulun.ReportingResult
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
	containerID, err := e.senulunServiceContainerID(ctx, docker, task.ApplicationID, task.DeploymentID)
	if err != nil {
		return result, errors.New("agent: reviewed Senulun service is unavailable")
	}
	help, err := senulunServiceCLI(ctx, docker, containerID, []string{"--help"}, nil)
	if err != nil || !strings.Contains(string(help), "node reporting ID") {
		return result, errors.New("agent: Senulun service does not support reporting inspection")
	}
	output, err := senulunServiceCLI(ctx, docker, containerID, []string{"node", "reporting", task.Credentials.NodeID}, strings.NewReader(task.Credentials.Token+"\n"))
	if err != nil {
		return result, errors.New("agent: Senulun reporting inspection failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || result.Validate(task) != nil {
		return senulun.ReportingResult{}, errors.New("agent: invalid Senulun reporting response")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return senulun.ReportingResult{}, errors.New("agent: invalid Senulun reporting response")
	}
	return result, nil
}

// Resolve the reviewed service through its package receipt, including adopted names.
func (e ApplicationExecutor) senulunServiceContainerID(ctx context.Context, docker *client.Client, applicationID, deploymentID string) (string, error) {
	receipt, err := e.ApplicationResources(applicationID)
	if err != nil || receipt.AppKey != senulun.ServiceKey || receipt.State != "ready" {
		return "", errors.New("agent: Senulun package ownership receipt unavailable")
	}
	resource := resourceNamed(receipt, "container", "pulse")
	if resource == nil {
		return "", errors.New("agent: Senulun container receipt unavailable")
	}
	current, exists, err := inspectOwnedApplicationContainer(ctx, docker, resource.Name, senulun.ServiceKey, resource.Component, applicationID, deploymentID)
	if err != nil {
		return "", err
	}
	if !exists || current.Container.ID != resource.ID {
		return "", errors.New("agent: reviewed Senulun service is unavailable")
	}
	return resource.ID, nil
}
