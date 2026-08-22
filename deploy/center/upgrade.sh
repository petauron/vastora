#!/bin/sh
set -eu

source_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
install_dir="/opt/vastora/center"

usage() {
  cat <<'EOF'
Usage: ./upgrade.sh [--install-dir DIR]

Replaces only Vastora-managed deployment files, preserves the existing Center
configuration, pulls the immutable release image, and waits for Center health.
Database migrations and their backups are handled by Center before it serves.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --install-dir) install_dir="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$install_dir" in
  /*) ;;
  *) echo "The installation directory must be absolute." >&2; exit 2 ;;
esac
if [ ! -f "$install_dir/.env" ] || [ ! -f "$install_dir/compose.yaml" ]; then
  echo "$install_dir is not a complete Center installation." >&2
  exit 1
fi
for required_file in setup.sh upgrade.sh compose.yaml release.env; do
  if [ ! -f "$source_dir/$required_file" ]; then
    echo "The upgrade bundle is incomplete: missing $required_file" >&2
    exit 1
  fi
done
for required in awk curl docker install mktemp mv; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done
if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required." >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "Docker is installed but the daemon is not running." >&2
  exit 1
fi

new_image="$(awk -F= '$1 == "VASTORA_CENTER_IMAGE" {sub(/^[^=]*=/, ""); print; exit}' "$source_dir/release.env")"
new_version="$(awk -F= '$1 == "VASTORA_VERSION" {sub(/^[^=]*=/, ""); print; exit}' "$source_dir/release.env")"
case "$new_image" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *) echo "The release image is not pinned by a complete sha256 digest." >&2; exit 2 ;;
esac
image_digest="${new_image##*@sha256:}"
case "$image_digest" in
  *[!0-9a-fA-F]*) echo "The release image digest is invalid." >&2; exit 2 ;;
esac

temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-center-upgrade.XXXXXX")"
candidate_env="$temporary_dir/.env"
backup_dir="$(mktemp -d "${install_dir}.backup.XXXXXX")"
agent_container=""
agent_changed=no
files_changed=no
center_started=no

restore_files() {
  for relative in setup.sh upgrade.sh compose.yaml release.env .env; do
    if [ -f "$backup_dir/$relative" ]; then
      install -d -m 0755 "$(dirname "$install_dir/$relative")"
      install -m 0644 "$backup_dir/$relative" "$install_dir/$relative"
    elif [ "$relative" != ".env" ]; then
      rm -f "$install_dir/$relative"
    fi
  done
  chmod 0755 "$install_dir/setup.sh"
  if [ -f "$install_dir/upgrade.sh" ]; then chmod 0755 "$install_dir/upgrade.sh"; fi
}

cleanup() {
  status=$?
  if [ -n "$agent_container" ]; then
    docker rm -f "$agent_container" >/dev/null 2>&1 || true
  fi
  if [ "$status" -ne 0 ] && [ "$agent_changed" = yes ] && [ "$center_started" = no ]; then
    echo "Upgrade stopped before Center was started; restoring the previous Agent executable." >&2
    agent_staged="$(mktemp /usr/local/bin/.vastora-rollback.XXXXXX)"
    install -m 0755 /usr/local/bin/vastora.previous "$agent_staged"
    mv "$agent_staged" /usr/local/bin/vastora
    systemctl restart vastora-agent.service || true
  fi
  if [ "$status" -ne 0 ] && [ "$files_changed" = yes ] && [ "$center_started" = no ]; then
    echo "Upgrade stopped before Center was started; restoring the previous managed files." >&2
    restore_files
  fi
  rm -rf "$temporary_dir" "$backup_dir"
  exit "$status"
}
trap cleanup EXIT HUP INT TERM

awk -v image="$new_image" '
  BEGIN { replaced = 0 }
  /^VASTORA_CENTER_IMAGE=/ {
    if (!replaced) print "VASTORA_CENTER_IMAGE=" image
    replaced = 1
    next
  }
  { print }
  END { if (!replaced) print "VASTORA_CENTER_IMAGE=" image }
' "$install_dir/.env" > "$candidate_env"
chmod 0600 "$candidate_env"

echo "Validating the new deployment with the existing configuration..."
docker compose --env-file "$candidate_env" -f "$source_dir/compose.yaml" config --quiet
echo "Downloading the immutable Center image..."
docker compose --env-file "$candidate_env" -f "$source_dir/compose.yaml" pull center deployer

agent_executable="/usr/local/bin/vastora"
agent_unit="/etc/systemd/system/vastora-agent.service"
if [ -f "$agent_executable" ] && [ -f "$agent_unit" ] && grep -Fq 'Description=Vastora Agent' "$agent_unit"; then
  if ! command -v systemctl >/dev/null 2>&1; then
    echo "systemctl is required to update the co-located Vastora Agent." >&2
    exit 1
  fi
  echo "Preparing the matching Agent from the immutable Center image..."
  agent_container="$(docker create "$new_image")"
  docker cp "$agent_container:/usr/local/bin/vastora" "$temporary_dir/vastora-agent"
  docker rm -f "$agent_container" >/dev/null
  agent_container=""
  chmod 0755 "$temporary_dir/vastora-agent"
  if [ "$("$temporary_dir/vastora-agent" version)" != "$new_version" ]; then
    echo "The Center image contains an unexpected Agent version; the upgrade was not started." >&2
    exit 1
  fi
  agent_staged="$(mktemp /usr/local/bin/.vastora-upgrade.XXXXXX)"
  install -m 0755 "$temporary_dir/vastora-agent" "$agent_staged"
  install -m 0755 "$agent_executable" "$agent_executable.previous"
  mv "$agent_staged" "$agent_executable"
  if ! systemctl restart vastora-agent.service || ! systemctl is-active --quiet vastora-agent.service; then
    echo "The updated Agent did not start; restoring the previous executable." >&2
    agent_staged="$(mktemp /usr/local/bin/.vastora-rollback.XXXXXX)"
    install -m 0755 "$agent_executable.previous" "$agent_staged"
    mv "$agent_staged" "$agent_executable"
    systemctl restart vastora-agent.service || true
    exit 1
  fi
  agent_changed=yes
  echo "Co-located Agent updated to $new_version before Center reconciliation."
fi

for relative in setup.sh upgrade.sh compose.yaml release.env .env; do
  if [ -f "$install_dir/$relative" ]; then
    install -d -m 0755 "$(dirname "$backup_dir/$relative")"
    install -m 0644 "$install_dir/$relative" "$backup_dir/$relative"
  fi
done

install -m 0755 "$source_dir/setup.sh" "$install_dir/setup.sh"
install -m 0755 "$source_dir/upgrade.sh" "$install_dir/upgrade.sh"
install -m 0644 "$source_dir/compose.yaml" "$install_dir/compose.yaml"
install -m 0644 "$source_dir/release.env" "$install_dir/release.env"
install -m 0600 "$candidate_env" "$install_dir/.env"
files_changed=yes

bootstrap_port="$(awk -F= '$1 == "VASTORA_CENTER_BOOTSTRAP_PORT" {sub(/^[^=]*=/, ""); print; exit}' "$install_dir/.env")"
if [ -z "$bootstrap_port" ]; then bootstrap_port=8080; fi

echo "Starting the updated Center..."
cd "$install_dir"
center_started=yes
docker compose up -d --remove-orphans deployer center

attempt=0
until curl -fsS "http://127.0.0.1:$bootstrap_port/healthz" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 30 ]; then
    echo "The updated Center did not become healthy." >&2
    echo "The new files were kept because its database migration may already have run; an automatic downgrade would be unsafe." >&2
    echo "Inspect: cd '$install_dir' && docker compose logs center" >&2
    exit 1
  fi
  sleep 2
done

files_changed=no
agent_changed=no
center_started=no
echo "Vastora Center was updated successfully${new_version:+ to $new_version}."
