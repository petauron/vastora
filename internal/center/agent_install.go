package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/tailscalehost"
)

const agentInstallLoaderScript = `#!/bin/sh
set -eu

token="${1:-}"
if [ -z "$token" ]; then
  echo "Usage: ./vastora-agent-install.sh <one-time-token>" >&2
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  if ! command -v sudo >/dev/null 2>&1; then
    echo "Root privileges are required and sudo is not installed." >&2
    exit 1
  fi
  exec sudo "$0" "$token"
fi

bootstrap_url=@@CENTER_URL@@
case "$bootstrap_url" in
  https://*) curl_protocol="https" ;;
  http://127.0.0.1:*|http://127.0.0.1|http://localhost:*|http://localhost) curl_protocol="http" ;;
  *) echo "Center must use HTTPS; only loopback development addresses may use HTTP." >&2; exit 1 ;;
esac

installer="$(mktemp -t vastora-agent-installer.XXXXXX)"
trap 'rm -f "$installer"' EXIT HUP INT TERM
curl --proto "=$curl_protocol" --tlsv1.2 --max-filesize 1048576 -fsS \
  -H "Authorization: Bearer $token" \
  "${bootstrap_url%/}/install/agent.sh" -o "$installer"
printf '%s\n' "$token" | sh "$installer"
`

const agentInstallScript = `#!/bin/sh
set -eu

center_url=@@CENTER_URL@@
bootstrap_url=@@BOOTSTRAP_URL@@
ca_fingerprint=@@CA_FINGERPRINT@@
IFS= read -r token
if [ -z "$token" ]; then
  echo "The one-time Agent token is required." >&2
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "Run this installer with sudo." >&2
  exit 1
fi

for required in curl systemctl docker sha256sum awk; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done
if ! docker info >/dev/null 2>&1; then
  echo "Docker is installed but the daemon is not running." >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) echo "Vastora Agent supports Ubuntu 24.04 on x86_64 and ARM64." >&2; exit 1 ;;
esac

temporary="$(mktemp -t vastora-agent.XXXXXX)"
headers="$(mktemp -t vastora-agent-headers.XXXXXX)"
trap 'rm -f "$temporary" "$headers"' EXIT HUP INT TERM
case "$bootstrap_url" in
  https://*) curl_protocol="https" ;;
  http://127.0.0.1:*|http://127.0.0.1|http://localhost:*|http://localhost) curl_protocol="http" ;;
  *) echo "Center must use HTTPS; only loopback development addresses may use HTTP." >&2; exit 1 ;;
esac
echo "Downloading the Vastora Agent for $arch..."
curl --proto "=$curl_protocol" --tlsv1.2 --max-filesize 268435456 -fsS \
  -D "$headers" -H "Authorization: Bearer $token" \
  "${bootstrap_url%/}/api/v1/agent-binaries/linux/$arch" -o "$temporary"
expected_digest="$(awk 'tolower($1) == "x-vastora-sha256:" {gsub("\\r", "", $2); value=tolower($2)} END {print value}' "$headers")"
expected_version="$(awk 'tolower($1) == "x-vastora-version:" {sub("^[^:]*:[[:space:]]*", ""); gsub("\\r", ""); value=$0} END {print value}' "$headers")"
actual_digest="$(sha256sum "$temporary" | awk '{print tolower($1)}')"
if [ -z "$expected_digest" ] || [ "$expected_digest" != "$actual_digest" ]; then
  echo "The downloaded Agent failed its SHA-256 integrity check." >&2
  exit 1
fi
chmod 0755 "$temporary"
if [ -z "$expected_version" ] || [ "$("$temporary" version)" != "$expected_version" ]; then
  echo "The downloaded Agent failed its version check." >&2
  exit 1
fi

replace_existing=0
resume_install=0
if [ -s /var/lib/vastora/agent/agent.db ]; then
  if ! install_state="$("$temporary" agent install-state --data-dir /var/lib/vastora/agent 2>&1)"; then
    echo "An existing Vastora Agent was found, but its installation state could not be inspected." >&2
    echo "$install_state" >&2
    exit 1
  fi
  case "$install_state" in
    fresh) resume_install=1 ;;
    replace) resume_install=1; replace_existing=1 ;;
    none) ;;
    *) echo "The stored Vastora Agent installation state is invalid." >&2; exit 1 ;;
  esac
fi
if [ "$resume_install" -eq 0 ] && { [ -s /var/lib/vastora/agent/agent.db ] || systemctl is-enabled --quiet vastora-agent.service 2>/dev/null || systemctl is-active --quiet vastora-agent.service 2>/dev/null; }; then
  if ! existing_status="$("$temporary" agent status --data-dir /var/lib/vastora/agent 2>&1)"; then
    echo "An existing Vastora Agent was found, but its Center registration could not be inspected." >&2
    echo "$existing_status" >&2
    echo "The existing Agent was not changed." >&2
    exit 1
  fi
  printf '%s\n' "This server is already registered with a Center:" >&2
  printf '%s\n' "$existing_status" >&2
  printf '%s\n' "Requested Center: $center_url" >&2
  printf '%s\n' "Switching keeps running Docker apps and local Agent state, but does not copy their records from the previous Center." >&2
  if ! ( : </dev/tty ) 2>/dev/null; then
    echo "Interactive confirmation is required to switch an existing Agent." >&2
    exit 1
  fi
  printf 'Switch this Agent to the requested Center? [y/N] ' >/dev/tty
  IFS= read -r answer </dev/tty
  case "$answer" in
    y|Y|yes|Yes|YES) replace_existing=1 ;;
    *) echo "The existing Agent was not changed." >&2; exit 0 ;;
  esac
fi

if [ "$resume_install" -eq 0 ]; then
@@HEADSCALE_BOOTSTRAP@@
fi

case "$center_url" in
  https://*) center_protocol="https" ;;
  http://127.0.0.1:*|http://127.0.0.1|http://localhost:*|http://localhost) center_protocol="http" ;;
  *) echo "Center must use HTTPS; only loopback development addresses may use HTTP." >&2; exit 1 ;;
esac
echo "Waiting for the requested Center to become reachable..."
center_ready=0
attempt=1
while [ "$attempt" -le 15 ]; do
  if curl --proto "=$center_protocol" --tlsv1.2 --max-filesize 1048576 -fs \
    -H "Authorization: Bearer $token" "${center_url%/}/install/agent.sh" -o /dev/null 2>/dev/null; then
    center_ready=1
    break
  fi
  attempt=$((attempt + 1))
  sleep 2
done
if [ "$center_ready" -ne 1 ]; then
  echo "The requested Center did not become reachable after joining the private network. The existing Agent was not migrated." >&2
  exit 1
fi

install -m 0755 "$temporary" /usr/local/bin/vastora
echo "Registering this node and starting the system service..."
if [ "$replace_existing" -eq 1 ]; then
  printf '%s' "$token" | /usr/local/bin/vastora agent install --center-url "$center_url" --token-file - --ca-fingerprint "$ca_fingerprint" --replace-existing
else
  printf '%s' "$token" | /usr/local/bin/vastora agent install --center-url "$center_url" --token-file - --ca-fingerprint "$ca_fingerprint"
fi
`

func renderAgentInstallLoader(centerURL string) string {
	return strings.ReplaceAll(agentInstallLoaderScript, "@@CENTER_URL@@", shellQuote(centerURL))
}

func renderAgentInstallScript(profile AgentEnrollmentInstallProfile, bootstrapURL string) string {
	headscaleBootstrap := ":"
	if profile.HeadscaleCommand != "" {
		prepareArguments := "--control-url " + shellQuote(profile.HeadscaleURL)
		for _, address := range profile.HeadscaleAddresses {
			prepareArguments += " --control-address " + shellQuote(address)
		}
		headscaleBootstrap = `tailscale_version=@@TAILSCALE_VERSION@@
tailscale_ownership=external
if ! command -v tailscale >/dev/null 2>&1; then
  tailscale_ownership=managed
  "$temporary" agent prepare-tailscale @@TAILSCALE_PREPARE_ARGUMENTS@@ --configure-only
  if ! command -v apt-get >/dev/null 2>&1; then
    echo "Vastora can install Tailscale automatically only on Ubuntu 24.04." >&2
    exit 1
  fi
  echo "Installing Tailscale $tailscale_version..."
  install -d -m 0755 /usr/share/keyrings /etc/apt/sources.list.d
  curl --proto '=https' --tlsv1.2 --max-filesize 1048576 -fsS \
    https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg \
    -o /usr/share/keyrings/tailscale-archive-keyring.gpg
  curl --proto '=https' --tlsv1.2 --max-filesize 1048576 -fsS \
    https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list \
    -o /etc/apt/sources.list.d/tailscale.list
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y "tailscale=$tailscale_version" tailscale-archive-keyring
fi
installed_tailscale_version="$(tailscale version | awk 'NR == 1 {print $1}')"
if [ "$installed_tailscale_version" != "$tailscale_version" ]; then
  echo "Vastora requires Tailscale $tailscale_version; found $installed_tailscale_version." >&2
  exit 1
fi
"$temporary" agent prepare-tailscale @@TAILSCALE_PREPARE_ARGUMENTS@@
echo "Joining the private network..."
` + strings.TrimPrefix(profile.HeadscaleCommand, "sudo ") + `
install -d -m 0700 /var/lib/vastora/agent
if [ -f /var/lib/vastora/agent/host-install.env ]; then
  while IFS='=' read -r host_state_key host_state_value; do
    case "$host_state_key=$host_state_value" in
      TAILSCALE_OWNERSHIP=managed) tailscale_ownership=managed ;;
    esac
  done </var/lib/vastora/agent/host-install.env
fi
host_state_temporary="$(mktemp /var/lib/vastora/agent/.host-install.XXXXXX)"
printf '%s\n' 'HOST_STATE_VERSION=1' "TAILSCALE_OWNERSHIP=$tailscale_ownership" 'TAILSCALE_ENROLLED=1' >"$host_state_temporary"
chmod 0600 "$host_state_temporary"
mv "$host_state_temporary" /var/lib/vastora/agent/host-install.env
`
		headscaleBootstrap = strings.ReplaceAll(headscaleBootstrap, "@@TAILSCALE_VERSION@@", shellQuote(tailscalehost.SupportedVersion))
		headscaleBootstrap = strings.ReplaceAll(headscaleBootstrap, "@@TAILSCALE_PREPARE_ARGUMENTS@@", prepareArguments)
	} else {
		headscaleBootstrap = `install -d -m 0700 /var/lib/vastora/agent
if [ ! -f /var/lib/vastora/agent/host-install.env ]; then
  host_state_temporary="$(mktemp /var/lib/vastora/agent/.host-install.XXXXXX)"
  printf '%s\n' 'HOST_STATE_VERSION=1' 'TAILSCALE_OWNERSHIP=none' 'TAILSCALE_ENROLLED=0' >"$host_state_temporary"
  chmod 0600 "$host_state_temporary"
  mv "$host_state_temporary" /var/lib/vastora/agent/host-install.env
fi`
	}
	return strings.NewReplacer(
		"@@CENTER_URL@@", shellQuote(profile.CenterURL),
		"@@BOOTSTRAP_URL@@", shellQuote(bootstrapURL),
		"@@CA_FINGERPRINT@@", shellQuote(profile.CAFingerprint),
		"@@HEADSCALE_BOOTSTRAP@@", headscaleBootstrap,
	).Replace(agentInstallScript)
}

func (s *Store) ValidateAgentEnrollment(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("center: agent enrollment token is invalid")
	}
	var expiresAt string
	var usedAt, recoveryExpiresAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT tokens.expires_at, tokens.used_at, operations.expires_at
		FROM agent_enrollment_tokens tokens
		LEFT JOIN agent_enrollment_operations operations ON operations.token_hash = tokens.token_hash
		WHERE tokens.token_hash = ?`, tokenHash(token)).Scan(&expiresAt, &usedAt, &recoveryExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: agent enrollment token is invalid")
	}
	if err != nil {
		return err
	}
	validUntil := expiresAt
	if usedAt.Valid {
		if !recoveryExpiresAt.Valid {
			return errors.New("center: agent enrollment token is invalid")
		}
		validUntil = recoveryExpiresAt.String
	}
	expires, err := time.Parse(time.RFC3339Nano, validUntil)
	if err != nil || !expires.After(s.now()) {
		return errors.New("center: agent enrollment token has expired")
	}
	return nil
}

func (s *Server) agentInstallerAvailable() bool {
	for _, architecture := range []string{platform.AMD64, platform.ARM64} {
		target, _ := platform.Parse(platform.Linux, architecture)
		info, err := os.Stat(filepath.Join(s.agentBinariesDir, target.AgentBinaryName()))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func (s *Server) handleAgentInstallScript(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	token := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if token == "" || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
		centerURL, err := agentInstallerRequestURL(request)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		writer.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		writer.Header().Set("Content-Disposition", `inline; filename="vastora-agent-install.sh"`)
		_, _ = writer.Write([]byte(renderAgentInstallLoader(centerURL)))
		return
	}
	profile, err := s.store.AgentEnrollmentInstallProfile(request.Context(), token)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	bootstrapURL, err := agentInstallerRequestURL(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="vastora-agent-install.sh"`)
	_, _ = writer.Write([]byte(renderAgentInstallScript(profile, bootstrapURL)))
}

func agentInstallerRequestURL(request *http.Request) (string, error) {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded != "" {
		scheme = forwarded
	}
	centerURL, err := NormalizeAgentConnectURL(scheme + "://" + request.Host)
	if err != nil {
		return "", errors.New("center: installer URL must use HTTPS; only loopback development addresses may use HTTP")
	}
	return centerURL, nil
}

func (s *Server) handleAgentBinary(writer http.ResponseWriter, request *http.Request) {
	token := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if err := s.store.ValidateAgentEnrollment(request.Context(), token); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	s.serveAgentBinary(writer, request, request.PathValue("os"), request.PathValue("arch"))
}

func (s *Server) handleAgentUpdateBinary(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	if err := s.store.authenticateAgent(request.Context(), request.PathValue("id"), credential); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	s.serveAgentBinary(writer, request, request.PathValue("os"), request.PathValue("arch"))
}

func (s *Server) serveAgentBinary(writer http.ResponseWriter, request *http.Request, operatingSystem, architecture string) {
	target, err := platform.Parse(operatingSystem, architecture)
	if err != nil {
		writeError(writer, http.StatusNotFound, errors.New("center: Agent binary target is not available"))
		return
	}
	path := filepath.Join(s.agentBinariesDir, target.AgentBinaryName())
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		writeError(writer, http.StatusNotFound, errors.New("center: Agent binary target is not available"))
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(writer, http.StatusNotFound, errors.New("center: Agent binary target is not available"))
		return
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		writeError(writer, http.StatusInternalServerError, errors.New("center: inspect Agent binary"))
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(writer, http.StatusInternalServerError, errors.New("center: inspect Agent binary"))
		return
	}
	writer.Header().Set("Content-Disposition", `attachment; filename="vastora"`)
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Vastora-Version", Version)
	writer.Header().Set("X-Vastora-SHA256", hex.EncodeToString(digest.Sum(nil)))
	http.ServeContent(writer, request, "vastora", info.ModTime(), file)
}
