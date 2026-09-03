package tailscalehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// Long release versions may contain Tailscale's documented git build stamps.
// Recognize that exact format; never strip arbitrary prerelease/dev suffixes.
var releaseVersionPattern = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+)(?:-t[0-9a-f]{7,40}(?:-g[0-9a-f]{7,40})?)?(\+[0-9A-Za-z.-]+)?$`)
var configFlagPattern = regexp.MustCompile(`(?m)^\s+-{1,2}config(?:\s|=)`)

// CompatibleVersion returns the numeric version only after validating the
// release format, stable branch and minimum version. Runtime/configuration
// checks are still required; this is not a promise about future releases.
func CompatibleVersion(value string) (string, error) {
	if len(value) > 160 {
		return "", compatibilityError("unparseable version", "version output is too long")
	}
	value = strings.TrimSpace(value)
	match := releaseVersionPattern.FindStringSubmatch(value)
	if match == nil || !semver.IsValid("v"+match[1]+match[2]) {
		return "", compatibilityError(value, "not a supported stable release version")
	}
	parts := strings.Split(match[1], ".")
	minor, err := strconv.ParseUint(parts[1], 10, 32)
	if parts[0] != "1" || err != nil || minor%2 != 0 {
		return "", compatibilityError(value, "only the stable Tailscale 1.x release branch is supported")
	}
	if semver.Compare("v"+match[1], "v"+MinimumCompatibleVersion) < 0 {
		return "", compatibilityError(value, "version is below the compatibility floor")
	}
	return match[1], nil
}

func compatibilityError(found, reason string) error {
	return fmt.Errorf("tailscale %q is not compatible: %s; minimum stable version is %s. Install a supported stable package and restart tailscaled; Vastora will not replace or downgrade it automatically", found, reason, MinimumCompatibleVersion)
}

type Versions struct {
	Client string
	Daemon string
}

// ParseVersions accepts the structured response of `tailscale version --json`.
// Runtime verification additionally requests --daemon and requires its version;
// preflight deliberately does not contact a daemon that may need restarting.
func ParseVersions(payload []byte, requireDaemon bool) (Versions, error) {
	var metadata struct {
		Short          string `json:"short"`
		Long           string `json:"long"`
		DaemonLong     string `json:"daemonLong"`
		IsDev          bool   `json:"isDev"`
		UnstableBranch bool   `json:"unstableBranch"`
		GitDirty       bool   `json:"gitDirty"`
	}
	if len(payload) > 16<<10 || json.Unmarshal(payload, &metadata) != nil {
		return Versions{}, compatibilityError("unparseable version", "the client did not return valid version metadata")
	}
	if metadata.IsDev || metadata.UnstableBranch || metadata.GitDirty {
		return Versions{}, compatibilityError("development build", "development, unstable and dirty builds are not supported")
	}
	client, err := CompatibleVersion(metadata.Short)
	if err != nil {
		return Versions{}, fmt.Errorf("client version: %w", err)
	}
	long, err := CompatibleVersion(metadata.Long)
	if err != nil || long != client {
		return Versions{}, compatibilityError(metadata.Short, "client build metadata is invalid or inconsistent")
	}
	versions := Versions{Client: client}
	if requireDaemon {
		versions.Daemon, err = CompatibleVersion(metadata.DaemonLong)
		if err != nil {
			return Versions{}, fmt.Errorf("running daemon version: %w", err)
		}
	}
	return versions, nil
}

// The daemon has no JSON version flag. Its documented --version output starts
// with Short(), followed by a "long version:" line from the same build. Require
// both rather than discarding arbitrary suffixes or trusting the CLI package.
func parseDaemonBinaryVersion(payload []byte) (string, error) {
	if len(payload) > 16<<10 {
		return "", compatibilityError("unparseable daemon version", "version output is too long")
	}
	lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
	version, err := CompatibleVersion(lines[0])
	if err != nil {
		return "", fmt.Errorf("installed daemon version: %w", err)
	}
	longVersion := ""
	foundLongVersion := false
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "track:") || strings.Contains(line, "-dirty") {
			return "", compatibilityError(version, "the installed daemon is an unstable or dirty build")
		}
		if value, ok := strings.CutPrefix(line, "long version:"); ok {
			if foundLongVersion {
				return "", compatibilityError(version, "daemon build metadata is ambiguous")
			}
			foundLongVersion = true
			longVersion = strings.TrimSpace(value)
		}
	}
	long, err := CompatibleVersion(longVersion)
	if err != nil || long != version {
		return "", compatibilityError(version, "daemon build metadata is invalid or inconsistent")
	}
	return version, nil
}

// CheckCompatibility is shared by the installer, fixed-endpoint reconciliation
// and explicit legacy adoption. It is read-only: no package/configuration,
// ownership, identity or login state is changed by a compatibility check.
func CheckCompatibility(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), requireRunning bool) error {
	if run == nil {
		return errors.New("verify Tailscale compatibility: host command runner is required")
	}
	versionArguments := []string{"version", "--json"}
	if requireRunning {
		versionArguments = append(versionArguments, "--daemon")
	}
	output, err := run(ctx, "tailscale", versionArguments...)
	if err != nil {
		return fmt.Errorf("read Tailscale version metadata (minimum %s); install a stable package and ensure tailscaled is running before runtime verification: %w", MinimumCompatibleVersion, err)
	}
	versions, err := ParseVersions(output, requireRunning)
	if err != nil {
		return err
	}
	output, err = run(ctx, "tailscaled", "--version")
	if err != nil {
		return fmt.Errorf("read installed Tailscale daemon version (minimum %s): %w", MinimumCompatibleVersion, err)
	}
	installedDaemon, err := parseDaemonBinaryVersion(output)
	if err != nil {
		return err
	}
	if requireRunning && installedDaemon != versions.Daemon {
		return compatibilityError(versions.Daemon, "running daemon differs from installed "+installedDaemon+"; restart tailscaled to finish the package upgrade")
	}
	output, err = run(ctx, "tailscaled", "--help")
	if err != nil || len(output) > 64<<10 || !configFlagPattern.Match(output) {
		return compatibilityError(installedDaemon, "the installed daemon does not expose the required --config capability")
	}
	if !requireRunning {
		return nil
	}
	if _, err := run(ctx, "systemctl", "is-active", "--quiet", "tailscaled.service"); err != nil {
		return errors.New("verify Tailscale compatibility: tailscaled.service is not active")
	}
	output, err = run(ctx, "tailscale", "status", "--json")
	var status struct {
		BackendState string `json:"BackendState"`
		Version      string `json:"Version"`
	}
	if err != nil || len(output) > 4<<20 || json.Unmarshal(output, &status) != nil || status.BackendState != "Running" {
		return errors.New("verify Tailscale compatibility: daemon backend is not Running")
	}
	version, err := CompatibleVersion(status.Version)
	if err != nil || version != versions.Daemon {
		return errors.New("verify Tailscale compatibility: running daemon changed during verification or returned invalid version metadata")
	}
	output, err = run(ctx, "systemctl", "show", "--property=Environment", "--value", "tailscaled.service")
	if err != nil {
		return errors.New("verify Tailscale compatibility: daemon privacy environment is unavailable")
	}
	for _, value := range strings.Fields(string(output)) {
		if strings.Trim(value, "\"") == "TS_NO_LOGS_NO_SUPPORT=true" {
			return nil
		}
	}
	return errors.New("verify Tailscale compatibility: TS_NO_LOGS_NO_SUPPORT=true is not loaded; run the Vastora private-network preparation before retrying")
}
