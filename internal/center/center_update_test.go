package center

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

type fixedReleaseChecker struct {
	version string
	err     error
}

func (checker fixedReleaseChecker) LatestVersion(context.Context) (string, time.Time, error) {
	return checker.version, time.Date(2026, 8, 25, 1, 2, 3, 0, time.UTC), checker.err
}

type fakeCenterUpdater struct {
	status  deployapi.CenterUpdateExecution
	started string
}

func (updater *fakeCenterUpdater) CenterUpdateStatus(context.Context) (deployapi.CenterUpdateExecution, error) {
	return updater.status, nil
}

func (updater *fakeCenterUpdater) StartCenterUpdate(_ context.Context, version string) (deployapi.CenterUpdateExecution, error) {
	updater.started = version
	return deployapi.CenterUpdateExecution{Available: true, State: "queued", TargetVersion: version}, nil
}

func TestOfficialReleaseCheckerReadsTheR2ReleaseVersion(t *testing.T) {
	installer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodHead {
			t.Fatalf("method = %s", request.Method)
		}
		writer.Header().Set("X-Vastora-Version", "0.1.0-alpha.48")
		writer.WriteHeader(http.StatusOK)
	}))
	defer installer.Close()
	checker := NewOfficialReleaseChecker(installer.URL)
	version, checkedAt, err := checker.LatestVersion(context.Background())
	if err != nil || version != "0.1.0-alpha.48" || checkedAt.IsZero() {
		t.Fatalf("unexpected release: version=%q checked=%s err=%v", version, checkedAt, err)
	}
	for _, value := range []string{"", "v0.1.0", "latest", "0.1.0/other"} {
		if _, err := releaseVersionFromHeader(value); err == nil {
			t.Fatalf("invalid release version %q was accepted", value)
		}
	}
}

func TestOfficialReleaseCheckerRejectsRedirectsAndMissingVersionHeaders(t *testing.T) {
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
			if _, _, err := NewOfficialReleaseChecker(installer.URL).LatestVersion(context.Background()); err == nil {
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
	server := &Server{updates: updater, releaseChecker: fixedReleaseChecker{version: "0.1.0-alpha.48"}}
	status := server.centerUpdateStatus(context.Background())
	if !status.UpdateAvailable || !status.Automatic || status.LatestVersion != "0.1.0-alpha.48" {
		t.Fatalf("unexpected update status: %#v", status)
	}
	response := httptest.NewRecorder()
	server.handleStartCenterUpdate(response, httptest.NewRequest(http.MethodPost, "/api/v1/system/update", nil))
	if response.Code != http.StatusAccepted || updater.started != "0.1.0-alpha.48" {
		t.Fatalf("start status=%d body=%s version=%q", response.Code, response.Body.String(), updater.started)
	}
}
