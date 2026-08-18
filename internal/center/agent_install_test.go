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

	enrollment, err := store.CreateAgentEnrollment(context.Background(), testSiteID(t, store))
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
	if response.Code != http.StatusNotFound {
		t.Fatalf("arm64 binary download status = %d", response.Code)
	}
	if _, err := store.EnrollAgent(context.Background(), enrollment.Token, "downloaded-agent", "test"); err != nil {
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
	enrollment, err := store.CreateAgentEnrollment(context.Background(), testSiteID(t, store))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.EnrollAgent(context.Background(), enrollment.Token, "update-node", "old-version")
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
	request := httptest.NewRequest(http.MethodGet, "/install/agent.sh", nil)
	response := httptest.NewRecorder()
	NewServer(store, "", false).Handler().ServeHTTP(response, request)
	script := response.Body.String()
	for _, expected := range []string{"command -v \"$required\"", "docker info", "sha256sum", "x86_64|amd64", "supports only Ubuntu 24.04 on amd64", "--proto \"=$curl_protocol\"", "--max-filesize 268435456", "Authorization: Bearer $token", "${center_url%/}/api/v1/agent-binaries/linux/$arch", "x-vastora-sha256:", "failed its SHA-256 integrity check", "failed its version check", "install -m 0755", "agent install --center-url"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("installer is missing %q:\n%s", expected, script)
		}
	}
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("installer is not valid POSIX shell: %v\n%s", err, output)
	}
}
