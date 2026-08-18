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

center_url=""
token=""
name=""
roles="worker,gateway"
capabilities="docker,gateway,tunnel"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --center-url) center_url="$2"; shift 2 ;;
    --token) token="$2"; shift 2 ;;
    --name) name="$2"; shift 2 ;;
    --roles) roles="$2"; shift 2 ;;
    --capabilities) capabilities="$2"; shift 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "Run this installer with sudo." >&2
  exit 1
fi
if [ -z "$center_url" ] || [ -z "$token" ] || [ -z "$name" ]; then
  echo "--center-url, --token, and --name are required." >&2
  exit 2
fi

for required in curl systemctl docker; do
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
  *) echo "This CPU architecture is not supported." >&2; exit 1 ;;
esac

temporary="$(mktemp -t vastora-agent.XXXXXX)"
trap 'rm -f "$temporary"' EXIT HUP INT TERM
echo "Downloading the Vastora Agent for $arch..."
curl -fsSL -H "Authorization: Bearer $token" "${center_url%/}/api/v1/agent-binaries/linux/$arch" -o "$temporary"
install -m 0755 "$temporary" /usr/local/bin/vastora
echo "Registering this node and starting the system service..."
printf '%s' "$token" | /usr/local/bin/vastora agent install --center-url "$center_url" --token-file - --name "$name" --roles "$roles" --capabilities "$capabilities"
`

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
	for _, target := range []string{"linux-amd64", "linux-arm64"} {
		info, err := os.Stat(filepath.Join(s.agentBinariesDir, target))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func (s *Server) handleAgentInstallScript(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	writer.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = writer.Write([]byte(agentInstallScript))
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
	if operatingSystem != "linux" || (architecture != "amd64" && architecture != "arm64") {
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
