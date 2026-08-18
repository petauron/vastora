#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
image=""
release_version=""
bootstrap_port="8080"
ssh_host=""

usage() {
  cat <<'EOF'
Usage: ./setup.sh [--image IMAGE@sha256:DIGEST] [--bootstrap-port PORT] \
  [--ssh-host HOST]

Starts Center on the server loopback interface only. The first-run wizard is
opened through an SSH tunnel, so installation does not require a domain, a TLS
certificate, or an unused public port 443.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --image) image="$2"; shift 2 ;;
    --bootstrap-port) bootstrap_port="$2"; shift 2 ;;
    --ssh-host) ssh_host="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -r "$script_dir/release.env" ]; then
  while IFS='=' read -r release_key release_value; do
    case "$release_key" in
      VASTORA_VERSION) release_version="$release_value" ;;
      VASTORA_CENTER_IMAGE) if [ -z "$image" ]; then image="$release_value"; fi ;;
    esac
  done < "$script_dir/release.env"
fi

if [ -z "$image" ]; then
  echo "This install bundle does not contain a released Center image." >&2
  echo "Create a bundle with scripts/package-center-install.sh or use --image for development." >&2
  exit 2
fi
case "$image" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *) echo "The Center image must be pinned by a complete sha256 digest." >&2; exit 2 ;;
esac
image_digest="${image##*@sha256:}"
case "$image_digest" in
  *[!0-9a-fA-F]*) echo "The Center image sha256 digest is invalid." >&2; exit 2 ;;
esac
case "$bootstrap_port" in
  *[!0-9]*|'') echo "The bootstrap port must be numeric." >&2; exit 2 ;;
esac
if [ "$bootstrap_port" -lt 1 ] || [ "$bootstrap_port" -gt 65535 ]; then
  echo "The bootstrap port must be between 1 and 65535." >&2
  exit 2
fi

for required in awk curl docker mktemp mv; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done
if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required. Install the Docker Compose plugin first." >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "Docker is installed but the daemon is not running." >&2
  exit 1
fi

cd "$script_dir"
if [ -e .env ]; then
  echo "A Center deployment already exists here. Existing configuration was not changed." >&2
  exit 1
fi

temporary_env="$(mktemp "${TMPDIR:-/tmp}/vastora-center-env.XXXXXX")"
cleanup() { rm -f "$temporary_env"; }
trap cleanup EXIT HUP INT TERM
{
  printf 'VASTORA_CENTER_IMAGE=%s\n' "$image"
  printf 'VASTORA_CENTER_BOOTSTRAP_PORT=%s\n' "$bootstrap_port"
} > "$temporary_env"
chmod 0600 "$temporary_env"
mv "$temporary_env" .env
trap - EXIT HUP INT TERM

echo "Validating the loopback-only deployment..."
docker compose config --quiet
echo "Downloading the immutable Center image..."
if ! docker compose pull center; then
  echo "The release image could not be downloaded. Deployment files were kept at $script_dir." >&2
  echo "Check registry access, then run: cd '$script_dir' && docker compose pull center" >&2
  exit 1
fi
echo "Starting Center without opening a public port..."
if ! docker compose up -d center; then
  echo "Center did not start. Run: cd '$script_dir' && docker compose logs center" >&2
  exit 1
fi

attempt=0
until curl -fsS "http://127.0.0.1:$bootstrap_port/healthz" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 30 ]; then
    echo "Center did not become healthy. Run: cd '$script_dir' && docker compose logs center" >&2
    exit 1
  fi
  sleep 2
done

if [ -z "$ssh_host" ] && [ -n "${SSH_CONNECTION:-}" ]; then
  ssh_host="$(printf '%s\n' "$SSH_CONNECTION" | awk '{print $3}')"
fi
if [ -z "$ssh_host" ]; then ssh_host="<server-address>"; fi
ssh_user="${SUDO_USER:-root}"

echo
echo "Vastora Center is ready for first-time setup."
if [ -n "$release_version" ]; then echo "  Release: $release_version"; fi
echo "  Public port 443: unchanged"
echo "  Server listener: 127.0.0.1:$bootstrap_port"
echo
echo "On your computer, keep this SSH tunnel running:"
echo "  ssh -N -L 18082:127.0.0.1:$bootstrap_port $ssh_user@$ssh_host"
echo
echo "Then open:"
echo "  http://127.0.0.1:18082"
echo
echo "The wizard creates the administrator and configures the location and network."
echo "Headscale and public Gateway are enabled later; installation never claims public 443."
