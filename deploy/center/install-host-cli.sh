#!/bin/sh
set -eu

image=""
version=""
install_dir="/opt/vastora/center"
target="${VASTORA_HOST_CLI_PATH:-/usr/local/bin/vastora}"
systemd_unit_dir="${VASTORA_SYSTEMD_UNIT_DIR:-/etc/systemd/system}"
agent_unit="$systemd_unit_dir/vastora-agent.service"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --image) image="$2"; shift 2 ;;
    --version) version="$2"; shift 2 ;;
    --install-dir) install_dir="$2"; shift 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "Installing the Vastora host command requires root." >&2
  exit 1
fi
case "$image" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *) echo "The host command image must be pinned by a complete sha256 digest." >&2; exit 2 ;;
esac
image_digest="${image##*@sha256:}"
case "$image_digest" in
  *[!0-9a-fA-F]*) echo "The host command image digest is invalid." >&2; exit 2 ;;
esac
case "$target" in
  /*/vastora) ;;
  *) echo "The Vastora host command path must be an absolute path ending in /vastora." >&2; exit 2 ;;
esac
case "$target" in
  /vastora|/bin/vastora|/sbin/vastora|/usr/vastora|/var/vastora|*/../*|*/..|*/./*|*/.|*/|*//*)
    echo "Refusing unsafe Vastora host command path: $target" >&2
    exit 2
    ;;
esac
for required in docker grep install mktemp mv; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done

temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-host-cli.XXXXXX")"
container=""
had_previous=no
cleanup() {
  status=$?
  if [ -n "$container" ]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
  rm -rf "$temporary_dir"
  exit "$status"
}
trap cleanup EXIT HUP INT TERM

container="$(docker create "$image")"
docker cp "$container:/usr/local/bin/vastora" "$temporary_dir/vastora"
docker rm -f "$container" >/dev/null
container=""
chmod 0755 "$temporary_dir/vastora"
candidate_version="$("$temporary_dir/vastora" version)"
if [ -z "$version" ]; then
  version="$candidate_version"
fi
if [ "$candidate_version" != "$version" ]; then
  echo "The Center image contains an unexpected Vastora command version." >&2
  exit 1
fi
if [ -e "$target" ]; then
  if [ ! -f "$target" ] || [ -L "$target" ] || ! "$target" help 2>&1 | grep -Fq 'Vastora control-plane tools'; then
    echo "Refusing to replace an unrecognized executable at $target." >&2
    exit 1
  fi
  install -m 0755 "$target" "$target.previous"
  had_previous=yes
fi

target_dir="$(dirname "$target")"
install -d -m 0755 "$target_dir"
staged="$(mktemp "$target_dir/.vastora-install.XXXXXX")"
install -m 0755 "$temporary_dir/vastora" "$staged"
mv "$staged" "$target"

if [ -f "$agent_unit" ] && grep -Fq 'Description=Vastora Agent' "$agent_unit"; then
  if ! command -v systemctl >/dev/null 2>&1; then
    echo "systemctl is required to restart the co-located Vastora Agent." >&2
    exit 1
  fi
  if ! systemctl restart vastora-agent.service || ! systemctl is-active --quiet vastora-agent.service; then
    echo "The Agent did not accept the new Vastora executable; restoring the previous command." >&2
    if [ "$had_previous" = yes ]; then
      staged="$(mktemp "$target_dir/.vastora-rollback.XXXXXX")"
      install -m 0755 "$target.previous" "$staged"
      mv "$staged" "$target"
      systemctl restart vastora-agent.service || true
    else
      rm -f "$target"
    fi
    exit 1
  fi
fi

install -m 0644 /dev/null "$install_dir/.host-cli-installed"
echo "Installed local management command: vastora status | update | uninstall"
