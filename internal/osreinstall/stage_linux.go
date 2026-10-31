//go:build linux

package osreinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// StageInputs are local, reviewed values. The encrypted Agent backup must
// already have a durable, authenticated Center acknowledgment before staging.
// The caller must verify the exact original service unit and SSH policy.
type StageInputs struct {
	Spec             InstallerSpec
	EncryptedBackup  []byte
	BackupDigest     string
	RecoveryPassword string
	Access           AccessSnapshot
	Inspection       Inspection
	ExecutableDigest string
}

func rootPrivateDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

func rootOwnedDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

func writeStageFile(dir, name string, data []byte) error {
	if filepath.Base(name) != name || len(data) == 0 {
		return errors.New("reinstall: invalid staging material")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, writeErr := file.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}

// PrepareInstallerAssets fetches only the pinned sources and Debian materials
// into a fresh, root-only task directory. It does not execute the installer,
// modify a boot entry, restart services or erase a disk.
func PrepareInstallerAssets(ctx context.Context, input StageInputs) (PreparedStage, error) {
	bad := func() (PreparedStage, error) {
		return PreparedStage{}, errors.New("reinstall: installer staging rejected")
	}
	dir := input.Spec.StageDir
	const prefix = "/var/lib/vastora/os-reinstall/"
	id := strings.TrimPrefix(dir, prefix)
	if os.Geteuid() != 0 || !strings.HasPrefix(dir, prefix) || !safeAgentID.MatchString(id) || id != input.Spec.TaskID ||
		input.Inspection.Validate() != nil || input.Inspection.Disk != input.Spec.Disk || !safeAgentHash.MatchString(input.ExecutableDigest) ||
		filepath.Clean(dir) != dir || len(input.EncryptedBackup) == 0 || len(input.EncryptedBackup) > MaxBackupBytes ||
		len(input.RecoveryPassword) < 32 || !safeAgentID.MatchString(input.RecoveryPassword) ||
		ValidateAccessSnapshot(input.Access, input.Spec.Username, input.Spec.SSHPort, input.Spec.SSHHostKeySHA256) != nil || input.Access.SudoPolicy != input.Spec.SudoPolicy {
		return bad()
	}
	sum := sha256.Sum256(input.EncryptedBackup)
	if input.BackupDigest != "sha256:"+hex.EncodeToString(sum[:]) {
		return bad()
	}
	// The existing Agent data root is owned by root. Do not follow a substituted
	// directory or overwrite a previous attempt's recovery material.
	parent := "/var/lib/vastora"
	if !rootOwnedDirectory("/var") || !rootOwnedDirectory("/var/lib") || !rootOwnedDirectory(parent) {
		return bad()
	}
	base := filepath.Join(parent, "os-reinstall")
	if err := os.Mkdir(base, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return bad()
	}
	if !rootPrivateDirectory(base) {
		return bad()
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(base, &fs) != nil || fs.Bsize <= 0 || fs.Bavail < (512<<20)/uint64(fs.Bsize) {
		return bad()
	}
	if err := os.Mkdir(dir, 0o700); err != nil || !rootPrivateDirectory(dir) {
		return bad()
	}
	sources := make(map[string][]byte, len(reinstallSourceHashes))
	names := make([]string, 0, len(reinstallSourceHashes))
	for name := range reinstallSourceHashes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := FetchReinstallSource(ctx, name)
		if err != nil {
			return PreparedStage{}, err
		}
		sources[name] = data
		if err = writeStageFile(dir, name, data); err != nil {
			return PreparedStage{}, err
		}
	}
	scripts, err := BuildInstallerScripts(sources, input.Spec)
	if err != nil {
		return PreparedStage{}, err
	}
	marker := ReconnectMarker{TaskID: input.Spec.TaskID, ExecutionID: input.Spec.ExecutionID}
	if marker.Validate() != nil {
		return bad()
	}
	markerJSON, err := json.Marshal(marker)
	if err != nil {
		return PreparedStage{}, err
	}
	reviewJSON, err := json.Marshal(StageReview{Spec: input.Spec, Inspection: input.Inspection, ExecutableDigest: input.ExecutableDigest})
	if err != nil {
		return PreparedStage{}, err
	}
	rootKeys := input.Access.RootKeys
	if input.Spec.Username == "root" {
		rootKeys = input.Access.AuthorizedKeys
	}
	if len(rootKeys) == 0 {
		rootKeys = []byte("\n")
	}
	for name, data := range map[string][]byte{
		"reinstall-hardened.sh":     scripts.Installer,
		"debian-hardened.cfg":       scripts.Preseed,
		"network-adapter.sh":        scripts.Network,
		"fix-eth-name.hardened.sh":  scripts.FirstBoot,
		"network.rules":             scripts.Link,
		"network.interfaces":        scripts.Interfaces,
		"resolv-before.conf":        scripts.Resolver,
		"agent-backup.enc":          input.EncryptedBackup,
		"recovery.password":         []byte(input.RecoveryPassword + "\n"),
		"ssh_keys":                  input.Access.AuthorizedKeys,
		"agent-unit-before.service": input.Access.AgentUnit,
		"ssh-config.tar.gz":         input.Access.SSHArchive,
		"root-authorized-keys":      rootKeys,
		"reconnect.json":            markerJSON,
		"stage-review.json":         reviewJSON,
	} {
		if err := writeStageFile(dir, name, data); err != nil {
			return PreparedStage{}, err
		}
	}
	for _, kind := range []string{"linux", "initrd", "fdisk"} {
		if _, err := DownloadDebianInstallerAsset(ctx, input.Spec.Architecture, kind, dir); err != nil {
			return PreparedStage{}, err
		}
	}
	digest, err := StageDigest(dir)
	if err != nil {
		return PreparedStage{}, err
	}
	return PreparedStage{Directory: dir, Digest: digest}, nil
}

// StageDigest binds file names, lengths and bytes, and refuses extra members,
// symlinks, unsafe modes or changes while the manifest is being read.
func StageDigest(dir string) (string, error) {
	bad := func() (string, error) { return "", errors.New("reinstall: staged materials changed") }
	if !rootPrivateDirectory(dir) {
		return bad()
	}
	expected := map[string]bool{}
	for name := range reinstallSourceHashes {
		expected[name] = true
	}
	for _, name := range []string{"reinstall-hardened.sh", "debian-hardened.cfg", "network-adapter.sh", "fix-eth-name.hardened.sh", "network.rules", "network.interfaces", "resolv-before.conf", "agent-backup.enc", "recovery.password", "ssh_keys", "agent-unit-before.service", "reconnect.json", "ssh-config.tar.gz", "root-authorized-keys", "stage-review.json", "debian13-amd64-linux", "debian13-amd64-initrd", "debian13-amd64-fdisk"} {
		expected[name] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != len(expected) {
		return bad()
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	hash := sha256.New()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return bad()
	}
	defer root.Close()
	for _, entry := range entries {
		name := entry.Name()
		if !expected[name] || entry.Type()&os.ModeSymlink != 0 {
			return bad()
		}
		file, err := root.Open(name)
		if err != nil {
			return bad()
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return bad()
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > 64<<20 {
			file.Close()
			return bad()
		}
		fmt.Fprintf(hash, "%s:%d\n", name, info.Size())
		n, readErr := io.Copy(hash, io.LimitReader(file, info.Size()+1))
		after, statErr := file.Stat()
		closeErr := file.Close()
		if readErr != nil || statErr != nil || closeErr != nil || n != info.Size() || !os.SameFile(info, after) || after.Size() != info.Size() {
			return bad()
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
