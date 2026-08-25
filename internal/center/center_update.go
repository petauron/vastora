package center

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

const officialInstallerURL = "https://vastora.petauron.com/install.sh"

type CenterReleaseChecker interface {
	LatestVersion(context.Context) (string, time.Time, error)
}

type CenterUpdateStatus struct {
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Automatic       bool   `json:"automatic"`
	State           string `json:"state"`
	TargetVersion   string `json:"targetVersion,omitempty"`
	Message         string `json:"message,omitempty"`
	CheckedAt       string `json:"checkedAt,omitempty"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
	Error           string `json:"error,omitempty"`
}

type OfficialReleaseChecker struct {
	url       string
	client    *http.Client
	mu        sync.Mutex
	version   string
	checkedAt time.Time
	expiresAt time.Time
}

func NewOfficialReleaseChecker(endpoint string) *OfficialReleaseChecker {
	if endpoint == "" {
		endpoint = officialInstallerURL
	}
	return &OfficialReleaseChecker{
		url: endpoint,
		client: &http.Client{
			Timeout:       15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (checker *OfficialReleaseChecker) LatestVersion(ctx context.Context) (string, time.Time, error) {
	checker.mu.Lock()
	defer checker.mu.Unlock()
	now := time.Now().UTC()
	if checker.version != "" && now.Before(checker.expiresAt) {
		return checker.version, checker.checkedAt, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, checker.url, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	request.Header.Set("User-Agent", "Vastora/"+Version)
	response, err := checker.client.Do(request)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("center: check official release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 300 || response.StatusCode > 399 {
		return "", time.Time{}, fmt.Errorf("center: official installer returned HTTP %d", response.StatusCode)
	}
	version, err := releaseVersionFromLocation(response.Header.Get("Location"))
	if err != nil {
		return "", time.Time{}, err
	}
	checker.version = version
	checker.checkedAt = now
	checker.expiresAt = now.Add(10 * time.Minute)
	return version, now, nil
}

func releaseVersionFromLocation(location string) (string, error) {
	target, err := url.Parse(location)
	if err != nil || target.Scheme != "https" || !strings.EqualFold(target.Hostname(), "github.com") {
		return "", errors.New("center: official installer selected an invalid release URL")
	}
	const prefix = "/petauron/vastora/releases/download/v"
	if !strings.HasPrefix(target.Path, prefix) || !strings.HasSuffix(target.Path, "/install.sh") {
		return "", errors.New("center: official installer selected an unexpected release asset")
	}
	version := strings.TrimSuffix(strings.TrimPrefix(target.Path, prefix), "/install.sh")
	if strings.Contains(version, "/") || !semver.IsValid("v"+version) {
		return "", errors.New("center: official installer selected an invalid release version")
	}
	return version, nil
}

func (s *Server) handleCenterUpdateStatus(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, s.centerUpdateStatus(request.Context()))
}

func (s *Server) handleStartCenterUpdate(writer http.ResponseWriter, request *http.Request) {
	status := s.centerUpdateStatus(request.Context())
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
	execution, err := s.updates.StartCenterUpdate(request.Context(), status.LatestVersion)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	status.State = execution.State
	status.TargetVersion = execution.TargetVersion
	status.Message = execution.Message
	status.UpdatedAt = execution.UpdatedAt
	writeJSON(writer, http.StatusAccepted, status)
}

func (s *Server) centerUpdateStatus(ctx context.Context) CenterUpdateStatus {
	result := CenterUpdateStatus{CurrentVersion: Version, State: "idle"}
	if s.releaseChecker == nil {
		result.Error = "center: release checking is unavailable"
		return result
	}
	latest, checkedAt, err := s.releaseChecker.LatestVersion(ctx)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.LatestVersion = latest
	result.CheckedAt = checkedAt.Format(time.RFC3339)
	currentSemver := "v" + strings.TrimPrefix(Version, "v")
	latestSemver := "v" + latest
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
	if execution.State == "queued" || execution.State == "applying" || execution.TargetVersion == latest {
		result.State = execution.State
		result.TargetVersion = execution.TargetVersion
		result.Message = execution.Message
		result.UpdatedAt = execution.UpdatedAt
	} else if execution.State == "succeeded" && execution.TargetVersion == strings.TrimPrefix(Version, "v") {
		result.State = execution.State
		result.TargetVersion = execution.TargetVersion
		result.Message = execution.Message
		result.UpdatedAt = execution.UpdatedAt
	}
	return result
}
