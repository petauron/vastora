package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/platform"
)

type nativePreparedFile struct {
	Path             string
	Data             []byte
	Mode             os.FileMode
	Kind             string
	LogicalName      string
	Component        string
	ApplicationOwned bool
}

type nativePreparedPackage struct {
	BeforeReplace func(context.Context) error
	Plan          nativePackagePlan
	Files         map[string]nativePreparedFile
}

type nativeInstallationService struct {
	Unit        string            `json:"unit"`
	User        string            `json:"user"`
	SHA256      string            `json:"sha256"`
	ConfigFiles map[string]string `json:"configFiles,omitempty"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

type nativeInstallation struct {
	Version  int                                  `json:"version"`
	Services map[string]nativeInstallationService `json:"services"`
}

func nativeInstallationPath(applicationID string) string {
	return filepath.Join("/etc/vastora/packages", packageIdentity(applicationID), "installation.json")
}

// prepareNativePackage only writes private temporary staging. It authenticates
// all artifacts and renders all declarations before an installer can stop a
// service or write any host configuration. No downloaded binary is executed.
func prepareNativePackage(ctx context.Context, manager SystemdHostApplicationManager, stateDirectory string, task DeploymentTask) (nativePreparedPackage, error) {
	var prepared nativePreparedPackage
	plan, err := planNativePackage(task.ApplicationID, task.Manifest)
	if err != nil {
		return prepared, err
	}
	target := manager.HostTarget
	if target.OS == "" {
		target = platform.Target{OS: runtime.GOOS, Architecture: runtime.GOARCH}
	}
	if target.OS != "linux" || target.Architecture != "amd64" && target.Architecture != "arm64" {
		return prepared, errors.New("agent: unsupported native bundle platform")
	}
	config, secrets, err := runtimeInputs(task)
	if err != nil {
		return prepared, err
	}
	base := packageIdentity(task.ApplicationID)
	parent := filepath.Join(stateDirectory, "packages", base, "staging")
	if err := checkPackageParents(filepath.Join(parent, "member")); err != nil {
		return prepared, err
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return prepared, err
	}
	stage, err := os.MkdirTemp(parent, "native-verified-")
	if err != nil {
		return prepared, err
	}
	defer os.RemoveAll(stage)
	prepared.Plan, prepared.Files = plan, map[string]nativePreparedFile{}
	add := func(file nativePreparedFile) error {
		if _, exists := prepared.Files[file.Path]; exists {
			return errors.New("agent: conflicting native package file paths")
		}
		prepared.Files[file.Path] = file
		return nil
	}
	client := manager.HTTPClient
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	}
	var artifactNames []string
	for name := range plan.Artifacts {
		artifactNames = append(artifactNames, name)
	}
	sort.Strings(artifactNames)
	for _, name := range artifactNames {
		artifact, err := declaredArtifact(task.Manifest, name, target)
		if err != nil {
			return nativePreparedPackage{}, err
		}
		raw, err := catalog.DownloadArtifact(ctx, client, artifact)
		if err != nil {
			return nativePreparedPackage{}, errors.New("agent: native bundle download verification failed")
		}
		extracted := filepath.Join(stage, name)
		if _, err := catalog.ExtractArtifact(bytes.NewReader(raw), extracted, artifact, plan.Artifacts[name]); err != nil {
			return nativePreparedPackage{}, err
		}
		for _, file := range plan.Artifacts[name] {
			mode := os.FileMode(0644)
			if file.Executable {
				if err := catalog.VerifyELF(filepath.Join(extracted, file.Path), target.Architecture); err != nil {
					return nativePreparedPackage{}, err
				}
				mode = 0755
			}
			data, err := os.ReadFile(filepath.Join(extracted, file.Path))
			if err != nil {
				return nativePreparedPackage{}, err
			}
			if err := add(nativePreparedFile{Path: filepath.Join("/opt/vastora/packages", base, "artifacts", name, file.Path), Data: data, Mode: mode, Kind: "file", LogicalName: "artifact:" + name + ":" + file.Path}); err != nil {
				return nativePreparedPackage{}, err
			}
		}
	}
	common := map[string]string{"private-address:": task.ServiceAddress}
	common["native-installation:"] = nativeInstallationPath(task.ApplicationID)
	if task.ServiceAddress == "" {
		common["private-address:"] = "127.0.0.1"
	}
	for name, user := range plan.Users {
		common["managed-user:"+name] = user
	}
	for _, unit := range plan.Units {
		common["native-unit:"+unit.LogicalName] = unit.Name
	}
	for _, service := range task.Manifest.Services {
		port, err := serviceHostPort(task.Config, service)
		if err != nil {
			return nativePreparedPackage{}, err
		}
		common["service-address:"+service.Name] = common["private-address:"] + ":" + strconv.Itoa(port)
	}
	configurationPaths := map[string]map[string]string{}
	credentialPaths := map[string]map[string]string{}
	for _, unit := range plan.Units {
		values := map[string]string{}
		for key, value := range common {
			values[key] = value
		}
		statePaths := map[string]string{}
		for _, name := range unit.Process.StateDirectories {
			statePaths[name] = plan.State[name].Path
			values["state-directory:"+name] = plan.State[name].Path
		}
		var writable []string
		for _, name := range unit.WritableState {
			writable = append(writable, plan.State[name].Path)
			values["state-directory:"+name] = plan.State[name].Path
		}
		credentials := map[string]string{}
		for name, source := range unit.StateCredentials {
			credentials[name] = filepath.Join(plan.State[source.Storage].Path, source.Path)
			values["credential-file:"+name] = "/run/" + unit.RuntimeDirectory + "/" + name
		}
		for _, file := range unit.Process.ConfigFiles {
			values["config-file:"+file.Path] = "/run/" + unit.RuntimeDirectory + "/config-" + strings.ReplaceAll(file.Path, "/", "-")
		}
		for name := range unit.Process.Credentials {
			values["credential-file:"+name] = "/run/" + unit.RuntimeDirectory + "/" + name
		}
		configFile := func(path, logical string, raw []byte) error {
			return add(nativePreparedFile{Path: path, Data: raw, Mode: 0600, Kind: "file", LogicalName: unit.LogicalName + ":" + logical, Component: unit.LogicalName, ApplicationOwned: true})
		}
		for _, file := range unit.Process.ConfigFiles {
			name := "config-" + strings.ReplaceAll(file.Path, "/", "-")
			if _, exists := credentials[name]; exists {
				return nativePreparedPackage{}, errors.New("agent: generated configuration credential names collide")
			}
			path := filepath.Join(unit.ConfigDirectory, "config", file.Path)
			if configurationPaths[unit.LogicalName] == nil {
				configurationPaths[unit.LogicalName] = map[string]string{}
			}
			configurationPaths[unit.LogicalName][file.Path] = path
			raw, err := catalog.RenderConfigFile(file, config, secrets, values)
			if err != nil {
				return nativePreparedPackage{}, err
			}
			credentials[name] = path
			if err := configFile(path, "config:"+file.Path, raw); err != nil {
				return nativePreparedPackage{}, err
			}
		}
		for name, key := range unit.Process.Credentials {
			if _, exists := credentials[name]; exists {
				return nativePreparedPackage{}, errors.New("agent: credential name collides with generated configuration")
			}
			value, ok := secrets[key]
			if !ok {
				return nativePreparedPackage{}, errors.New("agent: missing native bundle credential")
			}
			path := filepath.Join(unit.ConfigDirectory, "credentials", name)
			credentials[name] = path
			if err := configFile(path, "credential:"+name, []byte(value)); err != nil {
				return nativePreparedPackage{}, err
			}
		}
		if len(credentials) > 0 {
			credentialPaths[unit.LogicalName] = credentials
		}
		var environment strings.Builder
		var keys []string
		for key := range unit.Process.Environment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, err := catalog.ResolveString(unit.Process.Environment[key], config, secrets, values)
			if err != nil {
				return nativePreparedPackage{}, err
			}
			if strings.ContainsAny(value, "\n\r\x00") {
				return nativePreparedPackage{}, errors.New("agent: multiline native environment value")
			}
			fmt.Fprintf(&environment, "%s=%s\n", key, strconv.Quote(value))
		}
		environmentPath := filepath.Join(unit.ConfigDirectory, "environment")
		if err := configFile(environmentPath, "environment", []byte(environment.String())); err != nil {
			return nativePreparedPackage{}, err
		}
		executable := filepath.Join("/opt/vastora/packages", base, "artifacts", unit.Process.Artifact, unit.Process.Executable)
		raw, err := compileNativeProcessUnit(task.ApplicationID, task.Manifest.ID, &unit.Process, unit.RuntimeDirectory, statePaths, writable, executable, environmentPath, credentials, config, secrets, values)
		if err != nil {
			return nativePreparedPackage{}, err
		}
		if err := add(nativePreparedFile{Path: unit.Path, Data: raw, Mode: 0644, Kind: "unit", LogicalName: unit.LogicalName, Component: unit.LogicalName}); err != nil {
			return nativePreparedPackage{}, err
		}
	}
	installation := nativeInstallation{Version: 1, Services: map[string]nativeInstallationService{}}
	for _, unit := range plan.Units {
		digest := sha256.Sum256(prepared.Files[unit.Path].Data)
		installation.Services[unit.LogicalName] = nativeInstallationService{Unit: unit.Name, User: unit.Process.User, SHA256: hex.EncodeToString(digest[:]), ConfigFiles: configurationPaths[unit.LogicalName], Credentials: credentialPaths[unit.LogicalName]}
	}
	raw, err := json.Marshal(installation)
	if err != nil {
		return nativePreparedPackage{}, err
	}
	if err := add(nativePreparedFile{Path: nativeInstallationPath(task.ApplicationID), Data: raw, Mode: 0644, Kind: "file", LogicalName: "installation"}); err != nil {
		return nativePreparedPackage{}, err
	}
	return prepared, nil
}
