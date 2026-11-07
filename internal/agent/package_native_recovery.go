package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"strings"

	"github.com/petauron/vastora/internal/hostservice"
)

// Recovery replaces only the reviewed first-install inputs. No writer or
// unrecorded configuration edit may race that explicit correction.
func verifyNativeInstallRecovery(ctx context.Context, manager SystemdHostApplicationManager, prepared nativePreparedPackage, receipt *InstanceResources) error {
	for _, unit := range prepared.Plan.Units {
		if resourceNamed(receipt, "unit", unit.LogicalName) == nil {
			continue
		}
		if manager.InitSystem == hostservice.OpenRC {
			observed, err := manager.readPackageUnit(ctx, unit.Name)
			if err != nil || observed.MainPID != 0 {
				return errors.New("agent: stop the failed installation service before recovery")
			}
			continue
		}
		args := []string{"show", unit.Name, "--property=ActiveState,MainPID,ControlPID"}
		var raw []byte
		var err error
		if manager.ReadCommand != nil {
			raw, err = manager.ReadCommand(ctx, "systemctl", args...)
		} else {
			raw, err = exec.CommandContext(ctx, "systemctl", args...).Output()
		}
		if err != nil || len(raw) > 4096 {
			return errors.New("agent: failed installation service state is unavailable")
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			key, value, ok := strings.Cut(line, "=")
			valid := key == "ActiveState" && (value == "inactive" || value == "failed") || (key == "MainPID" || key == "ControlPID") && value == "0"
			if !ok || !valid || seen[key] {
				return errors.New("agent: stop the failed installation service before recovery")
			}
			seen[key] = true
		}
		if len(seen) != 3 {
			return errors.New("agent: failed installation service state is incomplete")
		}
	}
	for _, file := range prepared.Files {
		owned := resourceNamed(receipt, "file", file.LogicalName)
		if !file.ApplicationOwned || owned == nil {
			continue
		}
		snapshot, err := captureApplicationConfiguration(manager.path(file.Path))
		if err != nil || !snapshot.Exists {
			return errors.New("agent: failed installation configuration is unavailable")
		}
		digest := sha256.Sum256(snapshot.Data)
		if owned.SHA256 != hex.EncodeToString(digest[:]) {
			return errors.New("agent: failed installation configuration changed after review")
		}
	}
	return nil
}
