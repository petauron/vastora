package center

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

type fixedReleaseChecker struct {
	version string
	err     error
	checks  *[]bool
}

func (checker fixedReleaseChecker) LatestVersion(_ context.Context, refresh bool) (string, time.Time, error) {
	if checker.checks != nil {
		*checker.checks = append(*checker.checks, refresh)
	}
	return checker.version, time.Date(2026, 8, 25, 1, 2, 3, 0, time.UTC), checker.err
}

type fakeCenterUpdater struct {
	status  deployapi.CenterUpdateExecution
	started deployapi.CenterUpdateRequest
}

func (updater *fakeCenterUpdater) CenterUpdateStatus(context.Context) (deployapi.CenterUpdateExecution, error) {
	return updater.status, nil
}

func (updater *fakeCenterUpdater) StartCenterUpdate(_ context.Context, input deployapi.CenterUpdateRequest) (deployapi.CenterUpdateExecution, error) {
	updater.started = input
	return deployapi.CenterUpdateExecution{Available: true, State: "queued", TargetVersion: input.Version}, nil
}

func TestReleaseCheckerReadsTheConfiguredReleaseVersion(t *testing.T) {
	installer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodHead {
			t.Fatalf("method = %s", request.Method)
		}
		writer.Header().Set("X-Vastora-Version", "0.1.0-alpha.48")
		writer.WriteHeader(http.StatusOK)
	}))
	defer installer.Close()
	checker := NewReleaseChecker(installer.URL, installer.Client())
	version, checkedAt, err := checker.LatestVersion(context.Background(), false)
	if err != nil || version != "0.1.0-alpha.48" || checkedAt.IsZero() {
		t.Fatalf("unexpected release: version=%q checked=%s err=%v", version, checkedAt, err)
	}
	for _, value := range []string{"", "v0.1.0", "latest", "0.1.0/other"} {
		if _, err := releaseVersionFromHeader(value); err == nil {
			t.Fatalf("invalid release version %q was accepted", value)
		}
	}
}

func TestReleaseCheckerRejectsRedirectsAndMissingVersionHeaders(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
	}{
		{name: "redirect", status: http.StatusFound},
		{name: "missing version", status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			installer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
			}))
			defer installer.Close()
			if _, _, err := NewReleaseChecker(installer.URL, installer.Client()).LatestVersion(context.Background(), false); err == nil {
				t.Fatal("invalid installer response was accepted")
			}
		})
	}
}

func TestCenterUpdateStatusAndStartUseTheRestrictedUpdater(t *testing.T) {
	previousVersion := Version
	Version = "0.1.0-alpha.47"
	defer func() { Version = previousVersion }()
	updater := &fakeCenterUpdater{status: deployapi.CenterUpdateExecution{Available: true, State: "idle"}}
	server := &Server{updates: updater, releaseChecker: fixedReleaseChecker{version: "0.1.0-alpha.48"}, releaseInstallerBaseURL: "https://releases.example.com", resolveReleaseInstaller: func(context.Context) (ExternalHelperPin, error) {
		return ExternalHelperPin{Host: "releases.example.com", Port: "443", Address: "203.0.113.10"}, nil
	}}
	status := server.centerUpdateStatus(context.Background(), false)
	if !status.UpdateAvailable || !status.Automatic || status.LatestVersion != "0.1.0-alpha.48" {
		t.Fatalf("unexpected update status: %#v", status)
	}
	response := httptest.NewRecorder()
	server.handleStartCenterUpdate(response, httptest.NewRequest(http.MethodPost, "/api/v1/system/update", nil))
	if response.Code != http.StatusAccepted || updater.started.Version != "0.1.0-alpha.48" || updater.started.InstallerBaseURL != "https://releases.example.com" || updater.started.InstallerHost != "releases.example.com" || updater.started.InstallerAddress != "203.0.113.10" {
		t.Fatalf("start status=%d body=%s request=%#v", response.Code, response.Body.String(), updater.started)
	}
}

func TestCenterUpdateStatusReportsVerifiedHostProgress(t *testing.T) {
	previousVersion := Version
	Version = "0.1.0-alpha.72"
	defer func() { Version = previousVersion }()
	updater := &fakeCenterUpdater{status: deployapi.CenterUpdateExecution{
		Available:     true,
		State:         "applying",
		TargetVersion: "0.1.0-alpha.73",
		Message:       "Downloading the immutable Center image.",
	}}
	server := &Server{updates: updater, releaseChecker: fixedReleaseChecker{version: "0.1.0-alpha.73"}}
	status := server.centerUpdateStatus(context.Background(), false)
	if status.State != "applying" || status.Phase != "pulling" || status.Progress != 50 {
		t.Fatalf("unexpected update progress: %#v", status)
	}
}

func TestReleaseCheckerBypassesItsCacheWhenRefreshIsRequested(t *testing.T) {
	requests := 0
	installer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		writer.Header().Set("X-Vastora-Version", fmt.Sprintf("0.1.0-alpha.%d", 47+requests))
		writer.WriteHeader(http.StatusOK)
	}))
	defer installer.Close()
	checker := NewReleaseChecker(installer.URL, installer.Client())
	first, _, err := checker.LatestVersion(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	cached, _, err := checker.LatestVersion(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, _, err := checker.LatestVersion(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if first != "0.1.0-alpha.48" || cached != first || refreshed != "0.1.0-alpha.49" || requests != 2 {
		t.Fatalf("first=%q cached=%q refreshed=%q requests=%d", first, cached, refreshed, requests)
	}
}

func TestCenterUpdateStatusHandlerRequestsAnOfficialRefresh(t *testing.T) {
	checks := []bool{}
	server := &Server{releaseChecker: fixedReleaseChecker{version: "0.1.0-alpha.60", checks: &checks}}
	response := httptest.NewRecorder()
	server.handleCenterUpdateStatus(response, httptest.NewRequest(http.MethodGet, "/api/v1/system/update?refresh=true", nil))
	if response.Code != http.StatusOK || len(checks) != 1 || !checks[0] {
		t.Fatalf("status=%d checks=%v body=%s", response.Code, checks, response.Body.String())
	}
}
