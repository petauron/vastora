package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/platform"
)

type SystemdPackageBackend struct {
	Manager        SystemdHostApplicationManager
	StateDirectory string
	CheckHealth    func(context.Context, string, int, catalog.Service) error
	stage          string
	files          map[string][]byte
	modes          map[string]os.FileMode
	unitPath       string
	unitName       string
	logicalNames   map[string]string
	createUser     string
	resumeService  bool
}

func (b *SystemdPackageBackend) Prepare(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if task.Operation == "uninstall" {
		return nil
	}
	var err error
	task, err = b.preparePulseInputs(task, receipt)
	if err != nil {
		return err
	}
	target := b.Manager.HostTarget
	if target.OS == "" {
		target = platform.Target{OS: runtime.GOOS, Architecture: runtime.GOARCH}
	}
	if target.OS != "linux" || target.Architecture != "amd64" && target.Architecture != "arm64" {
		return errors.New("agent: unsupported systemd host platform")
	}
	spec := task.Manifest.Runtime.Systemd
	userName, _, _ := strings.Cut(spec.User, ":")
	if userName != "" && userName != "root" {
		if _, err := strconv.Atoi(userName); err != nil {
			if b.Manager.run(ctx, "getent", "passwd", userName) != nil {
				b.createUser = userName
			}
		}
	}
	artifact, err := declaredArtifact(task.Manifest, spec.Artifact, target)
	if err != nil {
		return err
	}
	config, secrets, err := runtimeInputs(task)
	if err != nil {
		return err
	}
	stagingParent := filepath.Join(b.StateDirectory, "packages", packageIdentity(task.ApplicationID), "staging")
	if err := checkPackageParents(filepath.Join(stagingParent, "member")); err != nil {
		return err
	}
	if err := os.MkdirAll(stagingParent, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(stagingParent, "verified-")
	if err != nil {
		return err
	}
	b.stage = staging
	// No caller-controlled redirect scheme, proxy environment, or origin data
	// can bypass the SHA256 comparison in the shared artifact verifier.
	client := b.Manager.HTTPClient
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	}
	archive, err := catalog.DownloadArtifact(ctx, client, artifact)
	if err != nil {
		return errors.New("agent: verified package artifact download failed")
	}
	extracted := filepath.Join(staging, "artifact")
	paths, err := catalog.ExtractArtifact(bytes.NewReader(archive), extracted, artifact, spec.Files)
	if err != nil {
		return errors.New("agent: unsafe or incomplete package archive")
	}
	_ = paths
	if err := catalog.VerifyELF(filepath.Join(extracted, spec.Executable), target.Architecture); err != nil {
		return err
	}
	baseName := packageIdentity(task.ApplicationID)
	b.unitName = baseName + ".service"
	b.unitPath = "/etc/systemd/system/" + b.unitName
	if old := resourceNamed(receipt, "unit", "service"); old != nil {
		b.unitName, b.unitPath = old.Name, old.Path
	}
	b.files, b.modes, b.logicalNames = map[string][]byte{}, map[string]os.FileMode{}, map[string]string{}
	runtimeValues := map[string]string{"private-address:": task.ServiceAddress}
	if task.ServiceAddress == "" {
		runtimeValues["private-address:"] = "127.0.0.1"
	}
	for _, service := range task.Manifest.Services {
		port, err := serviceHostPort(task.Config, service)
		if err != nil {
			return err
		}
		runtimeValues["service-address:"+service.Name] = runtimeValues["private-address:"] + ":" + strconv.Itoa(port)
	}
	for _, name := range spec.StateDirectories {
		path := "/var/lib/" + baseName + "-" + name
		if old := resourceNamed(receipt, "directory", name); old != nil {
			path = old.Path
		}
		runtimeValues["state-directory:"+name] = path
	}
	for _, file := range spec.ConfigFiles {
		runtimeValues["config-file:"+file.Path] = "/run/" + baseName + "/config-" + strings.ReplaceAll(file.Path, "/", "-")
	}
	for name := range spec.Credentials {
		runtimeValues["credential-file:"+name] = "/run/" + baseName + "/" + name
	}
	installedFiles := map[string]string{}
	for _, file := range spec.Files {
		path := "/opt/vastora/packages/" + baseName + "/" + file.Path
		if old := resourceNamed(receipt, "file", "artifact:"+file.Path); old != nil {
			path = old.Path
		}
		raw, err := os.ReadFile(filepath.Join(extracted, file.Path))
		if err != nil {
			return err
		}
		b.files[path] = raw
		b.logicalNames[path] = "artifact:" + file.Path
		mode := os.FileMode(0644)
		if file.Executable {
			mode = 0755
		}
		b.modes[path] = mode
		installedFiles[file.Path] = path
	}
	credentials := map[string]string{}
	for _, file := range spec.ConfigFiles {
		path := "/etc/vastora/packages/" + baseName + "/" + file.Path
		if old := resourceNamed(receipt, "file", "config:"+file.Path); old != nil {
			path = old.Path
		}
		raw, err := catalog.RenderConfigFile(file, config, secrets, runtimeValues)
		if err != nil {
			return err
		}
		b.files[path], b.modes[path] = raw, 0600
		b.logicalNames[path] = "config:" + file.Path
		credentials["config-"+strings.ReplaceAll(file.Path, "/", "-")] = path
	}
	for name, secretKey := range spec.Credentials {
		value, ok := secrets[secretKey]
		if !ok {
			return errors.New("agent: missing package credential")
		}
		path := "/etc/vastora/packages/" + baseName + "/credentials/" + name
		if old := resourceNamed(receipt, "file", "credential:"+name); old != nil {
			path = old.Path
		}
		b.files[path], b.modes[path] = []byte(value), 0600
		b.logicalNames[path] = "credential:" + name
		credentials[name] = path
	}
	environmentPath := "/etc/vastora/packages/" + baseName + "/environment"
	if old := resourceNamed(receipt, "file", "environment"); old != nil {
		environmentPath = old.Path
	}
	var environment strings.Builder
	var keys []string
	for key := range spec.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		text, err := catalog.ResolveString(spec.Environment[key], config, secrets, runtimeValues)
		if err != nil {
			return err
		}
		if strings.ContainsAny(text, "\n\r\x00") {
			return errors.New("agent: unsafe multiline environment value")
		}
		fmt.Fprintf(&environment, "%s=%s\n", key, strconv.Quote(text))
	}
	b.files[environmentPath], b.modes[environmentPath], b.logicalNames[environmentPath] = []byte(environment.String()), 0600, "environment"
	unit, err := compilePackageUnit(task, receipt, installedFiles[spec.Executable], environmentPath, credentials, config, secrets, runtimeValues)
	if err != nil {
		return err
	}
	b.files[b.unitPath], b.modes[b.unitPath] = unit, 0644
	for path := range b.files {
		actual := b.Manager.path(path)
		if err := checkPackageParents(actual); err != nil {
			return err
		}
		current, err := captureHostFile(actual)
		if err != nil {
			return err
		}
		if current.Exists {
			owned := false
			for _, resource := range receipt.Resources {
				if resource.Path == path {
					owned = true
				}
			}
			if !owned {
				return errors.New("agent: package file name collides with an unowned file")
			}
		}
	}
	return nil
}

func unitQuote(value string) string {
	// systemd is not a shell, but performs percent specifiers and $ expansion.
	// Escape both for ordinary scalar data; only executor-generated directives
	// may use specifiers. Quoting prevents a newline becoming a new directive.
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, "$", "$$")
	return strconv.Quote(value)
}

func compilePackageUnit(task DeploymentTask, receipt *InstanceResources, executable, environmentPath string, credentials map[string]string, config map[string]any, secrets map[string]string, runtimeValues map[string]string) ([]byte, error) {
	spec := task.Manifest.Runtime.Systemd
	arguments, err := runtimeStrings(spec.Arguments, config, secrets, runtimeValues)
	if err != nil {
		return nil, err
	}
	var unit strings.Builder
	fmt.Fprintf(&unit, "# Managed by Vastora\n# Application: %s\n[Unit]\nDescription=Managed package %s\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\n", task.ApplicationID, task.Manifest.ID)
	if spec.User == "" {
		unit.WriteString("User=65534\nGroup=65534\n")
	} else {
		parts := strings.SplitN(spec.User, ":", 2)
		fmt.Fprintf(&unit, "User=%s\n", parts[0])
		if len(parts) == 2 {
			fmt.Fprintf(&unit, "Group=%s\n", parts[1])
		}
	}
	unit.WriteString("NoNewPrivileges=yes\nPrivateTmp=yes\nPrivateDevices=yes\nProtectSystem=strict\nProtectHome=yes\nProtectKernelTunables=yes\nProtectKernelModules=yes\nProtectControlGroups=yes\nRestrictSUIDSGID=yes\nUMask=0077\nRestart=on-failure\nRestartSec=5s\n")
	var credentialNames []string
	for name := range credentials {
		credentialNames = append(credentialNames, name)
	}
	sort.Strings(credentialNames)
	baseName := packageIdentity(task.ApplicationID)
	if len(credentialNames) > 0 {
		fmt.Fprintf(&unit, "RuntimeDirectory=%s\nRuntimeDirectoryMode=0700\n", baseName)
	}
	for _, name := range credentialNames {
		fmt.Fprintf(&unit, "LoadCredential=%s:%s\n", name, unitQuote(credentials[name]))
		// The executable and argv here are fixed executor machinery, never a
		// catalog shell hook. Private copies accommodate consumers requiring 0600.
		fmt.Fprintf(&unit, "ExecStartPre=/usr/bin/install -m 0600 \"%%d/%s\" \"/run/%s/%s\"\n", name, baseName, name)
	}
	fmt.Fprintf(&unit, "EnvironmentFile=%s\n", unitQuote(environmentPath))
	for _, name := range spec.StateDirectories {
		directory := packageIdentity(task.ApplicationID) + "-" + name
		if old := resourceNamed(receipt, "directory", name); old != nil {
			if !strings.HasPrefix(old.Path, "/var/lib/") {
				return nil, errors.New("agent: retained state is outside systemd StateDirectory")
			}
			directory = strings.TrimPrefix(old.Path, "/var/lib/")
		}
		fmt.Fprintf(&unit, "StateDirectory=%s\nStateDirectoryMode=0700\n", directory)
	}
	if spec.Limits != nil {
		if spec.Limits.MemoryBytes > 0 {
			fmt.Fprintf(&unit, "MemoryMax=%d\n", spec.Limits.MemoryBytes)
		}
		if spec.Limits.CPUMillis > 0 {
			fmt.Fprintf(&unit, "CPUQuota=%.3f%%\n", float64(spec.Limits.CPUMillis)/10)
		}
		if spec.Limits.Pids > 0 {
			fmt.Fprintf(&unit, "TasksMax=%d\n", spec.Limits.Pids)
		}
	}
	fmt.Fprintf(&unit, "ExecStart=%s", unitQuote(executable))
	for _, arg := range arguments {
		fmt.Fprintf(&unit, " %s", unitQuote(arg))
	}
	unit.WriteString("\n\n[Install]\nWantedBy=multi-user.target\n")
	return []byte(unit.String()), nil
}

func (b *SystemdPackageBackend) Inspect(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	unitFound := false
	for _, resource := range receipt.Resources {
		actual := b.Manager.path(resource.Path)
		if err := checkPackageParents(actual); err != nil {
			return err
		}
		switch resource.Kind {
		case "file", "unit":
			snapshot, err := captureHostFile(actual)
			if err != nil || !snapshot.Exists {
				return errors.New("agent: receipt file no longer exists")
			}
			digest := sha256.Sum256(snapshot.Data)
			if resource.SHA256 == "" || hex.EncodeToString(digest[:]) != resource.SHA256 {
				return errors.New("agent: receipt file has changed outside the managed runtime")
			}
			if resource.Kind == "unit" {
				unitFound = true
				if !bytes.Contains(snapshot.Data, []byte("# Managed by Vastora\n")) {
					return errors.New("agent: refusing unowned systemd unit")
				}
				if !bytes.Contains(snapshot.Data, []byte("# Application: "+task.ApplicationID+"\n")) && task.Operation != "adopt" && resource.Component != "historical-verified" {
					return errors.New("agent: systemd unit instance ownership mismatch")
				}
				if task.Operation == "adopt" && resource.InvocationID != "" {
					observed, err := b.Manager.readPackageUnit(ctx, resource.Name)
					if err != nil {
						return err
					}
					if observed.InvocationID != resource.InvocationID || observed.MainPID != resource.MainPID {
						return errors.New("agent: systemd invocation changed during adoption")
					}
				}
			}
		case "directory":
			info, err := os.Lstat(actual)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("agent: package state is missing or not a real directory")
			}
		default:
			return errors.New("agent: unexpected resource kind in systemd receipt")
		}
	}
	if !unitFound && receipt.State != "retained" {
		return errors.New("agent: systemd ownership evidence is missing")
	}
	return ctx.Err()
}

func (b *SystemdPackageBackend) Backup(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	unit := resourceNamed(receipt, "unit", "service")
	if unit == nil {
		return errors.New("agent: systemd receipt lacks service")
	}
	observed, err := b.Manager.readPackageUnit(ctx, unit.Name)
	if err != nil {
		return err
	}
	b.resumeService = observed.MainPID > 0
	if err := b.Manager.run(ctx, "systemctl", "stop", unit.Name); err != nil {
		return errors.New("agent: systemd service could not stop for backup")
	}
	// Snapshot every managed file and data directory with the writer stopped.
	// Immutable backups survive success and failure; restoring is never automatic.
	for _, resource := range receipt.Resources {
		actual := b.Manager.path(resource.Path)
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		if resource.Kind == "directory" {
			if err := archivePackageDirectory(ctx, writer, actual); err != nil {
				return err
			}
		} else {
			raw, err := os.ReadFile(actual)
			if err != nil {
				return err
			}
			if err := writer.WriteHeader(&tar.Header{Name: filepath.Base(actual), Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(raw))}); err != nil {
				return err
			}
			if _, err := writer.Write(raw); err != nil {
				return err
			}
		}
		if err := writer.Close(); err != nil {
			return err
		}
		backup, err := savePackageBackup(b.StateDirectory, task, resource, receipt.PackageVersion, &archive)
		if err != nil {
			return err
		}
		receipt.Backups = append(receipt.Backups, backup)
	}
	return nil
}

func archivePackageDirectory(ctx context.Context, writer *tar.Writer, directory string) error {
	var size int64
	entries := 0
	return filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == directory {
			return nil
		}
		entries++
		if entries > 100000 {
			return errors.New("agent: backup has too many files")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return errors.New("agent: refusing links or special files in package backup")
		}
		size += info.Size()
		if size > 512<<20 {
			return errors.New("agent: native data backup exceeds 512 MiB")
		}
		name, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(name)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		return errors.Join(copyErr, file.Close())
	})
}

func (b *SystemdPackageBackend) Apply(ctx context.Context, task DeploymentTask, receipt *InstanceResources, persist func() error) error {
	if b.createUser != "" {
		if err := b.Manager.run(ctx, "useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", b.createUser); err != nil {
			return errors.New("agent: package service account could not be created")
		}
	}
	paths := make([]string, 0, len(b.files))
	for path := range b.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		actual := b.Manager.path(path)
		if err := checkPackageParents(actual); err != nil {
			return err
		}
		if err := writeHostFileAtomic(actual, b.files[path], b.modes[path]); err != nil {
			return err
		}
		digest := sha256.Sum256(b.files[path])
		kind, logical := "file", b.logicalNames[path]
		if path == b.unitPath {
			kind, logical = "unit", "service"
		} else {
			if old := resourceNamed(receipt, kind, logical); old != nil {
				logical = old.LogicalName
			}
		}
		if logical == "" {
			return errors.New("agent: internal native resource identity missing")
		}
		resource := RuntimeResource{Kind: kind, LogicalName: logical, Name: filepath.Base(path), Path: path, SHA256: hex.EncodeToString(digest[:])}
		if old := resourceNamed(receipt, kind, logical); old != nil {
			*old = resource
		} else {
			receipt.Resources = append(receipt.Resources, resource)
		}
		if err := persist(); err != nil {
			return err
		}
	}
	if err := b.Manager.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return errors.New("agent: systemd reload failed")
	}
	if err := b.Manager.run(ctx, "systemctl", "enable", b.unitName); err != nil {
		return errors.New("agent: systemd enable failed")
	}
	if err := b.Manager.run(ctx, "systemctl", "start", b.unitName); err != nil {
		return errors.New("agent: systemd start failed")
	}
	for _, name := range task.Manifest.Runtime.Systemd.StateDirectories {
		if resourceNamed(receipt, "directory", name) != nil {
			continue
		}
		path := "/var/lib/" + packageIdentity(task.ApplicationID) + "-" + name
		if err := checkPackageParents(b.Manager.path(path)); err != nil {
			return err
		}
		info, err := os.Lstat(b.Manager.path(path))
		if err != nil || !info.IsDir() {
			return errors.New("agent: systemd did not create expected package state")
		}
		persistent := false
		for _, storage := range task.Manifest.Runtime.Storage {
			if storage.Name == name {
				persistent = storage.Persistent
			}
		}
		receipt.Resources = append(receipt.Resources, RuntimeResource{Kind: "directory", LogicalName: name, Name: filepath.Base(path), Path: path, Persistent: persistent})
		if err := persist(); err != nil {
			return err
		}
	}
	return nil
}

func (b *SystemdPackageBackend) Healthy(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	unit := resourceNamed(receipt, "unit", "service")
	if unit == nil {
		return errors.New("agent: missing systemd receipt")
	}
	if err := b.Manager.run(ctx, "systemctl", "is-active", "--quiet", unit.Name); err != nil {
		return errors.New("agent: package service did not become active")
	}
	observed, err := b.Manager.readPackageUnit(ctx, unit.Name)
	if err != nil {
		return err
	}
	unit.MainPID, unit.InvocationID, unit.StartedAt = observed.MainPID, observed.InvocationID, observed.StartedAt
	if err := b.completePulseEnrollment(ctx, task, receipt); err != nil {
		return err
	}
	check := b.CheckHealth
	if check == nil {
		check = waitForServiceEndpoint
	}
	address := task.ServiceAddress
	if address == "" {
		address = "127.0.0.1"
	}
	for _, service := range task.Manifest.Services {
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

func (b *SystemdPackageBackend) Remove(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if err := b.Inspect(ctx, task, receipt); err != nil {
		return err
	}
	unit := resourceNamed(receipt, "unit", "service")
	if unit == nil {
		return errors.New("agent: missing systemd receipt")
	}
	if err := b.Manager.run(ctx, "systemctl", "disable", "--now", unit.Name); err != nil {
		return errors.New("agent: package service could not be disabled")
	}
	for _, resource := range receipt.Resources {
		if resource.Kind == "directory" {
			continue
		}
		if err := removeHostFile(b.Manager.path(resource.Path)); err != nil {
			return err
		}
	}
	if err := b.Manager.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return errors.New("agent: systemd reload failed")
	}
	// Never recursively delete an adopted host directory. Deletion is safe only
	// for this executor's exact per-instance namespace, after receipt verification.
	for _, resource := range receipt.Resources {
		if resource.Kind != "directory" || resource.Persistent && !task.DeleteData {
			continue
		}
		if !strings.HasPrefix(resource.Path, "/var/lib/"+packageIdentity(task.ApplicationID)+"-") {
			return errors.New("agent: retained historical state requires explicit manual data removal")
		}
		if err := checkPackageParents(b.Manager.path(resource.Path)); err != nil {
			return err
		}
		if err := os.RemoveAll(b.Manager.path(resource.Path)); err != nil {
			return err
		}
	}
	receipt.Resources = slices.DeleteFunc(receipt.Resources, func(resource RuntimeResource) bool {
		return resource.Kind != "directory" || !resource.Persistent || task.DeleteData
	})
	return nil
}

var _ PackageBackend = (*SystemdPackageBackend)(nil)

func (manager SystemdHostApplicationManager) readPackageUnit(ctx context.Context, name string) (RuntimeResource, error) {
	args := []string{"show", name, "--property=MainPID", "--property=InvocationID", "--property=ActiveEnterTimestampMonotonic"}
	var output []byte
	var err error
	if manager.ReadCommand != nil {
		output, err = manager.ReadCommand(ctx, "systemctl", args...)
	} else {
		output, err = exec.CommandContext(ctx, "systemctl", args...).Output()
	}
	if err != nil || len(output) > 16<<10 {
		return RuntimeResource{}, errors.New("agent: systemd invocation inspection failed")
	}
	resource := RuntimeResource{}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "MainPID":
			resource.MainPID, _ = strconv.Atoi(value)
		case "InvocationID":
			resource.InvocationID = value
		case "ActiveEnterTimestampMonotonic":
			resource.StartedAt = value
		}
	}
	if resource.MainPID < 0 || len(resource.InvocationID) > 64 {
		return RuntimeResource{}, errors.New("agent: malformed systemd invocation evidence")
	}
	return resource, nil
}
