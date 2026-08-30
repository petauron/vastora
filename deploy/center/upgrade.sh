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
if [ ! -f "$install_dir/.env" ] || [ ! -f "$install_dir/compose.yaml" ] || [ ! -f "$install_dir/release.env" ]; then
  echo "$install_dir is not a complete Center installation." >&2
  exit 1
fi
for required_file in install.sh setup.sh upgrade.sh uninstall.sh install-host-cli.sh install-update-service.sh update-center.sh compare-semver.awk runtime-network.sh compose.yaml release.env; do
  if [ ! -f "$source_dir/$required_file" ]; then
    echo "The upgrade bundle is incomplete: missing $required_file" >&2
    exit 1
  fi
done
. "$source_dir/runtime-network.sh"
for required in awk curl docker grep install ip mktemp mv; do
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
installed_version="$(awk -F= '$1 == "VASTORA_VERSION" {sub(/^[^=]*=/, ""); print; exit}' "$install_dir/release.env")"
for candidate_version in "$new_version" "$installed_version"; do
  if ! printf '%s\n' "$candidate_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
    echo "The upgrade requires valid installed and target Center versions." >&2
    exit 2
  fi
done
if [ "$(awk -f "$source_dir/compare-semver.awk" "$new_version" "$installed_version")" = "-1" ]; then
  echo "Refusing to downgrade Center from $installed_version to $new_version; no files were changed." >&2
  exit 1
fi
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
agent_database_opened=no
agent_stopped=no
files_changed=no
center_started=no
layer4_was_running=no
if [ "$(docker inspect -f '{{.State.Running}}' vastora-gateway-haproxy 2>/dev/null || true)" = "true" ]; then
  layer4_was_running=yes
fi

restore_files() {
  for relative in install.sh setup.sh upgrade.sh uninstall.sh install-host-cli.sh install-update-service.sh update-center.sh compare-semver.awk runtime-network.sh compose.yaml release.env .env; do
    if [ -f "$backup_dir/$relative" ]; then
      install -d -m 0755 "$(dirname "$install_dir/$relative")"
      install -m 0644 "$backup_dir/$relative" "$install_dir/$relative"
    elif [ "$relative" != ".env" ]; then
      rm -f "$install_dir/$relative"
    fi
  done
  chmod 0755 "$install_dir/setup.sh"
  if [ -f "$install_dir/upgrade.sh" ]; then chmod 0755 "$install_dir/upgrade.sh"; fi
  if [ -f "$install_dir/uninstall.sh" ]; then chmod 0755 "$install_dir/uninstall.sh"; fi
  if [ -f "$install_dir/install-host-cli.sh" ]; then chmod 0755 "$install_dir/install-host-cli.sh"; fi
  if [ -f "$install_dir/install-update-service.sh" ]; then chmod 0755 "$install_dir/install-update-service.sh"; fi
  if [ -f "$install_dir/update-center.sh" ]; then chmod 0755 "$install_dir/update-center.sh"; fi
}

cleanup() {
  status=$?
  if [ -n "$agent_container" ]; then
    docker rm -f "$agent_container" >/dev/null 2>&1 || true
  fi
  if [ "$status" -ne 0 ] && [ "$agent_changed" = yes ] && [ "$center_started" = no ]; then
    if [ "$agent_database_opened" = yes ]; then
      echo "Upgrade stopped after the Agent database migration boundary; keeping the schema-compatible Agent executable." >&2
      if ! systemctl restart vastora-agent.service; then
        echo "The compatible Agent could not be restarted; operator recovery is required." >&2
      fi
    else
      echo "Upgrade stopped before the Agent database was opened; restoring the previous Agent executable." >&2
      agent_staged="$(mktemp "$(dirname "$agent_executable")/.vastora-rollback.XXXXXX")"
      install -m 0755 "$agent_executable.previous" "$agent_staged"
      mv "$agent_staged" "$agent_executable"
      if ! systemctl restart vastora-agent.service; then
        echo "The previous Agent could not be restarted; operator recovery is required." >&2
      fi
    fi
    agent_stopped=no
  fi
  if [ "$status" -ne 0 ] && [ "$agent_stopped" = yes ]; then
    systemctl start vastora-agent.service || true
  fi
  if [ "$status" -ne 0 ] && [ "$files_changed" = yes ] && [ "$center_started" = no ]; then
    echo "Upgrade stopped before Center was started; restoring the previous managed files." >&2
    restore_files
  fi
  rm -rf "$temporary_dir" "$backup_dir"
  exit "$status"
}
trap cleanup EXIT HUP INT TERM

host_network_addresses="$(ip -o -4 addr show scope global | awk '$2 !~ /^(docker|br-|veth|cni|flannel|cali|podman|lxcbr|virbr|tailscale|wg|tun|tap)/ {split($4, address, "/"); printf "%s%s=%s", separator, $2, address[1]; separator=","}')"
if [ -z "$host_network_addresses" ]; then
  echo "No physical host IPv4 address was detected; refusing to start Center with incomplete network discovery." >&2
  exit 1
fi

awk -v image="$new_image" -v host_addresses="$host_network_addresses" '
  BEGIN { image_replaced = 0; addresses_replaced = 0 }
  /^VASTORA_CENTER_IMAGE=/ {
    if (!image_replaced) print "VASTORA_CENTER_IMAGE=" image
    image_replaced = 1
    next
  }
  /^VASTORA_HOST_NETWORK_ADDRESSES=/ {
    if (!addresses_replaced) print "VASTORA_HOST_NETWORK_ADDRESSES=" host_addresses
    addresses_replaced = 1
    next
  }
  { print }
  END {
    if (!image_replaced) print "VASTORA_CENTER_IMAGE=" image
    if (!addresses_replaced) print "VASTORA_HOST_NETWORK_ADDRESSES=" host_addresses
  }
' "$install_dir/.env" > "$candidate_env"
chmod 0600 "$candidate_env"
bootstrap_port="$(awk -F= '$1 == "VASTORA_CENTER_BOOTSTRAP_PORT" {sub(/^[^=]*=/, ""); print; exit}' "$candidate_env")"
if [ -z "$bootstrap_port" ]; then bootstrap_port=8080; fi
local_center_url="http://127.0.0.1:$bootstrap_port"

echo "Validating the new deployment with the existing configuration..."
migrate_legacy_vastora_runtime_network "$install_dir"
ensure_vastora_runtime_network_for_upgrade "$install_dir"
docker compose --env-file "$candidate_env" -f "$source_dir/compose.yaml" config --quiet
echo "Downloading the immutable Center image..."
docker compose --env-file "$candidate_env" -f "$source_dir/compose.yaml" pull center deployer

agent_executable="${VASTORA_AGENT_EXECUTABLE:-/usr/local/bin/vastora}"
agent_unit="${VASTORA_AGENT_UNIT:-/etc/systemd/system/vastora-agent.service}"
agent_data_dir="${VASTORA_AGENT_DATA_DIR:-/var/lib/vastora/agent}"
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
	if ! curl -fsS "$local_center_url/healthz" >/dev/null 2>&1; then
	  echo "The host-only Center channel is not healthy; the co-located Agent was not changed." >&2
	  exit 1
	fi
	echo "Moving the co-located Agent to the host-only Center channel..."
	if [ -f "$agent_data_dir/agent.db" ]; then
	  agent_database_backup="$(mktemp "$agent_data_dir/.agent.db.pre-upgrade.$new_version.XXXXXX")"
	  install -m 0600 "$agent_data_dir/agent.db" "$agent_database_backup"
	  echo "Protected the pre-migration Agent database at $agent_database_backup"
	fi
	systemctl stop vastora-agent.service
	agent_stopped=yes
  agent_staged="$(mktemp "$(dirname "$agent_executable")/.vastora-upgrade.XXXXXX")"
  install -m 0755 "$temporary_dir/vastora-agent" "$agent_staged"
  install -m 0755 "$agent_executable" "$agent_executable.previous"
  mv "$agent_staged" "$agent_executable"
	agent_changed=yes
	agent_database_opened=yes
	"$agent_executable" agent configure-center --data-dir "$agent_data_dir" --center-url "$local_center_url"
	if ! systemctl start vastora-agent.service || ! systemctl is-active --quiet vastora-agent.service; then
	  echo "The updated Agent did not start; keeping its schema-compatible executable for recovery." >&2
	  exit 1
	fi
	agent_stopped=no
	echo "Co-located Agent updated to $new_version on the host-only Center channel."
fi

for relative in install.sh setup.sh upgrade.sh uninstall.sh install-host-cli.sh install-update-service.sh update-center.sh compare-semver.awk runtime-network.sh compose.yaml release.env .env; do
  if [ -f "$install_dir/$relative" ]; then
    install -d -m 0755 "$(dirname "$backup_dir/$relative")"
    install -m 0644 "$install_dir/$relative" "$backup_dir/$relative"
  fi
done

install -m 0755 "$source_dir/install.sh" "$install_dir/install.sh"
install -m 0755 "$source_dir/setup.sh" "$install_dir/setup.sh"
install -m 0755 "$source_dir/upgrade.sh" "$install_dir/upgrade.sh"
install -m 0755 "$source_dir/uninstall.sh" "$install_dir/uninstall.sh"
install -m 0755 "$source_dir/install-host-cli.sh" "$install_dir/install-host-cli.sh"
install -m 0755 "$source_dir/install-update-service.sh" "$install_dir/install-update-service.sh"
install -m 0755 "$source_dir/update-center.sh" "$install_dir/update-center.sh"
install -m 0644 "$source_dir/compare-semver.awk" "$install_dir/compare-semver.awk"
install -m 0644 "$source_dir/runtime-network.sh" "$install_dir/runtime-network.sh"
install -m 0644 "$source_dir/compose.yaml" "$install_dir/compose.yaml"
install -m 0644 "$source_dir/release.env" "$install_dir/release.env"
install -m 0600 "$candidate_env" "$install_dir/.env"
files_changed=yes

echo "Starting the updated Center..."
cd "$install_dir"
center_started=yes
docker compose up -d --remove-orphans deployer center
validate_vastora_runtime_network

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

attempt=0
until curl -fsS "http://127.0.0.1:$bootstrap_port/readyz" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 240 ]; then
    echo "The updated Center did not finish startup reconciliation." >&2
    echo "Inspect: cd '$install_dir' && docker compose logs center deployer" >&2
    exit 1
  fi
  sleep 2
done

if [ "$agent_changed" = yes ]; then
	if ! systemctl is-active --quiet vastora-agent.service; then
	  echo "The co-located Agent stopped during Center reconciliation." >&2
	  exit 1
	fi
  attempt=0
  until curl -fsS http://127.0.0.1:8090/healthz >/dev/null 2>&1; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 30 ]; then
      echo "The co-located Agent did not become healthy after Center reconciliation." >&2
      exit 1
    fi
    sleep 1
  done
  if [ "$layer4_was_running" = yes ]; then
    attempt=0
    until [ "$(docker inspect -f '{{.State.Running}}' vastora-gateway-haproxy 2>/dev/null || true)" = "true" ]; do
      attempt=$((attempt + 1))
      if [ "$attempt" -ge 30 ]; then
        echo "The co-located Agent did not restore the shared HTTPS gateway." >&2
        exit 1
      fi
      sleep 1
    done
  fi
	echo "Co-located Agent remained connected through Center reconciliation."
else
  "$install_dir/install-host-cli.sh" \
    --image "$new_image" --version "$new_version" --install-dir "$install_dir"
fi

install -m 0644 /dev/null "$install_dir/.host-cli-installed"

files_changed=no
agent_changed=no
center_started=no
"$install_dir/install-update-service.sh" --install-dir "$install_dir"
echo "Vastora Center was updated successfully${new_version:+ to $new_version}."
