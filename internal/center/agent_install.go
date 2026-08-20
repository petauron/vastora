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
)

const agentInstallScript = `#!/bin/sh
set -eu

center_url=@@CENTER_URL@@
token=@@TOKEN@@

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
  *) echo "Vastora Agent currently supports only Ubuntu 24.04 on amd64." >&2; exit 1 ;;
esac

@@HEADSCALE_BOOTSTRAP@@

temporary="$(mktemp -t vastora-agent.XXXXXX)"
headers="$(mktemp -t vastora-agent-headers.XXXXXX)"
trap 'rm -f "$temporary" "$headers"' EXIT HUP INT TERM
case "$center_url" in
  https://*) curl_protocol="https" ;;
  http://127.0.0.1:*|http://127.0.0.1|http://localhost:*|http://localhost) curl_protocol="http" ;;
  *) echo "Center must use HTTPS; only loopback development addresses may use HTTP." >&2; exit 1 ;;
esac
echo "Downloading the Vastora Agent for $arch..."
curl --proto "=$curl_protocol" --tlsv1.2 --max-filesize 268435456 -fsS \
  -D "$headers" -H "Authorization: Bearer $token" \
  "${center_url%/}/api/v1/agent-binaries/linux/$arch" -o "$temporary"
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
install -m 0755 "$temporary" /usr/local/bin/vastora
echo "Registering this node and starting the system service..."
printf '%s' "$token" | /usr/local/bin/vastora agent install --center-url "$center_url" --token-file -
`

func renderAgentInstallScript(profile AgentEnrollmentInstallProfile, token string) string {
	headscaleBootstrap := ":"
	if profile.HeadscaleCommand != "" {
		headscaleBootstrap = `if ! command -v tailscale >/dev/null 2>&1; then
  echo "Tailscale must be installed before joining this private network." >&2
  exit 1
fi
echo "Joining the private network..."
` + strings.TrimPrefix(profile.HeadscaleCommand, "sudo ")
	}
	return strings.NewReplacer(
		"@@CENTER_URL@@", shellQuote(profile.CenterURL),
		"@@TOKEN@@", shellQuote(token),
		"@@HEADSCALE_BOOTSTRAP@@", headscaleBootstrap,
	).Replace(agentInstallScript)
}

func (s *Store) ValidateAgentEnrollment(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("center: agent enrollment token is invalid")
	}
	var expiresAt string
	var usedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT expires_at, used_at FROM agent_enrollment_tokens WHERE token_hash = ?`, tokenHash(token)).Scan(&expiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) || usedAt.Valid {
		return errors.New("center: agent enrollment token is invalid")
	}
	if err != nil {
		return err
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(s.now()) {
		return errors.New("center: agent enrollment token has expired")
	}
	return nil
}

func (s *Server) agentInstallerAvailable() bool {
	info, err := os.Stat(filepath.Join(s.agentBinariesDir, "linux-amd64"))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return true
}

func (s *Server) handleAgentInstallScript(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	token := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if token == "" || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
		writeError(writer, http.StatusUnauthorized, errors.New("center: agent enrollment token is required"))
		return
	}
	profile, err := s.store.AgentEnrollmentInstallProfile(request.Context(), token)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writer.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="vastora-agent-install.sh"`)
	_, _ = writer.Write([]byte(renderAgentInstallScript(profile, token)))
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
	if operatingSystem != "linux" || architecture != "amd64" {
		writeError(writer, http.StatusNotFound, errors.New("center: Agent binary target is not available"))
		return
	}
	path := filepath.Join(s.agentBinariesDir, operatingSystem+"-"+architecture)
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
