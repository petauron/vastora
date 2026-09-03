package center

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentBinaryDownloadRequiresLiveEnrollmentAndDoesNotConsumeIt(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	binaries := t.TempDir()
	if err := os.WriteFile(filepath.Join(binaries, "linux-amd64"), []byte("binary-linux-amd64"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binaries, "linux-arm64"), []byte("binary-linux-arm64"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, "", false).WithAgentBinaries(binaries)
	if !server.agentInstallerAvailable() {
		t.Fatal("complete Agent binary set was not detected")
	}
	handler := server.Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-binaries/linux/amd64", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated binary download status = %d", response.Code)
	}

	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "downloaded-agent", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/agent-binaries/linux/amd64", nil)
	request.Header.Set("Authorization", "Bearer "+enrollment.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	result := response.Result()
	body, readErr := io.ReadAll(result.Body)
	result.Body.Close()
	if readErr != nil || response.Code != http.StatusOK || string(body) != "binary-linux-amd64" {
		t.Fatalf("authorized binary download failed: status=%d body=%q err=%v", response.Code, body, readErr)
	}
	wantDigest := sha256.Sum256(body)
	if got := response.Header().Get("X-Vastora-SHA256"); got != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("initial Agent download digest = %q", got)
	}
	if response.Header().Get("X-Vastora-Version") != Version {
		t.Fatalf("initial Agent download version = %q", response.Header().Get("X-Vastora-Version"))
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/agent-binaries/linux/arm64", nil)
	request.Header.Set("Authorization", "Bearer "+enrollment.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "binary-linux-arm64" {
		t.Fatalf("arm64 binary download failed: status=%d body=%q", response.Code, response.Body.String())
	}
	if _, err := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t)); err != nil {
		t.Fatalf("binary download consumed enrollment token: %v", err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/agent-binaries/linux/amd64", nil)
	request.Header.Set("Authorization", "Bearer "+enrollment.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "binary-linux-amd64" {
		t.Fatalf("recovery binary download failed after enrollment commit: status=%d body=%q", response.Code, response.Body.String())
	}
	profile, err := store.AgentEnrollmentInstallProfile(context.Background(), enrollment.Token)
	if err != nil || profile.Name != "downloaded-agent" {
		t.Fatalf("recovery installer profile is unavailable after enrollment commit: profile=%#v err=%v", profile, err)
	}
}

func TestEnrolledAgentCanDownloadAuthenticatedUpdate(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	binaries := t.TempDir()
	payload := []byte("authenticated-agent-update")
	if err := os.WriteFile(filepath.Join(binaries, "linux-amd64"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "update-node", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.EnrollAgent(context.Background(), enrollment.Token, "old-version", "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).WithAgentBinaries(binaries).Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+credential.ID+"/binary/linux/amd64", nil)
	request.Header.Set("Authorization", "Bearer "+credential.Credential)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	result := response.Result()
	body, readErr := io.ReadAll(result.Body)
	result.Body.Close()
	wantDigest := sha256.Sum256(payload)
	if readErr != nil || response.Code != http.StatusOK || string(body) != string(payload) {
		t.Fatalf("Agent update download failed: status=%d body=%q err=%v", response.Code, body, readErr)
	}
	if got := response.Header().Get("X-Vastora-SHA256"); got != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("Agent update digest = %q", got)
	}
	if response.Header().Get("X-Vastora-Version") != Version {
		t.Fatalf("Agent update version header = %q", response.Header().Get("X-Vastora-Version"))
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+credential.ID+"/binary/linux/amd64", nil)
	request.Header.Set("Authorization", "Bearer wrong-credential")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid Agent credential status = %d", response.Code)
	}
}

func TestAgentInstallScriptUsesTLSAuthenticatedBinaryDownload(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewServer(store, "", false).Handler()
	dockerRequest := httptest.NewRequest(http.MethodGet, "https://center.example.com/install/docker.sh", nil)
	dockerResponse := httptest.NewRecorder()
	handler.ServeHTTP(dockerResponse, dockerRequest)
	if dockerResponse.Code != http.StatusOK {
		t.Fatalf("Docker installer status = %d", dockerResponse.Code)
	}
	dockerScript := dockerResponse.Body.String()
	for _, expected := range []string{"#!/bin/sh", "debian:12", "debian:13", "ubuntu:22.04", "ubuntu:24.04", "ubuntu:26.04", "amd64|arm64", "command -v docker", "docker info", "https://download.docker.com/linux/$distro/gpg", "docker-ce", "docker-compose-plugin", "systemctl enable --now docker"} {
		if !strings.Contains(dockerScript, expected) {
			t.Fatalf("Docker installer is missing %q:\n%s", expected, dockerScript)
		}
	}
	if dockerResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Docker installer cache policy = %q", dockerResponse.Header().Get("Cache-Control"))
	}
	if dockerResponse.Header().Get("Content-Disposition") != `attachment; filename="install-docker.sh"` {
		t.Fatalf("Docker installer disposition = %q", dockerResponse.Header().Get("Content-Disposition"))
	}
	dockerCommand := exec.Command("sh", "-n")
	dockerCommand.Stdin = strings.NewReader(dockerScript)
	if output, err := dockerCommand.CombinedOutput(); err != nil {
		t.Fatalf("Docker installer is not valid POSIX shell: %v\n%s", err, output)
	}
	request := httptest.NewRequest(http.MethodGet, "https://center.example.com/install/agent.sh", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("public installer status = %d", response.Code)
	}
	loader := response.Body.String()
	for _, expected := range []string{"token=\"${1:-}\"", "ca_certificate=\"${2:-}\"", "bootstrap_uses_ca=\"${3:-0}\"", "exec sudo \"$0\" \"$token\" \"$ca_certificate\" \"$bootstrap_uses_ca\"", "bootstrap_url='https://center.example.com'", "bootstrap_curl", "--cacert \"$ca_certificate\"", "command -v docker", "docker info", "${bootstrap_url%/}/install/docker.sh", "sh \"$docker_installer\"", "Authorization: Bearer $token", "${bootstrap_url%/}/install/agent.sh", "printf '%s\\n' \"$token\" | sh \"$installer\" \"$ca_certificate\" \"$bootstrap_uses_ca\""} {
		if !strings.Contains(loader, expected) {
			t.Fatalf("installer loader is missing %q:\n%s", expected, loader)
		}
	}
	if dockerCheck, dockerInstall, agentDownload := strings.Index(loader, "command -v docker"), strings.Index(loader, "/install/docker.sh"), strings.Index(loader, "Authorization: Bearer $token"); dockerCheck < 0 || dockerInstall < dockerCheck || agentDownload < dockerInstall {
		t.Fatal("installer loader must check and, when necessary, install Docker before downloading the authenticated Agent installer")
	}
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "bound-node", CenterURL: "https://center.example.com", Gateway: true, Tunnel: true})
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "https://center.example.com/install/agent.sh", nil)
	request.Header.Set("Authorization", "Bearer "+enrollment.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated installer status = %d, body = %q", response.Code, response.Body.String())
	}
	script := response.Body.String()
	for _, expected := range []string{"center_url='https://center.example.com'", "bootstrap_url='https://center.example.com'", "ca_certificate=\"${1:-}\"", "bootstrap_uses_ca=\"${2:-0}\"", "bootstrap_curl", "center_curl", "--cacert \"$ca_certificate\"", "IFS= read -r token", "command -v \"$required\"", "docker info", "sha256sum", "dpkg --print-architecture", "amd64|arm64", "Vastora Agent requires amd64 or arm64", "--proto \"=$curl_protocol\"", "--max-filesize 268435456", "Authorization: Bearer $token", "${bootstrap_url%/}/api/v1/agent-binaries/linux/$arch", "agent install-state --data-dir /var/lib/vastora/agent", "resume_install=1", "agent status --data-dir /var/lib/vastora/agent", "Switch this Agent to the requested Center? [y/N]", "switch-control-plane begin", "switch-control-plane rollback", "switch-control-plane commit", "Waiting for the requested Center to become reachable", "${center_url%/}/install/agent.sh", "--replace-existing", "x-vastora-sha256:", "failed its SHA-256 integrity check", "failed its version check", "install -m 0755", "agent install --center-url \"$center_url\" --token-file -", "--ca-certificate \"$ca_certificate\""} {
		if !strings.Contains(script, expected) {
			t.Fatalf("installer is missing %q:\n%s", expected, script)
		}
	}
	if strings.Contains(script, enrollment.Token) {
		t.Fatal("authenticated installer embeds its one-time token")
	}
	for _, hidden := range []string{"--name", "--roles", "--capabilities"} {
		if strings.Contains(script, hidden) {
			t.Fatalf("installer exposes Center-owned option %q", hidden)
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("installer cache policy = %q", response.Header().Get("Cache-Control"))
	}
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("installer is not valid POSIX shell: %v\n%s", err, output)
	}
	command = exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(loader)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("installer loader is not valid POSIX shell: %v\n%s", err, output)
	}
}

func TestAgentInstallScriptSupportedHostSystems(t *testing.T) {
	script := renderAgentInstallScript(AgentEnrollmentInstallProfile{CenterURL: "https://center.example.com"}, "https://center.example.com")
	start := strings.Index(script, "if [ ! -r /etc/os-release ]; then")
	end := strings.Index(script, "\nfor required in ")
	download := strings.Index(script, "Downloading the Vastora Agent")
	if start < 0 || end <= start || download <= end {
		t.Fatal("host system preflight must run before downloading or changing the Agent")
	}
	// Execute only the OS selection block with fixture metadata. This never
	// invokes the installer, package manager, Docker, or the network.
	preflight := script[start:end]
	for _, test := range []struct {
		name      string
		osRelease string
		want      string
	}{
		{"debian12", "ID=debian\nVERSION_ID=12\n", "debian:bookworm"},
		{"debian13", "ID=debian\nVERSION_ID=13\n", "debian:trixie"},
		{"ubuntu2204", "ID=ubuntu\nVERSION_ID=22.04\n", "ubuntu:jammy"},
		{"ubuntu2404", "ID=ubuntu\nVERSION_ID=24.04\n", "ubuntu:noble"},
		{"ubuntu2604", "ID=ubuntu\nVERSION_ID=26.04\n", "ubuntu:resolute"},
		{"debian11", "ID=debian\nVERSION_ID=11\n", ""},
		{"ubuntu2004", "ID=ubuntu\nVERSION_ID=20.04\n", ""},
		{"ubuntuInterim", "ID=ubuntu\nVERSION_ID=25.10\n", ""},
		{"futureDebian", "ID=debian\nVERSION_ID=14\n", ""},
		{"derivative", "ID=linuxmint\nID_LIKE=ubuntu\nVERSION_ID=22.04\n", ""},
		{"missingVersion", "ID=debian\n", ""},
		{"missingMetadata", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := filepath.Join(t.TempDir(), "os-release")
			if test.osRelease != "" {
				if err := os.WriteFile(metadata, []byte(test.osRelease), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("sh", "-c", "set -eu\n"+strings.ReplaceAll(preflight, "/etc/os-release", shellQuote(metadata))+"\nprintf '%s:%s\\n' \"$distro\" \"$codename\"")
			command.Env = append(os.Environ(), "ID=", "VERSION_ID=", "PRETTY_NAME=")
			output, err := command.CombinedOutput()
			if test.want == "" {
				if err == nil || !strings.Contains(string(output), "Unsupported system:") && !strings.Contains(string(output), "Cannot identify this server:") {
					t.Fatalf("unsupported system was not rejected clearly: err=%v output=%s", err, output)
				}
				return
			}
			if err != nil || strings.TrimSpace(string(output)) != test.want {
				t.Fatalf("host selection = %q, want %q: %v", output, test.want, err)
			}
		})
	}
}

func TestAgentInstallScriptInstallsTailscaleBeforeJoiningHeadscale(t *testing.T) {
	script := renderAgentInstallScript(AgentEnrollmentInstallProfile{
		CenterURL:          "https://center.example.com",
		HeadscaleCommand:   "sudo tailscale login --login-server 'https://headscale.example.com' --auth-key 'one-time-key'",
		HeadscaleURL:       "https://headscale.example.com",
		HeadscaleAddresses: []string{"203.0.113.10"},
	}, "https://headscale.example.com")
	for _, expected := range []string{
		"tailscale_version='1.102.3'",
		"Installing Tailscale $tailscale_version for $distro $VERSION_ID ($codename)...",
		"https://pkgs.tailscale.com/stable/$distro/$codename.noarmor.gpg",
		"https://pkgs.tailscale.com/stable/$distro/$codename.tailscale-keyring.list",
		"chmod 0644 /usr/share/keyrings/tailscale-archive-keyring.gpg /etc/apt/sources.list.d/tailscale.list",
		"apt-get install -y \"tailscale=$tailscale_version\"",
		"\"$temporary\" agent check-tailscale\n",
		"\"$temporary\" agent check-tailscale --require-running",
		"agent prepare-tailscale --control-url 'https://headscale.example.com' --control-address '203.0.113.10' --configure-only",
		"agent prepare-tailscale --control-url 'https://headscale.example.com' --control-address '203.0.113.10'",
		"Joining the private network...",
		"tailscale login --login-server 'https://headscale.example.com' --auth-key 'one-time-key'",
		"TAILSCALE_OWNERSHIP=$tailscale_ownership",
		"TAILSCALE_ENROLLED=1",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("private-network installer is missing %q:\n%s", expected, script)
		}
	}
	if strings.Contains(script, "Tailscale must be installed before") {
		t.Fatal("private-network installer still requires Tailscale to be installed manually")
	}
	if strings.Contains(script, "stable/ubuntu/noble") || strings.Contains(script, "only on Ubuntu 24.04") {
		t.Fatal("private-network installer still hard-codes Ubuntu 24.04")
	}
	if strings.Contains(script, "installed_tailscale_version=") || strings.Contains(script, "--allow-downgrades") {
		t.Fatal("installer still has a separate exact-version check or a downgrade path")
	}
	check := strings.Index(script, "\"$temporary\" agent check-tailscale\n")
	prepare := strings.Index(script, "\"$temporary\" agent prepare-tailscale --control-url 'https://headscale.example.com' --control-address '203.0.113.10'\n")
	joined := strings.Index(script, "tailscale login --login-server")
	runtimeCheck := strings.Index(script, "\"$temporary\" agent check-tailscale --require-running")
	ownership := strings.Index(script, "HOST_STATE_VERSION=1")
	if check < 0 || prepare < check || joined < prepare || runtimeCheck < joined || ownership < runtimeCheck {
		t.Fatal("compatibility and running-daemon checks must precede mutation and ownership recording")
	}
	if download, prompt, join := strings.Index(script, "Downloading the Vastora Agent"), strings.Index(script, "Switch this Agent to the requested Center?"), strings.Index(script, "Joining the private network"); download < 0 || prompt < download || join < prompt {
		t.Fatalf("installer does not inspect and confirm an existing Agent before changing Headscale:\n%s", script)
	}
	if state, guardedJoin := strings.Index(script, "agent install-state"), strings.Index(script, "if [ \"$resume_install\" -eq 0 ]; then\n"); state < 0 || guardedJoin < state {
		t.Fatalf("installer does not skip private-network mutation while resuming:\n%s", script)
	}
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("private-network installer is not valid POSIX shell: %v\n%s", err, output)
	}
}
