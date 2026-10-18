package agent

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"time"
)

func (b *NativeServicesPackageBackend) Backup(ctx context.Context, task DeploymentTask, receipt *InstanceResources) (resultErr error) {
	if err := b.Inspect(ctx, task, receipt); err != nil {
		return err
	}
	plan, err := planNativePackage(task.ApplicationID, task.Manifest)
	if err != nil {
		return err
	}
	b.resumeUnits = map[string]bool{}
	for _, unit := range plan.Units {
		observed, err := b.Manager.readPackageUnit(ctx, unit.Name)
		if err != nil {
			return err
		}
		b.resumeUnits[unit.Name] = observed.MainPID > 0
	}
	// Backup changes no application bytes. If snapshotting fails, resume the
	// exact previously running set using a fresh bounded context, not a downgrade.
	defer func() {
		if resultErr != nil {
			recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			resultErr = errors.Join(resultErr, b.Resume(recovery, task, receipt))
		}
	}()
	for _, automatic := range []bool{true, false} {
		for _, unit := range plan.Units {
			if unit.Automatic != automatic {
				continue
			}
			if err := b.Manager.run(ctx, "systemctl", "stop", unit.Name); err != nil {
				return errors.New("agent: native writer could not stop for backup")
			}
		}
	}
	// Require stopped evidence for every service before opening any snapshot.
	for _, unit := range plan.Units {
		observed, err := b.Manager.readPackageUnit(ctx, unit.Name)
		if err != nil || observed.MainPID != 0 {
			return errors.New("agent: native writer remains active during backup")
		}
	}
	for _, resource := range receipt.Resources {
		identity := resource
		identity.Name = resource.Kind + ":" + resource.LogicalName
		// Stream archive data to disk. A small-memory node must not hold an
		// entire (potentially hundreds of MiB) state snapshot in RAM.
		reader, stream := io.Pipe()
		finished := make(chan error, 1)
		go func() {
			archiveErr := b.archiveNativeResource(ctx, stream, resource)
			_ = stream.CloseWithError(archiveErr)
			finished <- archiveErr
		}()
		backup, saveErr := savePackageBackup(b.StateDirectory, task, identity, receipt.PackageVersion, reader)
		_ = reader.CloseWithError(saveErr)
		if err := errors.Join(saveErr, <-finished); err != nil {
			return err
		}
		receipt.Backups = append(receipt.Backups, backup)
	}
	return nil
}

func (b *NativeServicesPackageBackend) archiveNativeResource(ctx context.Context, destination io.Writer, resource RuntimeResource) error {
	writer := tar.NewWriter(destination)
	var err error
	if resource.Kind == "directory" {
		if err := archivePackageDirectory(ctx, writer, b.Manager.path(resource.Path)); err != nil {
			return err
		}
	} else {
		var raw []byte
		name := filepath.Base(resource.Path)
		if resource.Kind == "user" {
			// Identity metadata, never passwd/shadow content or passwords.
			raw, err = json.Marshal(resource)
			name = "account.json"
		} else {
			var snapshot hostFileSnapshot
			snapshot, err = captureHostFile(b.Manager.path(resource.Path))
			raw = snapshot.Data
			if err == nil && !snapshot.Exists {
				err = errors.New("agent: native backup file disappeared")
			}
		}
		if err != nil {
			return err
		}
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(raw))}); err != nil {
			return err
		}
		if _, err := writer.Write(raw); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return nil
}
