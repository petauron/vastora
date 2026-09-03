package tailscalehost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDefaultInstallVersionMeetsCompatibilityFloor(t *testing.T) {
	if _, err := CompatibleVersion(DefaultInstallVersion); err != nil {
		t.Fatalf("the installation pin cannot be installed under the compatibility policy: %v", err)
	}
}

func TestCompatibleVersion(t *testing.T) {
	// Higher versions are synthetic policy fixtures, not claims that these
	// versions have been published or verified against Headscale.
	for _, test := range []struct {
		input string
		want  string
	}{
		{"1.102.3", "1.102.3"},
		{"1.102.4", "1.102.4"},
		{"1.104.0", "1.104.0"},
		{"1.1000.0", "1.1000.0"},
		{"1.102.3+build.123", "1.102.3"},
		{"1.104.0-t123456789-gabcdef123", "1.104.0"},
		{"1.102.3-t012345678", "1.102.3"},
		{"1.102.2", ""},
		{"1.98.1000", ""},
		{"1.103.0", ""},
		{"2.0.0", ""},
		{"1.104.0-rc1", ""},
		{"1.104.0-beta.1", ""},
		{"1.104.0-dev20260903-t123456789", ""},
		{"1.104.0-t123456789-dirty", ""},
		{"1.104.0-unstable", ""},
		{"1.104.0-t-not-a-git-hash", ""},
		{"1.104.0+", ""},
		{"1.104.0+bad..metadata", ""},
		{"1.104", ""},
		{"1.0104.0", ""},
		{"v1.104.0", ""},
		{"1.104.0\nother output", ""},
		{"", ""},
		{strings.Repeat("1", 161), ""},
	} {
		t.Run(test.input, func(t *testing.T) {
			got, err := CompatibleVersion(test.input)
			if got != test.want || (err != nil) != (test.want == "") {
				t.Fatalf("version %q: got=%q err=%v, want=%q", test.input, got, err, test.want)
			}
			if err != nil && !strings.Contains(err.Error(), MinimumCompatibleVersion) {
				t.Fatal("compatibility failure omitted the minimum version")
			}
		})
	}
}

func versionPayload(client, daemon string) []byte {
	payload, _ := json.Marshal(map[string]any{"short": client, "long": client, "daemonLong": daemon})
	return payload
}

func TestParseVersionsChecksBothClientAndDaemonMetadata(t *testing.T) {
	for _, payload := range [][]byte{
		versionPayload("1.104.0", "1.104.0-t123456789"),
		versionPayload("1.104.0", "1.102.3"),
	} {
		if _, err := ParseVersions(payload, true); err != nil {
			t.Fatalf("compatible structured metadata was rejected: %v", err)
		}
	}
	for _, payload := range [][]byte{
		nil,
		[]byte(`not json`),
		[]byte(`{}`),
		[]byte(`{"short":"1.104.0","long":"1.104.0"}`),
		versionPayload("1.102.2", "1.104.0"),
		versionPayload("1.104.0", "1.102.2"),
		versionPayload("1.104.0", "1.104.0-dev20260903"),
		[]byte(`{"short":"1.104.0","long":"1.106.0","daemonLong":"1.104.0"}`),
		[]byte(`{"short":"1.104.0","long":"1.104.0","daemonLong":"1.104.0","isDev":true}`),
		[]byte(`{"short":"1.104.0","long":"1.104.0","daemonLong":"1.104.0","gitDirty":true}`),
		[]byte(`{"short":"1.104.0","long":"1.104.0","daemonLong":"1.104.0","unstableBranch":true}`),
		[]byte(strings.Repeat(" ", (16<<10)+1)),
	} {
		if _, err := ParseVersions(payload, true); err == nil {
			t.Fatalf("invalid client/daemon metadata was accepted: %s", payload)
		}
	}
}

func TestVersionPreflightDoesNotRequireDaemonMetadata(t *testing.T) {
	versions, err := ParseVersions([]byte(`{"short":"1.104.0","long":"1.104.0-t123456789"}`), false)
	if err != nil || versions.Client != "1.104.0" || versions.Daemon != "" {
		t.Fatalf("client preflight unexpectedly required a running daemon: %#v err=%v", versions, err)
	}
}

func TestParseDaemonBinaryVersion(t *testing.T) {
	for _, test := range []struct {
		output string
		valid  bool
	}{
		{"1.102.3\n  long version: 1.102.3-t123456789\n  go version: go1.26.0", true},
		{"1.104.0\n  long version: 1.104.0+build.123", true},
		{"", false},
		{"1.104.0", false},
		{"1.104.0\n  long version: 1.102.3", false},
		{"1.104.0\n  long version: 1.104.0-dev20260903-t123456789", false},
		{"1.104.0\n  track: unstable (dev)\n  long version: 1.104.0", false},
		{"1.104.0\n  tailscale commit: abcdef123-dirty\n  long version: 1.104.0", false},
		{"1.104.0\n  long version: 1.104.0\n  long version: 1.104.0", false},
		{"1.104.0\n  long version:\n  long version: 1.104.0", false},
		{strings.Repeat("x", (16<<10)+1), false},
	} {
		if _, err := parseDaemonBinaryVersion([]byte(test.output)); (err == nil) != test.valid {
			t.Fatalf("daemon version %q: err=%v", test.output, err)
		}
	}
}

func TestCheckCompatibilityRequiresCapabilitiesAndRunningPrivateDaemon(t *testing.T) {
	for _, broken := range []string{"", "version", "daemon_binary", "pending_restart", "config", "service", "session", "daemon_changed", "privacy"} {
		t.Run(broken, func(t *testing.T) {
			run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
				command := name + " " + strings.Join(arguments, " ")
				switch command {
				case "tailscale version --json --daemon":
					if broken == "version" {
						return nil, errors.New("daemon unavailable")
					}
					return versionPayload("1.104.0", "1.104.0"), nil
				case "tailscaled --version":
					if broken == "daemon_binary" {
						return []byte("1.102.2\n  long version: 1.102.2\n"), nil
					}
					if broken == "pending_restart" {
						return []byte("1.106.0\n  long version: 1.106.0\n"), nil
					}
					return []byte("1.104.0\n  long version: 1.104.0-t123456789\n"), nil
				case "tailscaled --help":
					if broken == "config" {
						return []byte("Usage: tailscaled\n"), nil
					}
					return []byte("Usage: tailscaled\n  -config string\n    configuration file\n"), nil
				case "systemctl is-active --quiet tailscaled.service":
					if broken == "service" {
						return nil, errors.New("inactive")
					}
					return nil, nil
				case "tailscale status --json":
					if broken == "session" {
						return []byte(`{"BackendState":"NeedsLogin","Version":"1.104.0"}`), nil
					}
					if broken == "daemon_changed" {
						return []byte(`{"BackendState":"Running","Version":"1.106.0"}`), nil
					}
					return []byte(`{"BackendState":"Running","Version":"1.104.0"}`), nil
				case "systemctl show --property=Environment --value tailscaled.service":
					if broken == "privacy" {
						return []byte("TS_NO_LOGS_NO_SUPPORT=false\n"), nil
					}
					return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
				default:
					t.Fatalf("unexpected or mutating command: %s", command)
					return nil, nil
				}
			}
			err := CheckCompatibility(context.Background(), run, true)
			if (err != nil) != (broken != "") {
				t.Fatalf("broken=%q err=%v", broken, err)
			}
		})
	}
}

func TestPreJoinCompatibilityDoesNotMutateOrRequireLogin(t *testing.T) {
	commands := []string{}
	run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		command := name + " " + strings.Join(arguments, " ")
		commands = append(commands, command)
		if command == "tailscale version --json" {
			return versionPayload(MinimumCompatibleVersion, ""), nil
		}
		if command == "tailscaled --version" {
			return []byte(MinimumCompatibleVersion + "\n  long version: " + MinimumCompatibleVersion + "\n"), nil
		}
		if command == "tailscaled --help" {
			return []byte("  -config string\n"), nil
		}
		t.Fatalf("pre-join check performed another action: %s", command)
		return nil, nil
	}
	if err := CheckCompatibility(context.Background(), run, false); err != nil || len(commands) != 3 {
		t.Fatalf("pre-join compatibility: commands=%v err=%v", commands, err)
	}
}
