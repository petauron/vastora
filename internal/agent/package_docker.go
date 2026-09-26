package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/catalog/catalog"
)

type packageDockerEngine interface {
	threeXUIContainerEngine
	ImagePull(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error)
}

type DockerPackageBackend struct {
	Docker         packageDockerEngine
	StateDirectory string
	CheckHealth    func(context.Context, string, int, catalog.Service) error
	plans          []packageContainerPlan
	resumeIDs      []string
	networkName    string
}

type packageContainerPlan struct {
	logical  string
	options  client.ContainerCreateOptions
	files    []byte
	services []catalog.Service
}

func runtimeInputs(task DeploymentTask) (map[string]any, map[string]string, error) {
	config, secrets := map[string]any{}, map[string]string{}
	if len(task.Config) > 0 && json.Unmarshal(task.Config, &config) != nil || len(task.Secrets) > 0 && json.Unmarshal(task.Secrets, &secrets) != nil {
		return nil, nil, errors.New("agent: invalid package configuration")
	}
	if config == nil || secrets == nil {
		return nil, nil, errors.New("agent: package configuration must be objects")
	}
	declared := map[string]catalog.ConfigField{}
	for _, field := range task.Manifest.Config {
		declared[field.Key] = field
		if field.Secret {
			if field.Required && strings.TrimSpace(secrets[field.Key]) == "" {
				return nil, nil, fmt.Errorf("agent: required package secret %s is missing", field.Key)
			}
			if _, exists := secrets[field.Key]; !exists {
				secrets[field.Key] = ""
			}
			continue
		}
		if _, present := config[field.Key]; present {
			continue
		}
		if field.Default != nil {
			var value any
			if json.Unmarshal(*field.Default, &value) != nil {
				return nil, nil, errors.New("agent: invalid catalog configuration default")
			}
			config[field.Key] = value
			continue
		}
		if !field.Required {
			switch field.Type {
			case "string":
				config[field.Key] = ""
			case "boolean":
				config[field.Key] = false
			case "integer":
				config[field.Key] = float64(0)
			}
		} else {
			return nil, nil, fmt.Errorf("agent: required package configuration %s is missing", field.Key)
		}
	}
	for key, value := range config {
		field, ok := declared[key]
		if !ok || field.Secret {
			return nil, nil, errors.New("agent: undeclared package configuration")
		}
		valid := false
		switch field.Type {
		case "string":
			_, valid = value.(string)
		case "boolean":
			_, valid = value.(bool)
		case "integer":
			number, ok := value.(float64)
			valid = ok && math.Trunc(number) == number && math.Abs(number) <= 9007199254740991
		}
		if !valid {
			return nil, nil, errors.New("agent: package configuration type mismatch")
		}
	}
	for key := range secrets {
		field, ok := declared[key]
		if !ok || !field.Secret {
			return nil, nil, errors.New("agent: undeclared package secret")
		}
	}
	return config, secrets, nil
}

func runtimeStrings(values []catalog.Value, config map[string]any, secrets map[string]string, runtimeValues ...map[string]string) ([]string, error) {
	result := make([]string, len(values))
	for i, value := range values {
		if value.Secret != "" {
			return nil, errors.New("agent: secrets must use private config files or credentials, not command arguments")
		}
		text, err := catalog.ResolveString(value, config, secrets, runtimeValues...)
		if err != nil {
			return nil, err
		}
		result[i] = text
	}
	return result, nil
}

func resourceNamed(receipt *InstanceResources, kind, logical string) *RuntimeResource {
	for i := range receipt.Resources {
		item := &receipt.Resources[i]
		if item.Kind == kind && item.LogicalName == logical {
			return item
		}
	}
	return nil
}

func runtimeName(receipt *InstanceResources, kind, logical string) string {
	if old := resourceNamed(receipt, kind, logical); old != nil {
		return old.Name
	}
	return packageIdentity(receipt.ApplicationID) + "-" + logical
}

func runtimeService(task DeploymentTask, name string) (catalog.Service, error) {
	for _, service := range task.Manifest.Services {
		if service.Name == name {
			return service, nil
		}
	}
	return catalog.Service{}, errors.New("agent: undeclared runtime service")
}

func compileDockerPackage(task DeploymentTask, receipt *InstanceResources) ([]packageContainerPlan, error) {
	config, secrets, err := runtimeInputs(task)
	if err != nil {
		return nil, err
	}
	address := task.ServiceAddress
	if address == "" {
		address = "127.0.0.1"
	}
	bind, err := netip.ParseAddr(address)
	if err != nil {
		return nil, errors.New("agent: invalid package service address")
	}
	// Dependencies are ordered explicitly; cycles and missing names are rejected
	// again here so backends cannot accidentally rely on manifest array order.
	pending := slices.Clone(task.Manifest.Runtime.Docker.Containers)
	done := map[string]bool{}
	var plans []packageContainerPlan
	for len(pending) > 0 {
		progress := false
		for i := 0; i < len(pending); {
			item := pending[i]
			ready := true
			for _, dependency := range item.DependsOn {
				if !done[dependency] {
					ready = false
				}
			}
			if !ready {
				i++
				continue
			}
			plan := packageContainerPlan{logical: item.Name}
			runtimeValues := map[string]string{"private-address:": "0.0.0.0"}
			if item.HostNetwork {
				runtimeValues["private-address:"] = address
			}
			for _, service := range task.Manifest.Services {
				runtimeValues["service-address:"+service.Name] = net.JoinHostPort(runtimeValues["private-address:"], strconv.Itoa(service.ContainerPort))
			}
			for _, mounted := range item.Mounts {
				if mounted.Storage != "" {
					runtimeValues["state-directory:"+mounted.Storage] = mounted.Target
				}
			}
			for _, file := range item.ConfigFiles {
				runtimeValues["config-file:"+file.Path] = file.Path
			}
			for name := range item.Credentials {
				runtimeValues["credential-file:"+name] = "/run/vastora-credentials/" + name
			}
			image := ""
			for _, declared := range task.Manifest.Images {
				if declared.Name == item.Image {
					image = declared.Reference
				}
			}
			if image == "" {
				return nil, errors.New("agent: undeclared runtime image")
			}
			entrypoint, err := runtimeStrings(item.Command, config, secrets, runtimeValues)
			if err != nil {
				return nil, err
			}
			arguments, err := runtimeStrings(item.Arguments, config, secrets, runtimeValues)
			if err != nil {
				return nil, err
			}
			user := item.User
			if user == "" {
				user = "65532:65532"
			}
			var environment []string
			for key, value := range item.Environment {
				text, err := catalog.ResolveString(value, config, secrets, runtimeValues)
				if err != nil {
					return nil, err
				}
				environment = append(environment, key+"="+text)
			}
			sort.Strings(environment)
			options := client.ContainerCreateOptions{Name: runtimeName(receipt, "container", item.Name), Config: &container.Config{Image: image, User: user, Entrypoint: entrypoint, Cmd: arguments, Env: environment, Labels: applicationResourceLabels(task.AppKey, item.Name, task.ApplicationID, task.ID), ExposedPorts: network.PortSet{}}, HostConfig: &container.HostConfig{RestartPolicy: container.RestartPolicy{Name: "unless-stopped"}, SecurityOpt: []string{"no-new-privileges:true"}, CapDrop: []string{"ALL"}, PortBindings: network.PortMap{}, LogConfig: container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}}}}
			if item.HostNetwork {
				options.HostConfig.NetworkMode = "host"
			}
			for _, device := range item.Devices {
				options.HostConfig.Devices = append(options.HostConfig.Devices, container.DeviceMapping{PathOnHost: device, PathInContainer: device, CgroupPermissions: "rwm"})
			}
			if item.Limits != nil {
				options.HostConfig.Memory = item.Limits.MemoryBytes
				options.HostConfig.NanoCPUs = item.Limits.CPUMillis * 1000000
				if item.Limits.Pids > 0 {
					n := item.Limits.Pids
					options.HostConfig.PidsLimit = &n
				}
			}
			for _, mounted := range item.Mounts {
				m := mount.Mount{Target: mounted.Target, ReadOnly: mounted.ReadOnly}
				if mounted.Storage != "" {
					m.Type, m.Source = mount.TypeVolume, runtimeName(receipt, "volume", mounted.Storage)
				} else {
					m.Type, m.Source = mount.TypeBind, mounted.HostPath
				}
				options.HostConfig.Mounts = append(options.HostConfig.Mounts, m)
			}
			for _, exposed := range item.Ports {
				service, err := runtimeService(task, exposed.Service)
				if err != nil {
					return nil, err
				}
				hostPort, err := serviceHostPort(task.Config, service)
				if err != nil {
					return nil, err
				}
				protocol := exposed.Protocol
				if protocol == "" {
					protocol = "tcp"
				}
				port, err := network.ParsePort(strconv.Itoa(service.ContainerPort) + "/" + protocol)
				if err != nil {
					return nil, err
				}
				options.Config.ExposedPorts[port] = struct{}{}
				options.HostConfig.PortBindings[port] = []network.PortBinding{{HostIP: bind, HostPort: strconv.Itoa(hostPort)}}
				plan.services = append(plan.services, service)
			}
			if item.Health != nil {
				service, err := runtimeService(task, item.Health.Service)
				if err != nil {
					return nil, err
				}
				service.HealthPath = item.Health.Path
				plan.services = []catalog.Service{service}
			}
			plan.options = options
			plan.files, err = packageConfigArchive(item.ConfigFiles, item.Credentials, user, config, secrets, runtimeValues)
			if err != nil {
				return nil, err
			}
			plans = append(plans, plan)
			done[item.Name], progress = true, true
			pending = append(pending[:i], pending[i+1:]...)
		}
		if !progress {
			return nil, errors.New("agent: cyclic or undeclared container dependency")
		}
	}
	return plans, nil
}

func packageConfigArchive(files []catalog.ConfigFile, credentials map[string]string, user string, config map[string]any, secrets map[string]string, runtimeValues map[string]string) ([]byte, error) {
	if len(files) == 0 && len(credentials) == 0 {
		return nil, nil
	}
	uidPart, gidPart, _ := strings.Cut(user, ":")
	uid, err := strconv.Atoi(uidPart)
	if err != nil || uid < 0 {
		return nil, errors.New("agent: private Docker config files require a numeric user")
	}
	gid := uid
	if gidPart != "" {
		gid, err = strconv.Atoi(gidPart)
		if err != nil || gid < 0 {
			return nil, errors.New("agent: private Docker config files require a numeric group")
		}
	}
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	dirs := map[string]bool{}
	contents := map[string][]byte{}
	for _, file := range files {
		data, err := catalog.RenderConfigFile(file, config, secrets, runtimeValues)
		if err != nil {
			return nil, err
		}
		contents[strings.TrimPrefix(file.Path, "/")] = data
	}
	for name, key := range credentials {
		value, exists := secrets[key]
		if !exists {
			return nil, errors.New("agent: missing package credential")
		}
		contents["run/vastora-credentials/"+name] = []byte(value)
	}
	for name, data := range contents {
		var ancestors []string
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			ancestors = append(ancestors, directory)
		}
		for i := len(ancestors) - 1; i >= 0; i-- {
			directory := ancestors[i]
			if dirs[directory] {
				continue
			}
			if err := archive.WriteHeader(&tar.Header{Name: directory + "/", Typeflag: tar.TypeDir, Mode: 0755, Uid: 0, Gid: 0}); err != nil {
				return nil, err
			}
			dirs[directory] = true
		}
		if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Uid: uid, Gid: gid, Size: int64(len(data))}); err != nil {
			return nil, err
		}
		if _, err := archive.Write(data); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (b *DockerPackageBackend) Prepare(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if b.Docker == nil {
		return errors.New("agent: Docker package backend unavailable")
	}
	if task.Operation == "uninstall" {
		return nil
	}
	plans, err := compileDockerPackage(task, receipt)
	if err != nil {
		return err
	}
	b.plans = plans
	if err := b.prepareNetwork(ctx, task, receipt); err != nil {
		return err
	}
	for _, plan := range plans {
		current, err := b.Docker.ContainerInspect(ctx, plan.options.Name, client.ContainerInspectOptions{})
		if err == nil {
			old := resourceNamed(receipt, "container", plan.logical)
			if old == nil || old.ID != current.Container.ID {
				return errors.New("agent: container name collision is not owned by this receipt")
			}
		} else if !errdefs.IsNotFound(err) {
			return errors.New("agent: could not inspect package container")
		}
	}
	for _, storage := range task.Manifest.Runtime.Storage {
		name := runtimeName(receipt, "volume", storage.Name)
		current, err := b.Docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
		if err == nil {
			old := resourceNamed(receipt, "volume", storage.Name)
			if old == nil || old.Name != current.Volume.Name {
				return errors.New("agent: volume name collision is not owned by this receipt")
			}
		} else if !errdefs.IsNotFound(err) {
			return errors.New("agent: could not inspect package storage")
		}
	}
	for _, image := range task.Manifest.Images {
		options, err := declaredImagePullOptions(task, image.Reference)
		if err != nil {
			return err
		}
		pull, err := b.Docker.ImagePull(ctx, image.Reference, options)
		if err != nil {
			return errors.New("agent: package image download failed")
		}
		err = pull.Wait(ctx)
		_ = pull.Close()
		if err != nil {
			return errors.New("agent: package image verification failed")
		}
	}
	return nil
}

func (b *DockerPackageBackend) Inspect(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	for _, resource := range receipt.Resources {
		switch resource.Kind {
		case "network":
			if err := b.inspectNetwork(ctx, task, resource); err != nil {
				return err
			}
		case "image":
			if resource.SHA256 == "" || !strings.HasSuffix(resource.Name, "@sha256:"+resource.SHA256) {
				return errors.New("agent: invalid image receipt")
			}
		case "container":
			current, exists, err := inspectOwnedApplicationContainer(ctx, b.Docker, resource.Name, task.AppKey, resource.Component, task.ApplicationID, anyApplicationDeployment)
			if err != nil || !exists || resource.ID == "" || current.Container.ID != resource.ID {
				return errors.New("agent: container receipt no longer matches runtime ownership")
			}
			if task.Operation == "adopt" && resource.StartedAt != "" && (current.Container.State == nil || current.Container.State.StartedAt != resource.StartedAt) {
				return errors.New("agent: container changed during adoption")
			}
		case "volume", "retained-volume":
			_, exists, err := inspectOwnedApplicationVolume(ctx, b.Docker, resource.Name, task.AppKey, resource.Component, task.ApplicationID)
			if err != nil || !exists {
				return errors.New("agent: volume receipt no longer matches runtime ownership")
			}
		case "bind":
			current, err := b.Docker.ContainerInspect(ctx, resource.ID, client.ContainerInspectOptions{})
			if err != nil {
				return errors.New("agent: retained host mount container is missing")
			}
			found := false
			for _, mounted := range current.Container.Mounts {
				if mounted.Type == "bind" && mounted.Source == resource.Path && mounted.Destination == resource.Component {
					found = true
				}
			}
			if !found {
				return errors.New("agent: retained host mount no longer matches receipt")
			}
		default:
			return errors.New("agent: unexpected resource kind in Docker receipt")
		}
	}
	return nil
}

func (b *DockerPackageBackend) Backup(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	// All writers are stopped before the first byte of a shared-volume snapshot.
	for i := len(receipt.Resources) - 1; i >= 0; i-- {
		resource := receipt.Resources[i]
		if resource.Kind != "container" {
			continue
		}
		current, err := b.Docker.ContainerInspect(ctx, resource.ID, client.ContainerInspectOptions{})
		if err != nil {
			return errors.New("agent: cannot inspect writer before backup")
		}
		if current.Container.State == nil || !current.Container.State.Running {
			continue
		}
		b.resumeIDs = append(b.resumeIDs, resource.ID)
		timeout := 30
		if _, err := b.Docker.ContainerStop(ctx, resource.ID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
			return errors.New("agent: package writer could not be stopped before backup")
		}
	}
	backedUp := map[string]bool{}
	for _, resource := range receipt.Resources {
		if resource.Kind != "volume" && resource.Kind != "bind" {
			continue
		}
		if backedUp[resource.Name] {
			continue
		}
		backedUp[resource.Name] = true
		var archive io.ReadCloser
		for _, candidate := range receipt.Resources {
			if candidate.Kind != "container" {
				continue
			}
			current, err := b.Docker.ContainerInspect(ctx, candidate.ID, client.ContainerInspectOptions{})
			if err != nil {
				return errors.New("agent: backup container inspection failed")
			}
			for _, mounted := range current.Container.Mounts {
				if resource.Kind == "volume" && mounted.Name != resource.Name || resource.Kind == "bind" && (mounted.Source != resource.Path || mounted.Destination != resource.Component) {
					continue
				}
				copied, err := b.Docker.CopyFromContainer(ctx, candidate.ID, client.CopyFromContainerOptions{SourcePath: mounted.Destination})
				if err != nil {
					return errors.New("agent: volume backup could not be read")
				}
				archive = copied.Content
				resource.Path = mounted.Destination
				break
			}
			if archive != nil {
				break
			}
		}
		if archive == nil {
			return errors.New("agent: receipt storage has no verified backup source")
		}
		backup, err := savePackageBackup(b.StateDirectory, task, resource, receipt.PackageVersion, archive)
		_ = archive.Close()
		if err != nil {
			return err
		}
		receipt.Backups = append(receipt.Backups, backup)
	}
	return nil
}

func savePackageBackup(directory string, task DeploymentTask, resource RuntimeResource, version string, source io.Reader) (RuntimeBackup, error) {
	backupPath := filepath.Join(directory, "packages", packageIdentity(task.ApplicationID), "backups", packageIdentity(task.ID), packageIdentity(resource.Name)+".tar")
	if err := checkPackageParents(backupPath); err != nil {
		return RuntimeBackup{}, err
	}
	if err := os.MkdirAll(filepath.Dir(backupPath), 0700); err != nil {
		return RuntimeBackup{}, err
	}
	file, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return RuntimeBackup{}, errors.New("agent: backup already exists or is not writable; review before continuing")
	}
	digest := sha256.New()
	count, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(source, (512<<20)+1))
	if count > 512<<20 {
		copyErr = errors.New("agent: package backup exceeds 512 MiB")
	}
	syncErr, closeErr := file.Sync(), file.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return RuntimeBackup{}, err
	}
	return RuntimeBackup{ID: task.ID, Target: resource.Path, Resource: resource.Name, Path: backupPath, SHA256: hex.EncodeToString(digest.Sum(nil)), PackageVersion: version, CreatedAt: time.Now().UTC()}, nil
}

func (b *DockerPackageBackend) Apply(ctx context.Context, task DeploymentTask, receipt *InstanceResources, persist func() error) error {
	if err := b.applyNetwork(ctx, task, receipt, persist); err != nil {
		return err
	}
	for _, resource := range slices.Clone(receipt.Resources) {
		if resource.Kind != "container" {
			continue
		}
		found := false
		for _, plan := range b.plans {
			if plan.logical == resource.LogicalName {
				found = true
			}
		}
		if found {
			continue
		}
		if err := removeOwnedApplicationContainer(ctx, b.Docker, resource.Name, task.AppKey, resource.Component, task.ApplicationID, anyApplicationDeployment); err != nil {
			return errors.New("agent: removed recipe component could not be retired")
		}
		receipt.Resources = slices.DeleteFunc(receipt.Resources, func(item RuntimeResource) bool { return item.Kind == "container" && item.ID == resource.ID })
		if err := persist(); err != nil {
			return err
		}
	}
	for _, storage := range task.Manifest.Runtime.Storage {
		if resourceNamed(receipt, "volume", storage.Name) != nil {
			continue
		}
		name := runtimeName(receipt, "volume", storage.Name)
		component := applicationVolumeComponent(name)
		if err := ensureOwnedApplicationVolume(ctx, b.Docker, name, task.AppKey, component, task.ApplicationID); err != nil {
			return errors.New("agent: could not create package storage")
		}
		receipt.Resources = append(receipt.Resources, RuntimeResource{Kind: "volume", LogicalName: storage.Name, Name: name, Component: component, Persistent: storage.Persistent})
		if err := persist(); err != nil {
			return err
		}
	}
	for _, plan := range b.plans {
		if old := resourceNamed(receipt, "container", plan.logical); old != nil {
			oldID := old.ID
			if err := removeOwnedApplicationContainer(ctx, b.Docker, old.Name, task.AppKey, old.Component, task.ApplicationID, anyApplicationDeployment); err != nil {
				return errors.New("agent: could not replace owned package container")
			}
			receipt.Resources = slices.DeleteFunc(receipt.Resources, func(item RuntimeResource) bool {
				return item.Kind == "container" && item.LogicalName == plan.logical || item.Kind == "bind" && item.ID == oldID
			})
			if err := persist(); err != nil {
				return err
			}
		}
		created, err := b.Docker.ContainerCreate(ctx, plan.options)
		if err != nil {
			return errors.New("agent: could not create package container")
		}
		receipt.Resources = append(receipt.Resources, RuntimeResource{Kind: "container", LogicalName: plan.logical, Name: plan.options.Name, ID: created.ID, Component: plan.logical, SHA256: strings.TrimPrefix(strings.Split(plan.options.Config.Image, "@")[1], "sha256:")})
		receipt.Resources = slices.DeleteFunc(receipt.Resources, func(item RuntimeResource) bool {
			return item.Kind == "bind" && item.LogicalName == "bind:"+plan.logical
		})
		for _, mounted := range plan.options.HostConfig.Mounts {
			if mounted.Type == mount.TypeBind {
				receipt.Resources = append(receipt.Resources, RuntimeResource{Kind: "bind", LogicalName: "bind:" + plan.logical, Name: mounted.Source, Path: mounted.Source, ID: created.ID, Component: mounted.Target, Persistent: true})
			}
		}
		if err := persist(); err != nil {
			return err
		}
		if len(plan.files) > 0 {
			if _, err := b.Docker.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: bytes.NewReader(plan.files)}); err != nil {
				return errors.New("agent: could not install private package configuration")
			}
		}
		if _, err := b.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			return errors.New("agent: could not start package container")
		}
		if err := b.checkPlan(ctx, task, receipt, plan); err != nil {
			return err
		}
	}
	return nil
}

func (b *DockerPackageBackend) checkPlan(ctx context.Context, task DeploymentTask, receipt *InstanceResources, plan packageContainerPlan) error {
	resource := resourceNamed(receipt, "container", plan.logical)
	if resource == nil {
		return errors.New("agent: package container receipt missing")
	}
	current, err := b.Docker.ContainerInspect(ctx, resource.ID, client.ContainerInspectOptions{})
	if err != nil || current.Container.State == nil || !current.Container.State.Running {
		return errors.New("agent: package container is not running")
	}
	resource.StartedAt = current.Container.State.StartedAt
	check := b.CheckHealth
	if check == nil {
		check = waitForServiceEndpoint
	}
	address := task.ServiceAddress
	if address == "" {
		address = "127.0.0.1"
	}
	for _, service := range plan.services {
		port, err := serviceHostPort(task.Config, service)
		if err != nil {
			return err
		}
		if err := check(ctx, address, port, service); err != nil {
			return errors.New("agent: package service health check failed")
		}
	}
	return nil
}

func (b *DockerPackageBackend) Healthy(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	for _, plan := range b.plans {
		if err := b.checkPlan(ctx, task, receipt, plan); err != nil {
			return err
		}
	}
	return nil
}

func (b *DockerPackageBackend) Remove(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if err := b.Inspect(ctx, task, receipt); err != nil {
		return err
	}
	for i := len(receipt.Resources) - 1; i >= 0; i-- {
		resource := receipt.Resources[i]
		if resource.Kind != "container" {
			continue
		}
		if err := removeOwnedApplicationContainer(ctx, b.Docker, resource.Name, task.AppKey, resource.Component, task.ApplicationID, anyApplicationDeployment); err != nil {
			return errors.New("agent: package container removal failed")
		}
	}
	receipt.Resources = slices.DeleteFunc(receipt.Resources, func(item RuntimeResource) bool { return item.Kind == "container" || item.Kind == "bind" })
	for _, resource := range receipt.Resources {
		if resource.Kind != "network" {
			continue
		}
		engine, ok := b.Docker.(packageNetworkEngine)
		if !ok {
			return errors.New("agent: network removal capability unavailable")
		}
		if _, err := engine.NetworkRemove(ctx, resource.ID, client.NetworkRemoveOptions{}); err != nil {
			return errors.New("agent: private network removal failed")
		}
	}
	receipt.Resources = slices.DeleteFunc(receipt.Resources, func(item RuntimeResource) bool { return item.Kind == "network" })
	for _, resource := range receipt.Resources {
		if resource.Kind != "volume" && resource.Kind != "retained-volume" || resource.Persistent && !task.DeleteData {
			continue
		}
		if err := removeOwnedApplicationVolume(ctx, b.Docker, resource.Name, task.AppKey, resource.Component, task.ApplicationID); err != nil {
			return errors.New("agent: package storage removal failed")
		}
	}
	receipt.Resources = slices.DeleteFunc(receipt.Resources, func(item RuntimeResource) bool {
		return (item.Kind == "volume" || item.Kind == "retained-volume") && (!item.Persistent || task.DeleteData)
	})
	return nil
}

var _ PackageBackend = (*DockerPackageBackend)(nil)
