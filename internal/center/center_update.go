package center

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
	"golang.org/x/mod/semver"
)

type CenterReleaseChecker interface {
	LatestRelease(context.Context, bool) (CenterRelease, time.Time, error)
}

type CenterRelease struct {
	Version string
	Image   string
}

type CenterUpdateStatus struct {
	CurrentVersion        string                    `json:"currentVersion"`
	CurrentImageDigest    string                    `json:"currentImageDigest,omitempty"`
	LatestVersion         string                    `json:"latestVersion,omitempty"`
	LatestImageDigest     string                    `json:"latestImageDigest,omitempty"`
	ImageMismatch         bool                      `json:"imageMismatch"`
	UpdateAvailable       bool                      `json:"updateAvailable"`
	ReleaseCheckAvailable bool                      `json:"releaseCheckAvailable"`
	Automatic             bool                      `json:"automatic"`
	State                 string                    `json:"state"`
	TargetVersion         string                    `json:"targetVersion,omitempty"`
	Phase                 string                    `json:"phase,omitempty"`
	Progress              int                       `json:"progress,omitempty"`
	Message               string                    `json:"message,omitempty"`
	CheckedAt             string                    `json:"checkedAt,omitempty"`
	UpdatedAt             string                    `json:"updatedAt,omitempty"`
	Error                 string                    `json:"error,omitempty"`
	AgentRollout          *AgentUpdateRolloutStatus `json:"agentRollout,omitempty"`
}

type ReleaseChecker struct {
	url       string
	client    *http.Client
	mu        sync.Mutex
	release   CenterRelease
	checkedAt time.Time
	expiresAt time.Time
}

func NewReleaseChecker(endpoint string, client *http.Client) *ReleaseChecker {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &ReleaseChecker{
		url:    endpoint,
		client: client,
	}
}

func (checker *ReleaseChecker) LatestRelease(ctx context.Context, refresh bool) (CenterRelease, time.Time, error) {
	checker.mu.Lock()
	defer checker.mu.Unlock()
	now := time.Now().UTC()
	if !refresh && checker.release.Version != "" && now.Before(checker.expiresAt) {
		return checker.release, checker.checkedAt, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, checker.url, nil)
	if err != nil {
		return CenterRelease{}, time.Time{}, err
	}
	request.Header.Set("User-Agent", "Vastora/"+Version)
	response, err := checker.client.Do(request)
	if err != nil {
		return CenterRelease{}, time.Time{}, fmt.Errorf("center: check configured release source: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return CenterRelease{}, time.Time{}, fmt.Errorf("center: release metadata endpoint returned HTTP %d", response.StatusCode)
	}
	version, err := releaseVersionFromHeader(response.Header.Get("X-Vastora-Version"))
	if err != nil {
		return CenterRelease{}, time.Time{}, err
	}
	image := strings.TrimSpace(response.Header.Get("X-Vastora-Center-Image"))
	if !validReleaseImage(image) {
		return CenterRelease{}, time.Time{}, errors.New("center: release metadata endpoint returned an invalid Center image")
	}
	release := CenterRelease{Version: version, Image: image}
	checker.release = release
	checker.checkedAt = now
	checker.expiresAt = now.Add(10 * time.Minute)
	return release, now, nil
}

func releaseVersionFromHeader(value string) (string, error) {
	version := strings.TrimSpace(value)
	if version == "" || strings.HasPrefix(version, "v") || !semver.IsValid("v"+version) {
		return "", errors.New("center: release metadata endpoint returned an invalid release version")
	}
	return version, nil
}

func validReleaseImage(image string) bool {
	digest, found := strings.CutPrefix(image, "ghcr.io/petauron/vastora-center@sha256:")
	if !found || len(digest) != 64 {
		return false
	}
	for _, character := range digest {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func releaseImageDigest(image string) string {
	return strings.TrimPrefix(image, "ghcr.io/petauron/vastora-center@sha256:")
}

func (s *Server) handleCenterUpdateStatus(writer http.ResponseWriter, request *http.Request) {
	refresh := request.URL.Query().Get("refresh") == "true"
	writeJSON(writer, http.StatusOK, s.centerUpdateStatus(request.Context(), refresh))
}

func (s *Server) handleStartCenterUpdate(writer http.ResponseWriter, request *http.Request) {
	status := s.centerUpdateStatus(request.Context(), false)
	if status.Error != "" {
		writeError(writer, http.StatusConflict, errors.New(status.Error))
		return
	}
	if !status.UpdateAvailable {
		writeError(writer, http.StatusConflict, errors.New("center: no newer release is available"))
		return
	}
	if !status.Automatic || s.updates == nil {
		writeError(writer, http.StatusConflict, errors.New("center: automatic updates are not available on this installation"))
		return
	}
	if s.resolveReleaseInstaller == nil {
		writeError(writer, http.StatusConflict, errors.New("center: release installer resolution is unavailable"))
		return
	}
	pin, err := s.resolveReleaseInstaller(request.Context())
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	execution, err := s.updates.StartCenterUpdate(request.Context(), deployapi.CenterUpdateRequest{Version: status.LatestVersion, Image: "ghcr.io/petauron/vastora-center@sha256:" + status.LatestImageDigest, InstallerBaseURL: s.releaseInstallerBaseURL, InstallerHost: pin.Host, InstallerPort: pin.Port, InstallerAddress: pin.Address})
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	status.State = execution.State
	status.TargetVersion = execution.TargetVersion
	status.Phase, status.Progress = centerUpdateProgress(execution)
	status.Message = execution.Message
	status.UpdatedAt = execution.UpdatedAt
	writeJSON(writer, http.StatusAccepted, status)
}

func (s *Server) centerUpdateStatus(ctx context.Context, refreshOfficial bool) CenterUpdateStatus {
	result := CenterUpdateStatus{CurrentVersion: Version, State: "idle"}
	if s.releaseChecker == nil {
		result.Error = "center: release checking is unavailable"
		return result
	}
	result.ReleaseCheckAvailable = true
	latest, checkedAt, err := s.releaseChecker.LatestRelease(ctx, refreshOfficial)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.LatestVersion = latest.Version
	result.LatestImageDigest = releaseImageDigest(latest.Image)
	result.CheckedAt = checkedAt.Format(time.RFC3339)
	currentSemver := "v" + strings.TrimPrefix(Version, "v")
	latestSemver := "v" + latest.Version
	if !semver.IsValid(currentSemver) {
		result.Error = "center: current version is not a released semantic version"
		return result
	}
	result.UpdateAvailable = semver.Compare(latestSemver, currentSemver) > 0
	if s.updates == nil {
		return result
	}
	execution, err := s.updates.CenterUpdateStatus(ctx)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Automatic = execution.Available
	result.CurrentImageDigest = releaseImageDigest(execution.InstalledImage)
	result.ImageMismatch = latest.Version == strings.TrimPrefix(Version, "v") && execution.InstalledImage != latest.Image
	result.UpdateAvailable = result.UpdateAvailable || result.ImageMismatch
	if execution.State == "queued" || execution.State == "applying" || execution.TargetVersion == latest.Version {
		result.State = execution.State
		result.TargetVersion = execution.TargetVersion
		result.Phase, result.Progress = centerUpdateProgress(execution)
		result.Message = execution.Message
		result.UpdatedAt = execution.UpdatedAt
	} else if execution.State == "succeeded" && execution.TargetVersion == strings.TrimPrefix(Version, "v") {
		result.State = execution.State
		result.TargetVersion = execution.TargetVersion
		result.Phase, result.Progress = centerUpdateProgress(execution)
		result.Message = execution.Message
		result.UpdatedAt = execution.UpdatedAt
	}
	if execution.State == "succeeded" && execution.TargetVersion == strings.TrimPrefix(Version, "v") {
		rollout, rolloutErr := s.store.AgentUpdateRolloutStatus(ctx, Version)
		if rolloutErr != nil {
			result.Error = rolloutErr.Error()
			return result
		}
		result.AgentRollout = &rollout
	}
	return result
}

func (s *Server) RunAgentUpdateRollout(ctx context.Context, interval time.Duration, report func(error)) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	reconcile := func() {
		if !s.agentUpdateRolloutAvailable() {
			return
		}
		_, err := s.store.QueueAgentUpdates(ctx, Version)
		if err != nil && ctx.Err() == nil && report != nil {
			report(err)
		}
	}
	reconcile()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func centerUpdateProgress(execution deployapi.CenterUpdateExecution) (string, int) {
	if execution.State == "queued" {
		return "queued", 5
	}
	if execution.State == "succeeded" {
		return "completed", 100
	}
	if execution.State != "applying" {
		return "", 0
	}
	switch execution.Message {
	case "Downloading the verified release metadata.":
		return "downloading", 10
	case "Verifying the immutable release.":
		return "verifying", 20
	case "Installing the verified release.":
		return "installing", 30
	case "Validating the existing installation.":
		return "validating", 40
	case "Downloading the immutable Center image.":
		return "pulling", 50
	case "Preparing the co-located Agent.":
		return "agent", 65
	case "Restarting Center.":
		return "restarting", 80
	case "Waiting for Center health checks.":
		return "health", 88
	case "Finishing Center startup reconciliation.":
		return "reconciling", 94
	case "Verifying the co-located Agent.":
		return "finalizing", 97
	default:
		return "installing", 30
	}
}
