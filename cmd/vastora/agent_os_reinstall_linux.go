//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/osreinstall"
	"github.com/petauron/vastora/internal/recovery"
)

type systemHostOSReinstaller struct{ dataDir, executable string }

func osReinstallerForHost(dataDir, executable string) agent.HostOSReinstaller {
	if dataDir != "/var/lib/vastora/agent" || executable != "/usr/local/bin/vastora" {
		return nil
	}
	return systemHostOSReinstaller{dataDir: dataDir, executable: executable}
}

func (u systemHostOSReinstaller) validate(request agent.HostOSReinstallRequest) error {
	if os.Geteuid() != 0 || u.dataDir != "/var/lib/vastora/agent" || u.executable != "/usr/local/bin/vastora" ||
		request.TaskID == "" || request.ExecutionID == "" || request.Task.Validate() != nil || request.Connection.AgentID == "" {
		return errors.New("agent: reinstall request is not supported")
	}
	key, err := controlplane.PublicKey(request.Connection.PrivateKey)
	if err != nil || recovery.Digest(key) != request.Task.IdentitySHA256 {
		return errors.New("agent: reinstall identity changed")
	}
	if _, err = hashHostUpdateExecutable(u.executable); err != nil {
		return err
	}
	return nil
}

func (u systemHostOSReinstaller) Backup(ctx context.Context, request agent.HostOSReinstallRequest) (osreinstall.BackupEvidence, error) {
	if err := u.validate(request); err != nil {
		return osreinstall.BackupEvidence{}, err
	}
	if missing := osreinstall.MissingPrerequisites(); len(missing) > 0 {
		return osreinstall.BackupEvidence{}, fmt.Errorf("agent: missing reinstall prerequisites: %s", strings.Join(missing, ", "))
	}
	plan, err := osreinstall.CaptureStaticNetworkPlan(ctx, request.Task.Inspection)
	if err != nil {
		return osreinstall.BackupEvidence{}, err
	}
	access, err := osreinstall.CaptureAccessSnapshot(ctx, request.Task.Username, request.Task.SSHPort, request.Task.Inspection.SSHHostKeySHA256)
	if err != nil {
		return osreinstall.BackupEvidence{}, err
	}
	dir, err := os.MkdirTemp("", "vastora-os-reinstall-backup-*")
	if err != nil {
		return osreinstall.BackupEvidence{}, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "agent.enc")
	artifact, err := recovery.ExportAgent(ctx, u.dataDir, "", agent.Version, file, request.Task.RecoveryPassword)
	if err != nil {
		return osreinstall.BackupEvidence{}, err
	}
	if artifact.Manifest.ComponentID != request.Connection.AgentID || artifact.Manifest.IdentityHash != request.Task.IdentitySHA256 {
		return osreinstall.BackupEvidence{}, errors.New("agent: backup identity changed")
	}
	ciphertext, err := os.ReadFile(file)
	if err != nil || len(ciphertext) > osreinstall.MaxBackupBytes {
		return osreinstall.BackupEvidence{}, errors.New("agent: Agent recovery backup exceeds its limit")
	}
	encryptedAccess, err := osreinstall.SealAccessSnapshot(access, request.Task.RecoveryPassword, request.TaskID)
	if err != nil {
		return osreinstall.BackupEvidence{}, err
	}
	if len(ciphertext)+len(encryptedAccess) > 700<<10 {
		return osreinstall.BackupEvidence{}, errors.New("agent: recovery backup exceeds its transfer limit")
	}
	return osreinstall.BackupEvidence{Ciphertext: ciphertext, AccessCiphertext: encryptedAccess, Digest: artifact.Digest, Network: plan}, nil
}

func (u systemHostOSReinstaller) Stage(ctx context.Context, request agent.HostOSReinstallRequest, backup osreinstall.BackupEvidence) (osreinstall.PreparedStage, error) {
	if err := u.validate(request); err != nil {
		return osreinstall.PreparedStage{}, err
	}
	plan, err := osreinstall.CaptureStaticNetworkPlan(ctx, request.Task.Inspection)
	if err != nil || !reflect.DeepEqual(plan, backup.Network) {
		return osreinstall.PreparedStage{}, errors.New("agent: reviewed network changed")
	}
	before, err := osreinstall.OpenAccessSnapshot(backup.AccessCiphertext, request.Task.RecoveryPassword, request.TaskID)
	if err != nil {
		return osreinstall.PreparedStage{}, err
	}
	current, err := osreinstall.CaptureAccessSnapshot(ctx, request.Task.Username, request.Task.SSHPort, request.Task.Inspection.SSHHostKeySHA256)
	if err != nil || !reflect.DeepEqual(before, current) {
		return osreinstall.PreparedStage{}, errors.New("agent: reviewed access changed")
	}
	binary, err := hashHostUpdateExecutable(u.executable)
	if err != nil {
		return osreinstall.PreparedStage{}, err
	}
	spec := osreinstall.InstallerSpec{StageDir: "/var/lib/vastora/os-reinstall/" + request.TaskID, TaskID: request.TaskID, ExecutionID: request.ExecutionID,
		Architecture: request.Task.Inspection.Architecture, BootMode: request.Task.Inspection.BootMode, Username: request.Task.Username, SSHPort: request.Task.SSHPort,
		SSHHostKeySHA256: request.Task.Inspection.SSHHostKeySHA256, SudoPolicy: current.SudoPolicy, Hostname: request.Task.Inspection.Hostname,
		AgentID: request.Connection.AgentID, IdentitySHA256: request.Task.IdentitySHA256, Disk: request.Task.Inspection.Disk, Network: plan}
	return osreinstall.PrepareInstallerAssets(ctx, osreinstall.StageInputs{Spec: spec, Inspection: request.Task.Inspection, ExecutableDigest: "sha256:" + binary.SHA256,
		EncryptedBackup: backup.Ciphertext, BackupDigest: backup.Digest, RecoveryPassword: request.Task.RecoveryPassword, Access: current})
}

func (u systemHostOSReinstaller) Activate(ctx context.Context, request agent.HostOSReinstallRequest, stage osreinstall.PreparedStage) error {
	if err := u.validate(request); err != nil {
		return err
	}
	digest, err := osreinstall.StageDigest(stage.Directory)
	if err != nil || digest != stage.Digest {
		return errors.New("agent: staged installer changed")
	}
	// A transient root systemd service survives this Agent process long enough
	// to create a single-use boot entry and restart; it receives no secrets.
	cmd := exec.CommandContext(ctx, "systemd-run", "--unit=vastora-os-reinstall-"+request.TaskID, "--property=Type=exec", "--property=Restart=no", "--collect",
		u.executable, "agent", "execute-os-reinstall", stage.Directory, stage.Digest)
	if err = cmd.Run(); err != nil {
		return errors.New("agent: reinstall helper did not start")
	}
	return nil
}

func executeOSReinstall(arguments []string) error {
	if len(arguments) != 2 || os.Geteuid() != 0 {
		return errors.New("agent: invalid reinstall helper")
	}
	dir, digest := arguments[0], arguments[1]
	if !strings.HasPrefix(dir, "/var/lib/vastora/os-reinstall/") || filepath.Clean(dir) != dir {
		return errors.New("agent: invalid reinstall stage")
	}
	actual, err := osreinstall.StageDigest(dir)
	if err != nil || actual != digest {
		return errors.New("agent: staged installer changed")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "stage-review.json"))
	if err != nil || len(raw) > 64<<10 {
		return errors.New("agent: stage review is unavailable")
	}
	var review osreinstall.StageReview
	if json.Unmarshal(raw, &review) != nil || review.Spec.StageDir != dir || filepath.Base(dir) != review.Spec.TaskID {
		return errors.New("agent: invalid stage review")
	}
	binary, err := hashHostUpdateExecutable("/usr/local/bin/vastora")
	if err != nil || "sha256:"+binary.SHA256 != review.ExecutableDigest {
		return errors.New("agent: Agent executable changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	plan, err := osreinstall.CaptureStaticNetworkPlan(ctx, review.Inspection)
	if err != nil || !reflect.DeepEqual(plan, review.Spec.Network) {
		return errors.New("agent: network changed before reinstall")
	}
	access, err := osreinstall.CaptureAccessSnapshot(ctx, review.Spec.Username, review.Spec.SSHPort, review.Spec.SSHHostKeySHA256)
	if err != nil {
		return err
	}
	rootKeys := access.RootKeys
	if review.Spec.Username == "root" {
		rootKeys = access.AuthorizedKeys
	}
	if len(rootKeys) == 0 {
		rootKeys = []byte("\n")
	}
	for name, expected := range map[string][]byte{"ssh-config.tar.gz": access.SSHArchive, "ssh_keys": access.AuthorizedKeys, "root-authorized-keys": rootKeys, "agent-unit-before.service": access.AgentUnit} {
		data, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil || !bytes.Equal(data, expected) {
			return errors.New("agent: access changed before reinstall")
		}
	}
	for _, name := range []string{"/reinstall-vmlinuz", "/reinstall-initrd", "/reinstall-tmp", "/boot/reinstall-vmlinuz", "/boot/reinstall-initrd"} {
		if _, e := os.Lstat(name); !errors.Is(e, os.ErrNotExist) {
			return errors.New("agent: another reinstall preparation exists")
		}
	}
	output, err := exec.CommandContext(ctx, "grub-editenv", "/boot/grub/grubenv", "list").Output()
	if err != nil || strings.Contains(string(output), "next_entry=") {
		return errors.New("agent: boot configuration requires review")
	}
	custom, err := os.ReadFile("/boot/grub/custom.cfg")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("agent: boot configuration requires review")
	}
	if bytes.Contains(custom, []byte("### BEGIN reinstall.sh ###")) {
		return errors.New("agent: an existing reinstall boot entry requires review")
	}
	args := []string{filepath.Join(dir, "reinstall-hardened.sh"), "debian", "13", "--username", review.Spec.Username, "--ssh-port", strconv.Itoa(review.Spec.SSHPort), "--ssh-key", filepath.Join(dir, "ssh_keys")}
	command := exec.CommandContext(ctx, "bash", args...)
	if err = command.Run(); err != nil {
		return errors.New("agent: verified installer preparation failed")
	}
	for _, name := range []string{"/reinstall-initrd", "/reinstall-vmlinuz"} {
		info, e := os.Lstat(name)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("agent: prepared boot material is not private")
		}
	}
	output, err = exec.CommandContext(ctx, "grub-editenv", "/boot/grub/grubenv", "list").Output()
	if err != nil || strings.Count("\n"+string(output), "\nnext_entry=reinstall (debian 13)\n") != 1 {
		return errors.New("agent: one-time reinstall boot entry is missing")
	}
	if err = exec.CommandContext(ctx, "systemctl", "reboot").Run(); err != nil {
		return fmt.Errorf("agent: reboot request failed: %w", err)
	}
	return nil
}
