package agent

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"os/exec"
	"time"

	"github.com/petauron/vastora/internal/tailscalehost"
	"golang.org/x/mod/semver"
)

// Checks never block the management heartbeat. A failed or stale check is not
// reported as current. The generation remembers a manual request received
// while another check is running, so the next heartbeat performs it.
func (s *Store) scheduleTailscaleUpdateCheck(generation int64, agentID string) {
	s.tailscaleUpdateMu.Lock()
	if generation > s.tailscaleUpdateRequested {
		s.tailscaleUpdateRequested = generation
	}
	if s.tailscaleUpdateRunning || (time.Now().Before(s.tailscaleUpdateNext) && s.tailscaleUpdateRequested <= s.tailscaleUpdateCompleted) {
		s.tailscaleUpdateMu.Unlock()
		return
	}
	s.tailscaleUpdateRunning = true
	requested := s.tailscaleUpdateRequested
	s.tailscaleUpdateMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		observed := inspectTailscaleUpdate(ctx)
		sum := sha256.Sum256([]byte(agentID))
		delay := 24*time.Hour + time.Duration(binary.BigEndian.Uint16(sum[:2])%600)*time.Second
		if observed.Error != "" {
			delay = time.Hour
		}
		s.tailscaleUpdateMu.Lock()
		s.tailscaleUpdate = &observed
		s.tailscaleUpdateCompleted = requested
		s.tailscaleUpdateNext = time.Now().Add(delay)
		s.tailscaleUpdateRunning = false
		s.tailscaleUpdateMu.Unlock()
	}()
}

func (s *Store) tailscaleUpdateStatus() *tailscalehost.UpdateStatus {
	s.tailscaleUpdateMu.Lock()
	defer s.tailscaleUpdateMu.Unlock()
	if s.tailscaleUpdate == nil {
		return nil
	}
	value := *s.tailscaleUpdate
	return &value
}

func inspectTailscaleUpdate(ctx context.Context) tailscalehost.UpdateStatus {
	result := tailscalehost.UpdateStatus{CheckedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	local, err := exec.CommandContext(ctx, "tailscale", "version", "--json", "--daemon").Output()
	if err != nil {
		result.Error = "local_version_unavailable"
		return result
	}
	versions, err := tailscalehost.ParseVersions(local, true)
	if err != nil {
		result.Error = "local_version_unavailable"
		return result
	}
	result.InstalledVersion, result.RunningVersion = versions.Client, versions.Daemon
	upstream, err := exec.CommandContext(ctx, "tailscale", "version", "--json", "--upstream", "--track=stable").Output()
	var payload struct {
		Upstream string `json:"upstream"`
	}
	if err != nil || len(upstream) > 16<<10 || json.Unmarshal(upstream, &payload) != nil {
		result.Error = "upstream_unavailable"
		return result
	}
	latest, err := tailscalehost.CompatibleVersion(payload.Upstream)
	if err != nil {
		result.Error = "upstream_unavailable"
		return result
	}
	result.UpgradeReady = tailscaleAptSourceValid() == nil
	result.LatestVersion = latest
	result.UpdateAvailable = semver.Compare("v"+versions.Client, "v"+latest) < 0 || semver.Compare("v"+versions.Daemon, "v"+latest) < 0
	return result
}
