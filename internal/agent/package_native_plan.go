package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"

	"github.com/petauron/catalog/catalog"
)

// nativePackagePlan contains only executor-owned resource locations. Logical
// names from a signed manifest never select existing host accounts or units.
// Planning is side-effect free; ownership inspection precedes any host write.
type nativePackagePlan struct {
	Users     map[string]string
	Units     []nativeUnitPlan
	Artifacts map[string][]catalog.ArtifactFile
	State     map[string]nativeStatePlan
}

type nativeUnitPlan struct {
	LogicalName      string
	Name             string
	Path             string
	RuntimeDirectory string
	ConfigDirectory  string
	Automatic        bool
	WritableState    []string
	StateCredentials map[string]catalog.StateCredential
	Process          catalog.SystemdRuntime
}

type nativeStatePlan struct {
	Path       string
	User       string
	Persistent bool
}

func nativeAccountName(applicationID, logicalName string) string {
	// Length-delimited identity prevents ambiguous concatenation. The result
	// fits the 32-character system account limit and cannot select root/daemon.
	h := sha256.Sum256([]byte(applicationID + "\x00" + logicalName))
	return "vp-" + hex.EncodeToString(h[:12])
}

func planNativePackage(applicationID string, app catalog.AppManifest) (nativePackagePlan, error) {
	var p nativePackagePlan
	if applicationID == "" {
		return p, errors.New("agent: native package identity is required")
	}
	if err := catalog.ValidateRuntime(app); err != nil {
		return p, err
	}
	if app.Runtime.Kind != "systemd" || app.Runtime.Version != 2 || app.Runtime.NativeServices == nil {
		return p, errors.New("agent: native bundle requires systemd version 2")
	}
	p.Users = map[string]string{}
	p.Artifacts = map[string][]catalog.ArtifactFile{}
	p.State = map[string]nativeStatePlan{}
	base := packageIdentity(applicationID)
	for _, logical := range app.Runtime.NativeServices.Users {
		p.Users[logical] = nativeAccountName(applicationID, logical)
	}
	persistent := map[string]bool{}
	for _, storage := range app.Runtime.Storage {
		persistent[storage.Name] = storage.Persistent
	}
	for _, service := range app.Runtime.NativeServices.Services {
		process := service.Process
		if service.ManagedUser != "" {
			process.User = p.Users[service.ManagedUser]
		}
		name := base + "-" + service.Name
		p.Units = append(p.Units, nativeUnitPlan{
			LogicalName: service.Name, Name: name + ".service",
			Path:             filepath.Join("/etc/systemd/system", name+".service"),
			RuntimeDirectory: name,
			ConfigDirectory:  filepath.Join("/etc/vastora/packages", base, service.Name),
			Automatic:        service.Start == "automatic", Process: process, WritableState: append([]string(nil), service.WritableState...),
			StateCredentials: service.StateCredentials,
		})
		for _, state := range process.StateDirectories {
			p.State[state] = nativeStatePlan{Path: filepath.Join("/var/lib", base+"-"+state), User: process.User, Persistent: persistent[state]}
		}
		if _, exists := p.Artifacts[process.Artifact]; !exists {
			files, err := catalog.NativeArtifactFiles(app, process.Artifact)
			if err != nil {
				return nativePackagePlan{}, err
			}
			p.Artifacts[process.Artifact] = files
		}
	}
	sort.Slice(p.Units, func(i, j int) bool { return p.Units[i].LogicalName < p.Units[j].LogicalName })
	return p, nil
}
