package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"golang.org/x/mod/semver"
)

// The Center installer stops the co-located Agent first. This command reuses
// the Agent updater's verified checkpoint, database/key backup and manifest.
func runPrepareCenterUpgradeRecovery(arguments []string) error {
	flags := flag.NewFlagSet("agent prepare-center-upgrade-recovery", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
	executable := flags.String("executable", "/usr/local/bin/vastora", "installed Agent executable")
	sourceVersion := flags.String("source-version", "", "installed Agent version")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := requireLinuxRoot("agent prepare-center-upgrade-recovery"); err != nil {
		return err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*executable) {
		return errors.New("agent: installed executable must be an absolute path")
	}
	source := strings.TrimPrefix(strings.TrimSpace(*sourceVersion), "v")
	target := strings.TrimPrefix(strings.TrimSpace(agent.Version), "v")
	if !semver.IsValid("v"+source) || !semver.IsValid("v"+target) || semver.Compare("v"+source, "v"+target) > 0 {
		return errors.New("agent: invalid Center upgrade source or target version")
	}
	ctx := context.Background()
	installed, err := executableVersion(ctx, *executable)
	if err != nil || strings.TrimPrefix(strings.TrimSpace(installed), "v") != source {
		return errors.New("agent: installed executable does not match the recorded source version")
	}
	if _, err := stoppedHostUpdateHelper(ctx, runHostCommand); err != nil {
		return err
	}
	if _, err := os.Lstat(hostUpdateOperationPath); err == nil {
		return errors.New("agent: a previous Agent update requires inspection before Center upgrade")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := checkCenterUpgradeStoppedServices(ctx); err != nil {
		return err
	}
	directory, err := safeAgentDataDir(*dataDir)
	if err != nil {
		return err
	}
	if _, err := checkpointAgentDatabase(ctx, directory); err != nil {
		return err
	}
	identity, err := agent.InspectConnection(directory)
	if err != nil {
		return fmt.Errorf("agent: inspect Center upgrade identity: %w", err)
	}
	candidate, err := os.Executable()
	if err != nil {
		return err
	}
	operation := hostUpdateOperation{Attempt: 1, AgentID: identity.AgentID, DataDir: directory,
		Executable: *executable, SourceVersion: source, TargetVersion: target}
	if err := checkHostUpdateSpace(candidate, operation); err != nil {
		return err
	}
	database, err := centerUpgradeStateDigest(filepath.Join(directory, "agent.db"))
	if err != nil {
		return err
	}
	key, err := centerUpgradeStateDigest(filepath.Join(directory, "agent.key"))
	if err != nil {
		return err
	}
	previous, err := hashHostUpdateExecutable(*executable)
	if err != nil {
		return fmt.Errorf("agent: inspect the current executable: %w", err)
	}
	// A changed source state produces a different backup identity; retrying
	// unchanged state reuses and verifies the same recovery point.
	digest := sha256.Sum256([]byte(source + "\x00" + target + "\x00" + database.SHA256 + "\x00" + key.SHA256 + "\x00" + previous.SHA256))
	operation.TaskID = "agent-update-center-" + hex.EncodeToString(digest[:])
	parent := filepath.Join(directory, ".center-upgrade-recovery")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("agent: Center upgrade recovery directory is not protected")
	}
	recovery := hostUpdateRecoveryDirectory(parent, operation)
	if err := prepareHostUpdateRecovery(ctx, operation, recovery, candidate); err != nil {
		return fmt.Errorf("agent: prepare Center upgrade recovery: %w", err)
	}
	backedDB, err := hashHostUpdateRecoveryFile(filepath.Join(recovery, hostUpdateRecoveryDatabaseName))
	if err != nil {
		return err
	}
	backedKey, err := hashHostUpdateRecoveryFile(filepath.Join(recovery, hostUpdateRecoveryKeyName))
	if err != nil {
		return err
	}
	if backedDB != database || backedKey != key {
		return errors.New("agent: state changed while preparing Center upgrade recovery")
	}
	previousPath := filepath.Join(recovery, "source-vastora")
	if _, err := os.Lstat(previousPath); errors.Is(err, os.ErrNotExist) {
		if _, err := copyHostUpdateRecoveryFile(*executable, previousPath); err != nil {
			return fmt.Errorf("agent: preserve the previous executable: %w", err)
		}
		if err := syncHostUpdateDirectory(recovery); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	backedPrevious, err := hashHostUpdateRecoveryFile(previousPath)
	if err != nil || backedPrevious != previous {
		return errors.New("agent: preserved executable does not match the installed Agent")
	}
	// Print only the path so the installer can use this exact recovery point.
	fmt.Println(recovery)
	return nil
}

func checkCenterUpgradeStoppedServices(ctx context.Context) error {
	for _, unit := range []string{"vastora-agent.service", "vastora-agent-decommission.service"} {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		output, err := runHostCommand(checkCtx, "systemctl", "show", unit, "--property=LoadState,ActiveState,SubState,MainPID,ControlPID")
		cancel()
		if err != nil {
			return fmt.Errorf("agent: inspect %s before Center upgrade: %w", unit, err)
		}
		// A node without a decommission unit is fine, but the Agent unit must
		// be installed and fully stopped before checkpointing its database.
		raw := string(output)
		if unit == "vastora-agent-decommission.service" {
			raw = strings.Replace(raw, "LoadState=not-found\n", "LoadState=loaded\n", 1)
		}
		if !nativeStageAgentStopped(raw) {
			return fmt.Errorf("agent: %s must be stopped before Center upgrade", unit)
		}
	}
	return nil
}

func centerUpgradeStateDigest(path string) (hostUpdateRecoveryFile, error) {
	if err := requireHostUpdateRegularFile(path); err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	return hostUpdateRecoveryFile{Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
