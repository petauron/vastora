package agent

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/petauron/catalog/catalog"
)

type daylightRecoveryFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
	Mode uint32 `json:"mode"`
}
type daylightHandoverJournal struct {
	TaskID            string                 `json:"taskId"`
	State             string                 `json:"state"`
	TargetVersion     string                 `json:"targetVersion"`
	TargetManifest    string                 `json:"targetManifest"`
	BeforeReceipt     InstanceResources      `json:"beforeReceipt"`
	BeforeManifest    catalog.AppManifest    `json:"beforeManifest"`
	BeforeSite        *CaddySite             `json:"beforeSite,omitempty"`
	BeforeCertificate *nodeSiteJournal       `json:"beforeCertificate,omitempty"`
	Files             []daylightRecoveryFile `json:"files"`
	CandidateFiles    map[string]string      `json:"candidateFiles"`
	SourceRoot        string                 `json:"sourceRoot"`
	RetainedRoot      string                 `json:"retainedRoot"`
	Unit              string                 `json:"unit"`
	Automatic         bool                   `json:"automatic"`
	WasRunning        bool                   `json:"wasRunning"`
}

type daylightHandover struct {
	backend *NativeServicesPackageBackend
	journal *daylightHandoverJournal
	prior   *daylightHandoverJournal
}

func (j daylightHandoverJournal) validate(applicationID string) error {
	root := filepath.Join("/opt/vastora/packages", packageIdentity(applicationID), "artifacts")
	digest, err := hex.DecodeString(j.TargetManifest)
	if err != nil || len(digest) != 32 || j.TaskID == "" || len(j.TaskID) > 256 || j.TargetVersion == "" || j.SourceRoot != root || j.RetainedRoot != root+".shared-caddy-"+packageIdentity(j.TaskID) || j.BeforeReceipt.ApplicationID != applicationID || !daylightKey(j.BeforeReceipt.AppKey) || (j.State != "applying" && j.State != "runtime_ready" && j.State != "completed" && j.State != "rolled_back") {
		return errors.New("agent: invalid website handover ownership")
	}
	plan, err := planNativePackage(applicationID, j.BeforeManifest)
	if err != nil || len(plan.Units) != 1 || plan.Units[0].Name != j.Unit || plan.Units[0].Automatic != j.Automatic || j.BeforeManifest.Version != j.BeforeReceipt.PackageVersion {
		return errors.New("agent: invalid website handover source")
	}
	allowed := map[string]bool{plan.Units[0].Path: true, nativeInstallationPath(applicationID): true}
	seen := map[string]bool{}
	for _, file := range j.Files {
		if !allowed[file.Path] || seen[file.Path] || len(file.Data) == 0 || len(file.Data) > 64<<10 || file.Mode&^0777 != 0 || file.Mode&0022 != 0 {
			return errors.New("agent: invalid website recovery metadata")
		}
		seen[file.Path] = true
	}
	if len(seen) != len(allowed) {
		return errors.New("agent: incomplete website recovery metadata")
	}
	configRoot := filepath.Dir(nativeInstallationPath(applicationID)) + string(filepath.Separator)
	for path, value := range j.CandidateFiles {
		digest, err := hex.DecodeString(value)
		if err != nil || len(digest) != 32 || filepath.Clean(path) != path || (!allowed[path] && !strings.HasPrefix(path, root+string(filepath.Separator)) && !strings.HasPrefix(path, configRoot)) {
			return errors.New("agent: invalid website candidate ownership")
		}
	}
	if j.BeforeSite != nil && (j.BeforeSite.ApplicationID != applicationID || j.BeforeSite.validate() != nil) {
		return errors.New("agent: invalid previous website")
	}
	if j.BeforeCertificate != nil && (j.BeforeCertificate.Task.ApplicationID != applicationID || j.BeforeCertificate.Task.Validate() != nil) {
		return errors.New("agent: invalid previous website certificate")
	}
	return nil
}

func (b *NativeServicesPackageBackend) prepareWebsite(ctx context.Context, task DeploymentTask, receipt *InstanceResources) (*daylightHandover, error) {
	if b.Store.sharedCaddy == nil || b.prepared == nil {
		return nil, errors.New("agent: shared Caddy is unavailable before website replacement")
	}
	plan := b.prepared.Plan
	if len(plan.Units) != 1 || plan.Units[0].LogicalName != "web" || plan.Units[0].Automatic || plan.Units[0].Process.Executable != "daylight" || !slices.Contains(task.Manifest.Runtime.RequiredCapabilities, "shared-caddy") || !slices.Contains(task.AuthorizedCapabilities, "shared-caddy") {
		return nil, errors.New("agent: managed Daylight requires the authorized shared Caddy content recipe")
	}
	sourceRoot := filepath.Join("/opt/vastora/packages", packageIdentity(task.ApplicationID), "artifacts")
	page := filepath.Join(sourceRoot, plan.Units[0].Process.Artifact, "site/index.html")
	if file, ok := b.prepared.Files[page]; !ok || len(file.Data) == 0 || len(file.Data) > 512<<10 {
		return nil, errors.New("agent: signed Daylight content is missing")
	}
	var input struct {
		Hostname    string `json:"hostname"`
		HTTPPort    int    `json:"http_port"`
		HTTPSListen string `json:"https_listen"`
	}
	if json.Unmarshal(task.Config, &input) != nil {
		return nil, errors.New("agent: invalid website configuration")
	}
	address := task.ServiceAddress
	if address == "" {
		address = "127.0.0.1"
	}
	if task.AppKey == "vastora-official/daylight" && input.HTTPSListen != "" {
		return nil, errors.New("agent: the website does not declare a public HTTPS listener")
	}
	if task.AppKey == "vastora-official/daylight-https" && input.HTTPSListen == "" {
		return nil, errors.New("agent: an exact HTTPS listener is required")
	}
	if err := (CaddySite{ApplicationID: task.ApplicationID, Hostname: input.Hostname, Address: address, HTTPPort: input.HTTPPort, PublicListen: input.HTTPSListen, HTML: string(b.prepared.Files[page].Data)}).validate(); err != nil {
		return nil, err
	}
	h := &daylightHandover{backend: b}
	// Renaming the old immutable tree is not another full copy. Reserve the
	// candidate files, a possible first native runtime copy and bounded journal
	// overhead before stopping the current site or writing host resources.
	var required uint64 = 16 << 20
	for _, file := range b.prepared.Files {
		if !file.ApplicationOwned {
			required += uint64(len(file.Data)) * 2
		}
	}
	for _, path := range []string{b.Manager.path(sourceRoot), b.Manager.path(nativeCaddyBinary), b.Store.dataDir} {
		if err := requireWebsiteSpace(path, required); err != nil {
			return nil, err
		}
	}
	if task.Operation != "upgrade" {
		return h, nil
	}
	installed, err := b.Store.AppliedInstallation(ctx, task.AppKey)
	if err != nil || installed.ApplicationID != task.ApplicationID {
		return nil, errors.New("agent: website source registration is unavailable")
	}
	previousPlan, err := planNativePackage(task.ApplicationID, installed.Manifest)
	if err != nil || len(previousPlan.Units) != 1 || previousPlan.Units[0].Name != plan.Units[0].Name {
		return nil, errors.New("agent: website runtime identity changed")
	}
	state, err := b.Store.readCaddySite(ctx, task.ApplicationID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if state.Handover != nil && state.Handover.State != "completed" && state.Handover.State != "rolled_back" {
		if state.Handover.State != "runtime_ready" || installed.Version != state.Handover.TargetVersion || receipt.ManifestSHA256 != state.Handover.TargetManifest || receipt.State != "ready" {
			return nil, errors.New("agent: review the previous website handover before another upgrade")
		}
		h.prior = state.Handover
	}
	if state.State != "" && state.State != "applied" {
		return nil, errors.New("agent: recover the website journal before replacement")
	}
	encoded, _ := json.Marshal(receipt)
	var before InstanceResources
	if json.Unmarshal(encoded, &before) != nil {
		return nil, errors.New("agent: invalid website source receipt")
	}
	j := &daylightHandoverJournal{TaskID: task.ID, State: "applying", TargetVersion: task.Manifest.Version, TargetManifest: task.ManifestSHA256, BeforeReceipt: before, BeforeManifest: installed.Manifest, BeforeSite: state.Site, SourceRoot: sourceRoot, RetainedRoot: sourceRoot + ".shared-caddy-" + packageIdentity(task.ID), Unit: previousPlan.Units[0].Name, Automatic: previousPlan.Units[0].Automatic, CandidateFiles: map[string]string{}}
	if _, err := os.Lstat(b.Manager.path(j.RetainedRoot)); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("agent: website handover recovery directory already exists")
	}
	observed, err := b.Manager.readPackageUnit(ctx, j.Unit)
	if err != nil {
		return nil, err
	}
	j.WasRunning = observed.MainPID > 0
	if certificate, err := b.Store.readNodeSiteJournal(ctx, task.ApplicationID); err == nil {
		j.BeforeCertificate = &certificate
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if j.Automatic && task.AppKey == "vastora-official/daylight-https" && j.BeforeSite == nil && (j.BeforeCertificate == nil || j.BeforeCertificate.State != "applied") {
		return nil, errors.New("agent: migrate the direct website certificate to Center management before replacing its standalone HTTPS runtime")
	}
	for _, resource := range receipt.Resources {
		if resource.Kind != "unit" && !(resource.Kind == "file" && resource.LogicalName == "installation") {
			continue
		}
		raw, err := readOwnedCaddyFile(b.Manager.path(resource.Path), resource.SHA256, 64<<10)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(b.Manager.path(resource.Path))
		if err != nil {
			return nil, err
		}
		j.Files = append(j.Files, daylightRecoveryFile{Path: resource.Path, Data: raw, Mode: uint32(info.Mode().Perm())})
	}
	for path, file := range b.prepared.Files {
		if !file.ApplicationOwned {
			j.CandidateFiles[path] = sha256String(file.Data)
		}
	}
	if err := j.validate(task.ApplicationID); err != nil {
		return nil, err
	}
	h.journal = j
	return h, nil
}

func (h *daylightHandover) save(ctx context.Context, applicationID string) error {
	state, err := h.backend.Store.readCaddySite(ctx, applicationID)
	if errors.Is(err, sql.ErrNoRows) {
		state = caddySiteJournal{ApplicationID: applicationID, State: "applied"}
	} else if err != nil {
		return err
	}
	state.Handover = h.journal
	return h.backend.Store.saveCaddySite(ctx, state)
}

func (h *daylightHandover) apply(ctx context.Context, task DeploymentTask, receipt *InstanceResources, persist func() error) (err error) {
	b := h.backend
	if h.prior != nil {
		if err := b.Store.completeSharedDaylight(ctx, DeploymentTask{ID: h.prior.TaskID, ApplicationID: task.ApplicationID, ManifestSHA256: h.prior.TargetManifest}); err != nil {
			return err
		}
	}
	if h.journal != nil {
		if err := h.save(ctx, task.ApplicationID); err != nil {
			return err
		}
		b.prepared.BeforeReplace = func(context.Context) error {
			return os.Rename(b.Manager.path(h.journal.SourceRoot), b.Manager.path(h.journal.RetainedRoot))
		}
		defer func() {
			if err == nil {
				return
			}
			recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
			defer cancel()
			err = errors.Join(err, h.rollback(recovery, receipt, persist))
		}()
	}
	// These are the exact grants validated by the outer package executor. Record
	// them before the shared platform service is touched, while state=applying.
	receipt.AuthorizedCapabilities = append([]string(nil), task.AuthorizedCapabilities...)
	if err = persist(); err != nil {
		return err
	}
	if err = applyNativePackage(ctx, b.Manager, task, *b.prepared, receipt, persist); err != nil {
		return err
	}
	installed := AppliedInstallation{ApplicationID: task.ApplicationID, AppKey: task.AppKey, Version: task.Manifest.Version, Manifest: task.Manifest, Config: task.Config, ServiceAddress: task.ServiceAddress}
	if err = b.Store.syncSharedDaylight(ctx, installed); err != nil {
		return err
	}
	if h.journal != nil {
		h.journal.State = "runtime_ready"
		err = h.save(ctx, task.ApplicationID)
	}
	return err
}

func (h *daylightHandover) rollback(ctx context.Context, receipt *InstanceResources, persist func() error) (resultErr error) {
	b, j := h.backend, h.journal
	sharedErr := b.Store.restoreSharedCaddySites(ctx)
	if sharedErr == nil {
		sharedErr = b.Store.updateCaddySite(ctx, j.BeforeReceipt.ApplicationID, j.BeforeSite, nil)
	}
	defer func() { resultErr = errors.Join(sharedErr, resultErr) }()
	// Still restore owned source files and the previous unit after a partial
	// first runtime installation failed. Such a failure must not strand the
	// standalone website merely because the new shared service is incomplete.
	source, retained := b.Manager.path(j.SourceRoot), b.Manager.path(j.RetainedRoot)
	if _, err := os.Lstat(retained); err == nil {
		if err := verifyDaylightArtifactTree(retained, j.SourceRoot, j.BeforeReceipt.Resources, nil); err != nil {
			return err
		}
		if _, err := os.Lstat(source); err == nil {
			if err := verifyDaylightArtifactTree(source, j.SourceRoot, nil, j.CandidateFiles); err != nil {
				return err
			}
			if err := os.RemoveAll(source); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(retained, source); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := verifyDaylightArtifactTree(source, j.SourceRoot, j.BeforeReceipt.Resources, nil); err != nil {
		return err
	}
	for _, file := range j.Files {
		path := b.Manager.path(file.Path)
		if err := checkPackageParents(path); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 64<<10 {
			return errors.New("agent: unsafe website metadata during recovery")
		}
		current, err := os.ReadFile(path)
		if err != nil || (sha256String(current) != sha256String(file.Data) && sha256String(current) != j.CandidateFiles[file.Path]) {
			return errors.New("agent: website metadata changed during recovery")
		}
		if err := writeHostFileAtomic(path, file.Data, os.FileMode(file.Mode)); err != nil {
			return err
		}
	}
	*receipt = j.BeforeReceipt
	if err := persist(); err != nil {
		return err
	}
	if j.BeforeCertificate != nil {
		if err := b.Store.saveNodeSiteJournal(ctx, *j.BeforeCertificate); err != nil {
			return err
		}
	}
	if err := b.Manager.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	action := "disable"
	if j.Automatic {
		action = "enable"
	}
	if err := b.Manager.run(ctx, "systemctl", action, j.Unit); err != nil {
		return err
	}
	if j.WasRunning {
		if err := b.Manager.run(ctx, "systemctl", "start", j.Unit); err != nil {
			return err
		}
	}
	observed, err := b.Manager.readPackageUnit(ctx, j.Unit)
	if err != nil {
		return err
	}
	if j.WasRunning && observed.MainPID == 0 {
		return errors.New("agent: previous website did not resume")
	}
	if j.WasRunning && j.Automatic {
		installed, err := b.Store.AppliedInstallation(ctx, j.BeforeReceipt.AppKey)
		if err != nil || installed.ApplicationID != j.BeforeReceipt.ApplicationID || installed.Version != j.BeforeReceipt.PackageVersion {
			return errors.New("agent: website source registration changed during recovery")
		}
		previous := &NativeServicesPackageBackend{Manager: b.Manager}
		if err := previous.Healthy(ctx, DeploymentTask{ApplicationID: installed.ApplicationID, Manifest: j.BeforeManifest, Config: installed.Config, ServiceAddress: installed.ServiceAddress}, receipt); err != nil {
			return err
		}
	}
	if sharedErr == nil && j.BeforeSite != nil {
		sharedErr = probeCaddySite(ctx, *j.BeforeSite)
	}
	if resource := resourceNamed(receipt, "unit", "web"); resource != nil {
		resource.MainPID, resource.InvocationID, resource.StartedAt = observed.MainPID, observed.InvocationID, observed.StartedAt
	}
	if err := persist(); err != nil {
		return err
	}
	if sharedErr != nil {
		return nil
	} // retain unfinished journal for review
	j.State = "rolled_back"
	return h.save(ctx, j.BeforeReceipt.ApplicationID)
}

func requireWebsiteSpace(path string, required uint64) error {
	for {
		var stat syscall.Statfs_t
		err := syscall.Statfs(path, &stat)
		if errors.Is(err, os.ErrNotExist) && filepath.Dir(path) != path {
			path = filepath.Dir(path)
			continue
		}
		if err != nil {
			return errors.New("agent: cannot inspect website handover capacity")
		}
		if uint64(stat.Bavail)*uint64(stat.Bsize) < required {
			return errors.New("agent: insufficient space for website handover and recovery")
		}
		return nil
	}
}

// The retained tree contains only immutable package artifacts. Validate every
// path/digest before removing or replacing it; configuration and data are not
// inside this namespace and are never included in this recovery copy.
func verifyDaylightArtifactTree(root, original string, resources []RuntimeResource, candidate map[string]string) error {
	if err := checkPackageParents(filepath.Join(root, "member")); err != nil {
		return err
	}
	allowed := map[string]string{}
	for _, resource := range resources {
		if resource.Kind == "file" && strings.HasPrefix(resource.Path, original+string(filepath.Separator)) {
			relative, _ := filepath.Rel(original, resource.Path)
			allowed[relative] = resource.SHA256
		}
	}
	for path, digest := range candidate {
		if strings.HasPrefix(path, original+string(filepath.Separator)) {
			relative, _ := filepath.Rel(original, path)
			allowed[relative] = digest
		}
	}
	if len(allowed) == 0 {
		return errors.New("agent: website program recovery inventory is empty")
	}
	seen := map[string]bool{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("agent: website program recovery contains a symlink")
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest, ok := allowed[relative]
		if !ok {
			return errors.New("agent: website program recovery contains an unowned file")
		}
		if _, err := readOwnedCaddyFile(path, digest, 64<<20); err != nil {
			return err
		}
		seen[relative] = true
		return nil
	})
	if err != nil {
		return err
	}
	if resources != nil && len(seen) != len(allowed) {
		return errors.New("agent: website program recovery is incomplete")
	}
	return nil
}

func (s *Store) completeSharedDaylight(ctx context.Context, task DeploymentTask) error {
	state, err := s.readCaddySite(ctx, task.ApplicationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	j := state.Handover
	if j == nil || j.TaskID != task.ID || j.State == "completed" {
		return nil
	}
	if j.State != "runtime_ready" || j.TargetManifest != task.ManifestSHA256 {
		return errors.New("agent: website handover is not ready to complete")
	}
	retained := j.RetainedRoot
	if _, err := os.Lstat(retained); err == nil {
		if err := verifyDaylightArtifactTree(retained, j.SourceRoot, j.BeforeReceipt.Resources, nil); err != nil {
			return err
		}
		if err := os.RemoveAll(retained); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	j.State = "completed"
	return s.saveCaddySite(ctx, state)
}

func (s *Store) recoverDaylightHandovers(ctx context.Context) error {
	states, err := s.sharedCaddyJournals(ctx)
	if err != nil {
		return err
	}
	for _, state := range states {
		j := state.Handover
		if j == nil || j.State == "completed" || j.State == "rolled_back" {
			continue
		}
		id := state.ApplicationID
		expectedRoot := filepath.Join("/opt/vastora/packages", packageIdentity(id), "artifacts")
		if j.SourceRoot != expectedRoot || j.RetainedRoot != expectedRoot+".shared-caddy-"+packageIdentity(j.TaskID) || j.BeforeReceipt.ApplicationID != id || !daylightKey(j.BeforeReceipt.AppKey) {
			return errors.New("agent: invalid website handover ownership")
		}
		executor := PackageExecutor{StateDirectory: s.dataDir}
		receipt, err := executor.ReadResources(id)
		if err != nil {
			return err
		}
		installed, err := s.AppliedInstallation(ctx, j.BeforeReceipt.AppKey)
		if err != nil {
			return err
		}
		// An already committed installation is retained for ordinary task
		// inspection. Never replay an upgrade just to recover its acknowledgement.
		if j.State == "runtime_ready" && installed.Version == j.TargetVersion && receipt.PackageVersion == j.TargetVersion && receipt.ManifestSHA256 == j.TargetManifest && receipt.State == "ready" {
			continue
		}
		if installed.Version != j.BeforeReceipt.PackageVersion {
			return errors.New("agent: website registration changed during handover recovery")
		}
		backend := &NativeServicesPackageBackend{Store: s, Manager: SystemdHostApplicationManager{}, StateDirectory: s.dataDir}
		h := &daylightHandover{backend: backend, journal: j}
		if err := h.rollback(ctx, receipt, func() error { return executor.save(receipt) }); err != nil {
			return err
		}
		receipt.State = "review-required"
		if err := executor.save(receipt); err != nil {
			return err
		}
	}
	return nil
}
