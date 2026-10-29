package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/tailscalehost"
	"golang.org/x/mod/semver"
)

const tailscaleAptSource = "/etc/apt/sources.list.d/tailscale.list"

// Only the existing signed, official stable source is accepted. No repository
// is added, no key is replaced, and no custom source or shell is executed.
func tailscaleAptSourceValid() error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("Tailscale updates require a root Linux Agent")
	}
	for _, executable := range []string{"apt-get", "apt-cache", "dpkg-query", "systemd-run", "systemctl", "tailscale"} {
		if _, err := exec.LookPath(executable); err != nil {
			return errors.New("Tailscale update tools are unavailable")
		}
	}
	raw, err := os.ReadFile(tailscaleAptSource)
	if err != nil || len(raw) > 4096 {
		return errors.New("official Tailscale APT source is unavailable")
	}
	lines := strings.Split(string(raw), "\n")
	entries := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries++
		fields := strings.Fields(line)
		if len(fields) != 5 || fields[0] != "deb" || fields[1] != "[signed-by=/usr/share/keyrings/tailscale-archive-keyring.gpg]" ||
			(fields[2] != "https://pkgs.tailscale.com/stable/debian" && fields[2] != "https://pkgs.tailscale.com/stable/ubuntu") || fields[4] != "main" {
			return errors.New("Tailscale APT source is not the supported official stable source")
		}
		for _, ch := range fields[3] {
			if ch < 'a' || ch > 'z' {
				return errors.New("invalid Tailscale APT suite")
			}
		}
		if fields[3] == "" {
			return errors.New("invalid Tailscale APT suite")
		}
	}
	if entries != 1 {
		return errors.New("Tailscale APT source is ambiguous")
	}
	key, err := os.Stat("/usr/share/keyrings/tailscale-archive-keyring.gpg")
	if err != nil || !key.Mode().IsRegular() {
		return errors.New("Tailscale APT signing key is unavailable")
	}
	return nil
}

var tailscaleAptOptions = []string{"-o", "Dir::Etc::SourceList=sources.list.d/tailscale.list", "-o", "Dir::Etc::SourceParts=-", "-o", "APT::Get::List-Cleanup=0", "-o", "Acquire::AllowInsecureRepositories=false", "-o", "Acquire::AllowDowngradeToInsecureRepositories=false", "-o", "APT::Get::AllowUnauthenticated=false"}

func prepareTailscaleUpdate(ctx context.Context, taskID string, task tailscalehost.UpdateTask) error {
	if task.Validate() != nil || tailscalehost.UpdateUnitName(taskID) == "" {
		return errors.New("agent: invalid Tailscale update task")
	}
	if err := tailscaleAptSourceValid(); err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, "tailscale", "version", "--json", "--daemon").Output()
	versions, parseErr := tailscalehost.ParseVersions(output, true)
	if err != nil || parseErr != nil || versions.Client != versions.Daemon || semver.Compare("v"+versions.Client, "v"+task.TargetVersion) >= 0 {
		return errors.New("agent: Tailscale must be running consistently on an older version before updating")
	}
	installed, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Status}", "tailscale").Output()
	if err != nil || strings.TrimSpace(string(installed)) != "install ok installed" {
		return errors.New("agent: Tailscale is not installed as a supported Debian package")
	}
	args := append(append([]string{}, tailscaleAptOptions...), "update")
	if err := exec.CommandContext(ctx, "apt-get", args...).Run(); err != nil {
		return errors.New("agent: failed to refresh the official Tailscale package source")
	}
	args = append(append([]string{}, tailscaleAptOptions...), "policy", "tailscale")
	policy, err := exec.CommandContext(ctx, "apt-cache", args...).Output()
	if err != nil || len(policy) > 64<<10 {
		return errors.New("agent: unable to inspect the Tailscale package candidate")
	}
	found := false
	for _, line := range strings.Split(string(policy), "\n") {
		if strings.TrimSpace(line) == "Candidate: "+task.TargetVersion {
			found = true
		}
	}
	if !found {
		return errors.New("agent: approved version is not the current official package candidate")
	}
	return nil
}

func startTailscaleUpdate(ctx context.Context, taskID string, task tailscalehost.UpdateTask) error {
	if task.Validate() != nil || tailscalehost.UpdateUnitName(taskID) == "" {
		return errors.New("agent: invalid Tailscale update task")
	}
	// systemd owns the package command after dispatch, so a private-network drop
	// does not terminate dpkg. Scheduling is not evidence of successful update.
	args := []string{"--unit=" + tailscalehost.UpdateUnitName(taskID), "--no-block", "--remain-after-exit", "--setenv=DEBIAN_FRONTEND=noninteractive", "--property=TimeoutStartSec=900", "--property=RuntimeMaxSec=900", "--", "/usr/bin/apt-get"}
	args = append(args, tailscaleAptOptions...)
	args = append(args, "--yes", "--only-upgrade", "--no-remove", "--no-install-recommends", "install", "tailscale="+task.TargetVersion)
	if err := exec.CommandContext(ctx, "systemd-run", args...).Run(); err != nil {
		return uncertainTaskOutcome(errors.New("agent: Tailscale update dispatch outcome is uncertain"))
	}
	return nil
}

func (c Client) beginTailscaleUpdate(ctx context.Context, store *Store, task DeploymentTask) error {
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	endpoint := connection.CenterURL + "/api/v1/agents/" + url.PathEscape(connection.AgentID) + "/tailscale-updates/" + url.PathEscape(task.ID) + "/start"
	return c.post(ctx, endpoint, map[string]any{"executionId": c.execution.ID, "sessionId": c.executionSession, "attempt": task.Attempt}, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, nil)
}

func (s *Store) setTailscaleUpdateTask(id string) {
	s.tailscaleUpdateMu.Lock()
	s.tailscaleUpdateTaskID = id
	s.tailscaleUpdateMu.Unlock()
}

func (s *Store) observeTailscaleUpdateTask(parent context.Context) *tailscalehost.UpdateObservation {
	s.tailscaleUpdateMu.Lock()
	id := s.tailscaleUpdateTaskID
	s.tailscaleUpdateMu.Unlock()
	unit := tailscalehost.UpdateUnitName(id)
	if unit == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "systemctl", "show", unit, "--property=LoadState", "--property=ActiveState", "--property=Result", "--property=ExecMainStatus").Output()
	result := &tailscalehost.UpdateObservation{TaskID: id, State: "unknown"}
	if err != nil || len(output) > 8192 {
		return result
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	switch {
	case props["LoadState"] == "not-found":
		return result
	case props["ActiveState"] == "failed" || props["Result"] != "" && props["Result"] != "success":
		result.State = "failed"
		return result
	case props["ActiveState"] != "active" || props["Result"] != "success" || props["ExecMainStatus"] != "0":
		result.State = "running"
		return result
	}
	local, err := exec.CommandContext(ctx, "tailscale", "version", "--json", "--daemon").Output()
	versions, parseErr := tailscalehost.ParseVersions(local, true)
	if err != nil || parseErr != nil {
		result.State = "running"
		return result
	}
	status, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil || len(status) > 4<<20 || !tailscaleBackendRunning(status) {
		result.State = "running"
		return result
	}
	result.State = "succeeded"
	s.tailscaleUpdateMu.Lock()
	s.tailscaleUpdateNext = time.Time{}
	s.tailscaleUpdateMu.Unlock()
	result.InstalledVersion, result.RunningVersion = versions.Client, versions.Daemon
	return result
}

func tailscaleBackendRunning(raw []byte) bool {
	var payload struct {
		BackendState string `json:"BackendState"`
	}
	return json.Unmarshal(raw, &payload) == nil && payload.BackendState == "Running"
}
