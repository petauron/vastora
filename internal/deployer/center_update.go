package deployer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
	"golang.org/x/mod/semver"
)

type FileCenterUpdater struct {
	InstallDir string
}

func (updater FileCenterUpdater) CenterUpdateStatus(context.Context) (deployapi.CenterUpdateExecution, error) {
	if updater.InstallDir == "" || !filepath.IsAbs(updater.InstallDir) {
		return deployapi.CenterUpdateExecution{}, errors.New("deployer: Center installation directory is invalid")
	}
	available := regularFile(filepath.Join(updater.InstallDir, ".update-service-enabled")) && regularFile(filepath.Join(updater.InstallDir, "update-center.sh"))
	statusPath := filepath.Join(updater.InstallDir, ".update-status.json")
	file, err := os.Open(statusPath)
	if errors.Is(err, os.ErrNotExist) {
		return deployapi.CenterUpdateExecution{Available: available, State: "idle"}, nil
	}
	if err != nil {
		return deployapi.CenterUpdateExecution{}, fmt.Errorf("deployer: read Center update status: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	var result deployapi.CenterUpdateExecution
	if err := decoder.Decode(&result); err != nil {
		return deployapi.CenterUpdateExecution{}, fmt.Errorf("deployer: decode Center update status: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return deployapi.CenterUpdateExecution{}, errors.New("deployer: Center update status contains trailing data")
	}
	if !validUpdateState(result.State) {
		return deployapi.CenterUpdateExecution{}, errors.New("deployer: Center update status is invalid")
	}
	result.Available = available
	return result, nil
}

func (updater FileCenterUpdater) StartCenterUpdate(ctx context.Context, input deployapi.CenterUpdateRequest) (deployapi.CenterUpdateExecution, error) {
	version := strings.TrimSpace(input.Version)
	if !semver.IsValid("v" + version) {
		return deployapi.CenterUpdateExecution{}, errors.New("deployer: requested Center version is invalid")
	}
	installerBaseURL, err := validateInstallerBaseURL(input.InstallerBaseURL)
	if err != nil {
		return deployapi.CenterUpdateExecution{}, err
	}
	if err := validateInstallerPin(input, installerBaseURL); err != nil {
		return deployapi.CenterUpdateExecution{}, err
	}
	requestPayload := []byte(strings.Join([]string{version, installerBaseURL, input.InstallerHost, input.InstallerPort, input.InstallerAddress, ""}, "\n"))
	status, err := updater.CenterUpdateStatus(ctx)
	if err != nil {
		return deployapi.CenterUpdateExecution{}, err
	}
	if !status.Available {
		return deployapi.CenterUpdateExecution{}, errors.New("deployer: automatic Center updates are not installed")
	}
	currentVersion, err := installedCenterVersion(filepath.Join(updater.InstallDir, "release.env"))
	if err != nil {
		return deployapi.CenterUpdateExecution{}, err
	}
	if status.State == "queued" || status.State == "applying" {
		if status.TargetVersion == version {
			requestPath := filepath.Join(updater.InstallDir, ".update-request")
			if !regularFile(requestPath) {
				if err := writeCenterUpdateFile(requestPath, requestPayload, 0o600); err != nil {
					return deployapi.CenterUpdateExecution{}, fmt.Errorf("deployer: recover Center update request: %w", err)
				}
			}
			return status, nil
		}
		return deployapi.CenterUpdateExecution{}, errors.New("deployer: another Center update is already running")
	}
	if semver.Compare("v"+version, "v"+currentVersion) <= 0 {
		return deployapi.CenterUpdateExecution{}, fmt.Errorf("deployer: Center update %s is not newer than installed version %s", version, currentVersion)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	queued := deployapi.CenterUpdateExecution{Available: true, State: "queued", TargetVersion: version, Message: "Waiting for the host update service.", UpdatedAt: now}
	statusPath := filepath.Join(updater.InstallDir, ".update-status.json")
	if err := writeCenterUpdateStatus(statusPath, queued); err != nil {
		return deployapi.CenterUpdateExecution{}, fmt.Errorf("deployer: queue Center update status: %w", err)
	}
	if err := writeCenterUpdateFile(filepath.Join(updater.InstallDir, ".update-request"), requestPayload, 0o600); err != nil {
		failed := queued
		failed.State = "failed"
		failed.Message = "The update request could not be queued."
		if statusErr := writeCenterUpdateStatus(statusPath, failed); statusErr != nil {
			return deployapi.CenterUpdateExecution{}, errors.Join(
				fmt.Errorf("deployer: queue Center update request: %w", err),
				fmt.Errorf("deployer: record Center update queue failure: %w", statusErr),
			)
		}
		return deployapi.CenterUpdateExecution{}, fmt.Errorf("deployer: queue Center update request: %w", err)
	}
	return queued, nil
}

func validateInstallerPin(input deployapi.CenterUpdateRequest, installerBaseURL string) error {
	parsed, _ := url.Parse(installerBaseURL)
	host := strings.TrimSpace(input.InstallerHost)
	port := strings.TrimSpace(input.InstallerPort)
	address := net.ParseIP(strings.TrimSpace(input.InstallerAddress))
	expectedPort := parsed.Port()
	if expectedPort == "" {
		expectedPort = "443"
	}
	portNumber, portErr := strconv.Atoi(port)
	if !strings.EqualFold(host, parsed.Hostname()) || strings.ContainsAny(host, "\r\n\t") || portErr != nil || portNumber < 1 || portNumber > 65535 || port != expectedPort || address == nil || strings.ContainsAny(input.InstallerAddress, "\r\n\t") {
		return errors.New("deployer: release installer DNS pin is invalid")
	}
	return nil
}

func validateInstallerBaseURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("deployer: release installer base URL must be an exact credential-free HTTPS URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

func writeCenterUpdateStatus(path string, status deployapi.CenterUpdateExecution) error {
	payload, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return writeCenterUpdateFile(path, append(payload, '\n'), 0o600)
}

func installedCenterVersion(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("deployer: read installed Center release: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 16<<10))
	for scanner.Scan() {
		if version, found := strings.CutPrefix(scanner.Text(), "VASTORA_VERSION="); found {
			version = strings.TrimSpace(version)
			if !semver.IsValid("v" + version) {
				return "", errors.New("deployer: installed Center version is invalid")
			}
			return version, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("deployer: read installed Center release: %w", err)
	}
	return "", errors.New("deployer: installed Center version is missing")
}

func writeCenterUpdateFile(path string, payload []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".vastora-write-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func validUpdateState(value string) bool {
	switch value {
	case "idle", "queued", "applying", "succeeded", "failed":
		return true
	default:
		return false
	}
}
