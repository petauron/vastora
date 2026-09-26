package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/networking"
)

// InstanceResources is durable ownership evidence, not a deployment recipe.
// It deliberately contains no configuration values or credential contents.
type InstanceResources struct {
	IntegrationState       string            `json:"integrationState,omitempty"`
	Version                int               `json:"version"`
	ApplicationID          string            `json:"applicationId"`
	AppKey                 string            `json:"appKey"`
	Runtime                string            `json:"runtime"`
	PackageVersion         string            `json:"packageVersion"`
	PackageRevision        int               `json:"packageRevision"`
	ManifestSHA256         string            `json:"manifestSha256"`
	AuthorizedCapabilities []string          `json:"authorizedCapabilities,omitempty"`
	State                  string            `json:"state"`
	TaskID                 string            `json:"taskId"`
	Resources              []RuntimeResource `json:"resources"`
	Backups                []RuntimeBackup   `json:"backups,omitempty"`
	RecordedAt             time.Time         `json:"recordedAt"`
}

type RuntimeResource struct {
	Kind         string `json:"kind"`
	LogicalName  string `json:"logicalName"`
	Name         string `json:"name"`
	ID           string `json:"id,omitempty"`
	Path         string `json:"path,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Persistent   bool   `json:"persistent,omitempty"`
	StartedAt    string `json:"startedAt,omitempty"`
	MainPID      int    `json:"mainPid,omitempty"`
	InvocationID string `json:"invocationId,omitempty"`
	Component    string `json:"component,omitempty"`
}

type RuntimeBackup struct {
	ID             string    `json:"id"`
	Target         string    `json:"target,omitempty"`
	Resource       string    `json:"resource"`
	Path           string    `json:"path"`
	SHA256         string    `json:"sha256"`
	PackageVersion string    `json:"packageVersion"`
	CreatedAt      time.Time `json:"createdAt"`
}

// PackageBackend implements concrete resource operations. The executor owns
// ordering, durable intent, permissions and refusal to replay uncertain effects.
// Prepare must be read-only except downloading into its private staging area.
type PackageBackend interface {
	Prepare(context.Context, DeploymentTask, *InstanceResources) error
	Inspect(context.Context, DeploymentTask, *InstanceResources) error
	Backup(context.Context, DeploymentTask, *InstanceResources) error
	Apply(context.Context, DeploymentTask, *InstanceResources, func() error) error
	Healthy(context.Context, DeploymentTask, *InstanceResources) error
	Remove(context.Context, DeploymentTask, *InstanceResources) error
}

type PackageExecutor struct {
	StateDirectory string
	Backend        PackageBackend
}

func packageIdentity(id string) string {
	hash := sha256.Sum256([]byte(id))
	return "vastora-pkg-" + hex.EncodeToString(hash[:12])
}

func (e PackageExecutor) receiptPath(id string) string {
	base := e.StateDirectory
	// Resolve the caller-selected state root once (macOS /var is a platform
	// symlink); symlinks inside the managed namespace are still rejected below.
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	return filepath.Join(base, "packages", packageIdentity(id), "resources.json")
}

func (e PackageExecutor) ReadResources(id string) (*InstanceResources, error) {
	if e.StateDirectory == "" || id == "" {
		return nil, errors.New("agent: package state directory and identity are required")
	}
	path := e.receiptPath(id)
	if err := checkPackageParents(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4<<20 {
		return nil, errors.New("agent: unsafe package receipt")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var receipt InstanceResources
	if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || receipt.ApplicationID != id {
		return nil, errors.New("agent: invalid package receipt")
	}
	return &receipt, nil
}

func (e PackageExecutor) save(receipt *InstanceResources) error {
	path := e.receiptPath(receipt.ApplicationID)
	if err := checkPackageParents(path); err != nil {
		return err
	}
	receipt.RecordedAt = time.Now().UTC()
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return writeHostFileAtomic(path, raw, 0600)
}

// Check every existing parent, not just the leaf: a managed state path must
// never traverse a symlink planted by an application user.
func checkPackageParents(path string) error {
	for current := filepath.Dir(path); current != "/" && current != "."; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("agent: package path traverses a non-directory or symlink")
		}
	}
	return nil
}

func validatePackageTask(task DeploymentTask) error {
	if task.ID == "" || task.ApplicationID == "" || task.AppKey == "" {
		return errors.New("agent: application task identity is required")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]{1,256}$`).MatchString(task.ApplicationID) || strings.ContainsAny(task.ID+task.AppKey, "\r\n\x00") {
		return errors.New("agent: invalid application task identity")
	}
	if !slices.Contains([]string{"install", "upgrade", "configure", "uninstall", "adopt"}, task.Operation) {
		return errors.New("agent: unsupported package operation")
	}
	if task.DeleteData && task.Operation != "uninstall" {
		return errors.New("agent: data deletion requires uninstall")
	}
	if task.Operation == "adopt" {
		return nil
	} // Historical manifest is evidence, not an executable v4 recipe.
	if task.Operation == "uninstall" && task.Manifest.Runtime == nil && task.PackageRevision == 0 {
		return validateHistoricalPackageTask(task)
	}
	if err := catalog.ValidateApp(task.Manifest); err != nil {
		return fmt.Errorf("agent: invalid package: %w", err)
	}
	if !strings.HasSuffix(task.AppKey, "/"+task.Manifest.ID) || task.PackageRevision != task.Manifest.PackageRevision {
		return errors.New("agent: package identity mismatch")
	}
	canonical, err := catalog.CanonicalAppManifest(task.Manifest)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if task.ManifestSHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("agent: package manifest digest mismatch")
	}
	runtime := task.Manifest.Runtime
	if runtime == nil || runtime.Version != 1 || runtime.Kind != "docker" && runtime.Kind != "systemd" {
		return errors.New("agent: unsupported package executor protocol")
	}
	for _, capability := range runtime.RequiredCapabilities {
		if !slices.Contains([]string{"root", "host-network", "host-path", "devices", "meridian-runtime"}, capability) {
			return errors.New("agent: unsupported runtime capability")
		}
		if capability == "meridian-runtime" && task.AppKey != meridianKey {
			return errors.New("agent: Meridian integration capability is reserved for its reviewed package")
		}
		if !slices.Contains(task.AuthorizedCapabilities, capability) {
			return errors.New("agent: runtime capability requires explicit administrator authorization: " + capability)
		}
	}
	if len(task.AuthorizedCapabilities) != len(runtime.RequiredCapabilities) {
		return errors.New("agent: runtime authorization must exactly match required capabilities")
	}
	if err := catalog.ValidateAuthorizedCapabilities(task.Manifest, task.AuthorizedCapabilities); err != nil {
		return err
	}
	address := task.ServiceAddress
	if address == "" {
		address = "127.0.0.1"
	}
	if !networking.IsPrivateServiceAddress(address) {
		return errors.New("agent: package services must bind to a private address")
	}
	if task.Operation != "uninstall" {
		if _, _, err := runtimeInputs(task); err != nil {
			return err
		}
	}
	return nil
}

func (e PackageExecutor) Deploy(ctx context.Context, task DeploymentTask) (result ApplicationTaskResult, err error) {
	if err := validatePackageTask(task); err != nil {
		return result, err
	}
	if e.StateDirectory == "" || e.Backend == nil {
		return result, errors.New("agent: package executor is not configured")
	}
	receipt, err := e.ReadResources(task.ApplicationID)
	if errors.Is(err, os.ErrNotExist) {
		receipt, err = nil, nil
	}
	if err != nil {
		return result, err
	}
	if receipt != nil && (receipt.AppKey != task.AppKey || receipt.State != "ready" && receipt.State != "retained") {
		return result, errors.New("agent: package ownership conflict or unfinished execution requires review")
	}
	if task.Operation == "adopt" {
		if receipt != nil {
			if err := e.Backend.Inspect(ctx, task, receipt); err != nil {
				return result, err
			}
			return ApplicationTaskResult{Resources: receipt}, nil
		}
		if task.Resources == nil || task.Resources.ApplicationID != task.ApplicationID || task.Resources.AppKey != task.AppKey || task.Resources.Version != 1 || len(task.Resources.Resources) == 0 {
			return result, errors.New("agent: adoption requires historical resource evidence")
		}
		copy := *task.Resources
		copy.Resources = slices.Clone(task.Resources.Resources)
		// The backend verifies stable IDs, existing labels/unit ownership and
		// file digests. It must never adopt based only on a guessed name.
		if err := e.Backend.Inspect(ctx, task, &copy); err != nil {
			return result, err
		}
		copy.State, copy.TaskID = "ready", task.ID
		if err := e.save(&copy); err != nil {
			return result, err
		}
		return ApplicationTaskResult{Resources: &copy}, nil
	}
	if task.Operation != "install" && receipt == nil {
		return result, errors.New("agent: existing instance has no verified resource receipt; adopt it before mutation")
	}
	if task.Operation == "install" && receipt != nil && receipt.State != "retained" {
		return result, errors.New("agent: application is already installed")
	}
	if receipt == nil {
		receipt = &InstanceResources{Version: 1, ApplicationID: task.ApplicationID, AppKey: task.AppKey, Runtime: task.Manifest.Runtime.Kind}
	} else if task.Manifest.Runtime != nil && receipt.Runtime != task.Manifest.Runtime.Kind {
		return result, errors.New("agent: changing executor requires explicit instance migration")
	}
	if task.Operation == "uninstall" && (receipt.ManifestSHA256 != task.ManifestSHA256 || receipt.PackageRevision != task.PackageRevision || receipt.PackageVersion != task.Manifest.Version) {
		return result, errors.New("agent: uninstall must identify the installed receipt, not a catalog replacement")
	}
	if len(receipt.Resources) > 0 {
		if err := e.Backend.Inspect(ctx, task, receipt); err != nil {
			return result, err
		}
	}
	if err := e.Backend.Prepare(ctx, task, receipt); err != nil {
		return result, err
	}
	receipt.State, receipt.TaskID = "prepared", task.ID
	if err := e.save(receipt); err != nil {
		return result, err
	}
	result.Resources = receipt
	defer func() {
		if err != nil {
			receipt.State = "review-required"
			err = uncertainTaskOutcome(errors.Join(err, e.save(receipt)))
		}
	}()
	if task.Operation == "uninstall" {
		if err = e.Backend.Remove(ctx, task, receipt); err != nil {
			return result, err
		}
		receipt.State = "retained"
		if task.DeleteData {
			receipt.State = "removed"
		}
		return result, e.save(receipt)
	}
	if task.Operation != "install" {
		receipt.State = "backing-up"
		if err = e.save(receipt); err != nil {
			return result, err
		}
		if err = e.Backend.Backup(ctx, task, receipt); err != nil {
			return result, err
		}
	}
	receipt.State = "applying"
	if err = e.save(receipt); err != nil {
		return result, err
	}
	if err = e.Backend.Apply(ctx, task, receipt, func() error { return e.save(receipt) }); err != nil {
		return result, err
	}
	if err = e.Backend.Healthy(ctx, task, receipt); err != nil {
		return result, err
	}
	receipt.PackageVersion, receipt.PackageRevision, receipt.ManifestSHA256 = task.Manifest.Version, task.PackageRevision, task.ManifestSHA256
	receipt.AuthorizedCapabilities = slices.Clone(task.AuthorizedCapabilities)
	receipt.State = "ready"
	if err = e.save(receipt); err != nil {
		return result, err
	}
	result.Services = make([]ApplicationServiceResult, 0, len(task.Manifest.Services))
	for _, service := range task.Manifest.Services {
		port, portErr := serviceHostPort(task.Config, service)
		if portErr != nil {
			return result, portErr
		}
		address := task.ServiceAddress
		if address == "" {
			address = "127.0.0.1"
		}
		result.Services = append(result.Services, ApplicationServiceResult{Name: service.Name, Protocol: service.Protocol, ContainerPort: service.ContainerPort, HostPort: port, Address: address})
	}
	return result, nil
}

func validateHistoricalPackageTask(task DeploymentTask) error {
	if len(task.HistoricalManifest) == 0 || len(task.HistoricalManifest) > 4<<20 || task.ManifestSHA256 == "" {
		return errors.New("agent: historical package bytes are required")
	}
	digest := sha256.Sum256(task.HistoricalManifest)
	var historical catalog.AppManifest
	if hex.EncodeToString(digest[:]) != task.ManifestSHA256 || json.Unmarshal(task.HistoricalManifest, &historical) != nil {
		return errors.New("agent: historical raw manifest digest mismatch")
	}
	if historical.Runtime != nil || historical.PackageRevision != 0 || !strings.HasSuffix(task.AppKey, "/"+historical.ID) {
		return errors.New("agent: invalid historical package identity")
	}
	left, _ := json.Marshal(historical)
	right, _ := json.Marshal(task.Manifest)
	if string(left) != string(right) {
		return errors.New("agent: historical package body mismatch")
	}
	return nil
}
