package agent

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// openNativeBackup authenticates an already recorded immutable snapshot without
// allocating its entire contents. The returned descriptor stays open through
// extraction, so a path replacement cannot substitute a different archive.
func openNativeBackup(directory string, task DeploymentTask, backup RuntimeBackup) (*os.File, error) {
	expected := filepath.Join(directory, "packages", packageIdentity(task.ApplicationID), "backups", packageIdentity(backup.ID), packageIdentity(backup.Resource)+".tar")
	if backup.ID == "" || backup.Path != expected {
		return nil, errors.New("agent: native backup is outside recorded namespace")
	}
	if err := checkPackageParents(backup.Path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(backup.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 512<<20 {
		return nil, errors.New("agent: unsafe native backup file")
	}
	f, err := os.Open(backup.Path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) { _ = f.Close(); return nil, err }
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fail(errors.New("agent: native backup changed during open"))
	}
	digest := sha256.New()
	size, err := io.Copy(digest, io.LimitReader(f, (512<<20)+1))
	if err != nil || size != info.Size() || hex.EncodeToString(digest.Sum(nil)) != backup.SHA256 {
		return fail(errors.New("agent: native backup integrity mismatch"))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	if err := validateBackupTarStream(f); err != nil {
		return fail(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return f, nil
}

type nativeRestoreTarget struct {
	resource                   RuntimeResource
	staged, original, previous string
}

func (b *NativeServicesPackageBackend) Restore(ctx context.Context, task DeploymentTask, receipt *InstanceResources, backups []RuntimeBackup, persist func() error) error {
	if err := b.Inspect(ctx, task, receipt); err != nil {
		return err
	}
	plan, err := planNativePackage(task.ApplicationID, task.Manifest)
	if err != nil {
		return err
	}
	// The maintenance transaction must already have stopped every writer and
	// taken a new pre-restore snapshot. Never modify data under a running writer.
	for _, unit := range plan.Units {
		if resourceNamed(receipt, "unit", unit.LogicalName) != nil {
			observed, err := b.Manager.readPackageUnit(ctx, unit.Name)
			if err != nil || observed.MainPID != 0 {
				return errors.New("agent: native restore requires stopped writers")
			}
		}
	}
	selected := map[string]RuntimeBackup{}
	for _, backup := range backups {
		if backup.PackageVersion != receipt.PackageVersion || selected[backup.Resource].ID != "" {
			return errors.New("agent: native restore version or duplicate snapshot mismatch")
		}
		selected[backup.Resource] = backup
	}
	var targets []nativeRestoreTarget
	for _, resource := range receipt.Resources {
		expected, err := expectedNativeResource(task.ApplicationID, plan, resource)
		if err != nil {
			return err
		}
		if resource.Kind != "directory" && !expected.ApplicationOwned {
			continue
		}
		if resource.Kind == "directory" && !resource.Persistent {
			continue
		}
		backup, ok := selected[resource.Kind+":"+resource.LogicalName]
		if !ok || backup.Target != resource.Path {
			return errors.New("agent: native restore must cover every configuration and persistent directory")
		}
		fresh := false
		for _, before := range receipt.Backups {
			if before.ID == task.ID && before.Resource == resource.Kind+":"+resource.LogicalName && before.Target == resource.Path && before.PackageVersion == receipt.PackageVersion {
				fresh = true
			}
		}
		if !fresh {
			return errors.New("agent: native restore requires a recorded pre-restore snapshot")
		}
		original := b.Manager.path(resource.Path)
		staged, previous := original+".restore-"+packageIdentity(task.ID), original+".retained-"+packageIdentity(task.ID)
		for _, path := range []string{staged, previous} {
			if err := checkPackageParents(path); err != nil {
				return err
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return errors.New("agent: previous native recovery residue requires review")
			}
		}
		info, err := os.Lstat(original)
		if err != nil {
			return err
		}
		archive, err := openNativeBackup(b.StateDirectory, task, backup)
		if err != nil {
			return err
		}
		if resource.Kind == "directory" {
			err = extractValidatedBackupDirectory(archive, staged, info)
		} else {
			err = extractNativeConfiguration(archive, staged, filepath.Base(original), info.Mode().Perm())
		}
		closeErr := archive.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return err
		}
		targets = append(targets, nativeRestoreTarget{resource: resource, staged: staged, original: original, previous: previous})
	}
	// Stage every resource before the first replacement. Originals are retained,
	// never deleted or silently restored after a partial application failure.
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Rename(target.original, target.previous); err != nil {
			return err
		}
		if err := os.Rename(target.staged, target.original); err != nil {
			return err
		}
		if target.resource.Kind == "file" {
			file, err := os.Open(target.original)
			if err != nil {
				return err
			}
			hash := sha256.New()
			_, hashErr := io.Copy(hash, file)
			closeErr := file.Close()
			if err := errors.Join(hashErr, closeErr); err != nil {
				return err
			}
			resourceNamed(receipt, "file", target.resource.LogicalName).SHA256 = hex.EncodeToString(hash.Sum(nil))
		}
		if err := persist(); err != nil {
			return err
		}
	}
	return b.Resume(ctx, task, receipt)
}

func extractNativeConfiguration(source io.Reader, destination, expectedName string, mode os.FileMode) error {
	reader := tar.NewReader(source)
	header, err := reader.Next()
	if err != nil || header.Name != expectedName || (header.Typeflag != tar.TypeReg && header.Typeflag != 0) {
		return errors.New("agent: native configuration backup member mismatch")
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode&0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, reader)
	syncErr, closeErr := f.Sync(), f.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	if _, err := reader.Next(); err != io.EOF {
		return errors.New("agent: native configuration backup contains extra members")
	}
	return nil
}
