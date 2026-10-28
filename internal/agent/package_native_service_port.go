package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Verify this configure task changes service metadata only. Installed programs,
// units and application-owned configuration must match the signed declaration;
// no service is stopped or rewritten. Fail before the executor changes a receipt.
func (b *NativeServicesPackageBackend) verifyServicePortConfiguration(ctx context.Context, task DeploymentTask, prepared nativePreparedPackage, receipt *InstanceResources) error {
	invalid := errors.New("agent: native service port update differs from the ready installed bundle")
	if receipt == nil || receipt.State != "ready" || receipt.ManifestSHA256 != task.ManifestSHA256 || receipt.PackageRevision != task.PackageRevision || receipt.PackageVersion != task.Manifest.Version {
		return invalid
	}
	for _, file := range prepared.Files {
		owned := resourceNamed(receipt, file.Kind, file.LogicalName)
		if owned == nil || owned.Path != file.Path {
			return invalid
		}
		if file.ApplicationOwned {
			snapshot, err := captureApplicationConfiguration(b.Manager.path(file.Path))
			if err != nil || !snapshot.Exists || !bytes.Equal(snapshot.Data, file.Data) || snapshot.Mode.Perm() != file.Mode.Perm() {
				return invalid
			}
			continue
		}
		digest := sha256.Sum256(file.Data)
		if owned.SHA256 != hex.EncodeToString(digest[:]) {
			return invalid
		}
	}
	return b.probeServicePorts(ctx, task)
}

func (b *NativeServicesPackageBackend) probeServicePorts(ctx context.Context, task DeploymentTask) error {
	address := task.ServiceAddress
	if address == "" {
		address = "127.0.0.1"
	}
	found := false
	for _, service := range task.Manifest.Services {
		if service.HostPortField == "" {
			continue
		}
		if service.Protocol != "tcp" && service.Protocol != "http" && service.Protocol != "https" {
			return errors.New("agent: native service port protocol is unsupported")
		}
		found = true
		port, err := serviceHostPort(task.Config, service)
		if err != nil {
			return err
		}
		check := b.CheckHealth
		if check == nil {
			check = waitForServiceEndpoint
		}
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = check(probe, address, port, service)
		cancel()
		if err != nil {
			return errors.New("agent: changed native service port is not accepting connections")
		}
	}
	if !found {
		return errors.New("agent: native service has no configurable TCP port")
	}
	return nil
}
