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
	if _, err := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "amd64"); err != nil {
		t.Fatalf("binary download consumed enrollment token: %v", err)
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
	credential, err := store.EnrollAgent(context.Background(), enrollment.Token, "old-version", "linux", "amd64")
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
	request := httptest.NewRequest(http.MethodGet, "https://center.example.com/install/agent.sh", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("public installer status = %d", response.Code)
	}
	loader := response.Body.String()
	for _, expected := range []string{"token=\"${1:-}\"", "exec sudo \"$0\" \"$token\"", "bootstrap_url='https://center.example.com'", "Authorization: Bearer $token", "${bootstrap_url%/}/install/agent.sh", "printf '%s\\n' \"$token\" | sh \"$installer\""} {
		if !strings.Contains(loader, expected) {
			t.Fatalf("installer loader is missing %q:\n%s", expected, loader)
		}
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
	for _, expected := range []string{"center_url='https://center.example.com'", "bootstrap_url='https://center.example.com'", "IFS= read -r token", "command -v \"$required\"", "docker info", "sha256sum", "x86_64|amd64", "aarch64|arm64", "supports Ubuntu 24.04 on x86_64 and ARM64", "--proto \"=$curl_protocol\"", "--max-filesize 268435456", "Authorization: Bearer $token", "${bootstrap_url%/}/api/v1/agent-binaries/linux/$arch", "agent status --data-dir /var/lib/vastora/agent", "Switch this Agent to the requested Center? [y/N]", "Waiting for the requested Center to become reachable", "${center_url%/}/install/agent.sh", "--replace-existing", "x-vastora-sha256:", "failed its SHA-256 integrity check", "failed its version check", "install -m 0755", "agent install --center-url \"$center_url\" --token-file -"} {
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

func TestAgentInstallScriptInstallsTailscaleBeforeJoiningHeadscale(t *testing.T) {
	script := renderAgentInstallScript(AgentEnrollmentInstallProfile{
		CenterURL:        "https://center.example.com",
		HeadscaleCommand: "sudo tailscale up --login-server 'https://headscale.example.com' --auth-key 'one-time-key' --reset",
	}, "https://headscale.example.com")
	for _, expected := range []string{
		"tailscale_version='1.102.3'",
		"Installing Tailscale $tailscale_version...",
		"https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg",
		"https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list",
		"apt-get install -y \"tailscale=$tailscale_version\"",
		"installed_tailscale_version=",
		"Vastora requires Tailscale $tailscale_version",
		"Environment=TS_NO_LOGS_NO_SUPPORT=true",
		"/etc/systemd/system/tailscaled.service.d/90-vastora-privacy.conf",
		"privacy_override_marker=\"$privacy_override.applied\"",
		"cmp -s \"$privacy_override_temporary\" \"$privacy_override\"",
		"systemctl is-active --quiet tailscaled.service",
		"systemctl daemon-reload",
		"systemctl enable --now tailscaled.service",
		"systemctl restart tailscaled.service",
		"printf '%s\\n' v1 >\"$privacy_marker_temporary\"",
		"Joining the private network...",
		"tailscale up --login-server 'https://headscale.example.com' --auth-key 'one-time-key' --reset",
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
	if download, prompt, join := strings.Index(script, "Downloading the Vastora Agent"), strings.Index(script, "Switch this Agent to the requested Center?"), strings.Index(script, "Joining the private network"); download < 0 || prompt < download || join < prompt {
		t.Fatalf("installer does not inspect and confirm an existing Agent before changing Headscale:\n%s", script)
	}
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("private-network installer is not valid POSIX shell: %v\n%s", err, output)
	}
}
