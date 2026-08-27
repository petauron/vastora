#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-center-uninstall-test.XXXXXX")"
temporary_dir="$(CDPATH='' cd -- "$temporary_dir" && pwd -P)"
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM

fake_bin="$temporary_dir/fake-bin"
systemd_dir="$temporary_dir/systemd"
docker_log="$temporary_dir/docker.log"
systemctl_log="$temporary_dir/systemctl.log"
install -d "$fake_bin" "$systemd_dir"

digest="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
"$project_dir/scripts/package-center-install.sh" \
  --version 0.1.0-test \
  --image "ghcr.io/petauron/vastora-center@sha256:$digest" \
  --output-dir "$temporary_dir/release" >/dev/null
archive="$temporary_dir/release/vastora-center-install.tar.gz"
checksum="$archive.sha256"

if "$project_dir/install.sh" center uninstall --purge >"$temporary_dir/unsupported.out" 2>&1; then
  echo "Legacy uninstall flags were accepted instead of using the TUI" >&2
  exit 1
fi
grep -Fq 'the terminal menu provides every option' "$temporary_dir/unsupported.out"

cat > "$fake_bin/id" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "-u" ]; then echo 0; else exec /usr/bin/id "$@"; fi
EOF
cat > "$fake_bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_SYSTEMCTL_LOG"
exit 0
EOF
cat > "$fake_bin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
  -s) echo Linux ;;
  -m) echo x86_64 ;;
  *) echo Linux ;;
esac
EOF
cat > "$fake_bin/curl" <<'EOF'
#!/bin/sh
output=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
case "$url" in
  *.sha256) cp "$FAKE_RELEASE_CHECKSUM" "$output" ;;
  *) cp "$FAKE_RELEASE_ARCHIVE" "$output" ;;
esac
EOF
cat > "$fake_bin/docker" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_DOCKER_LOG"
case "${1:-}" in
  info) exit 0 ;;
  compose)
    case "$*" in
      *'center capabilities')
        if [ "${FAKE_DOCKER_CAPABILITIES:-current}" = old ]; then exit 1; fi
        printf '%s\n' 'decommission-applications' 'agent-host-decommission'
        exit 0
        ;;
      *'center offline-agent-cleanups'*)
        if [ -n "${FAKE_OFFLINE_AGENT_REPORT:-}" ]; then
          printf '%s\n' "$FAKE_OFFLINE_AGENT_REPORT"
        fi
        exit 0
        ;;
      *'center decommission-applications'*)
        if [ "${FAKE_DOCKER_MODE:-}" = cleanup-fail ]; then exit 1; fi
        exit 0
        ;;
    esac
    exit 0
    ;;
  volume)
    case "${2:-}" in
      inspect)
        case "$*" in
          *vastora_deployer-socket*) exit 1 ;;
        esac
        exit 1
        ;;
      rm) exit 0 ;;
    esac
    ;;
  container) exit 1 ;;
  ps|inspect) exit 0 ;;
  rm) exit 0 ;;
esac
exit 0
EOF
chmod 0755 "$fake_bin/id" "$fake_bin/systemctl" "$fake_bin/uname" "$fake_bin/curl" "$fake_bin/docker"

create_install() {
  target="$1"
  install -d "$target"
  printf '%s\n' 'VASTORA_CENTER_IMAGE=test' > "$target/.env"
  install -m 0644 "$project_dir/deploy/center/compose.yaml" "$target/compose.yaml"
  cat > "$systemd_dir/vastora-center-update.service" <<EOF
[Unit]
Description=Vastora Center verified update
[Service]
ExecStart=$target/update-center.sh --install-dir $target
EOF
  cat > "$systemd_dir/vastora-center-update.path" <<EOF
[Unit]
Description=Watch for a Vastora Center update request
[Path]
PathExists=$target/.update-request
EOF
}

default_install="$temporary_dir/default-center"
create_install "$default_install"
: > "$temporary_dir/default.input"
printf '1\ny\n' > "$temporary_dir/default.input"
: > "$docker_log"
: > "$systemctl_log"
VASTORA_SYSTEMD_UNIT_DIR="$systemd_dir" \
VASTORA_UNINSTALL_INPUT="$temporary_dir/default.input" \
VASTORA_UNINSTALL_OUTPUT="$temporary_dir/default.output" \
FAKE_RELEASE_ARCHIVE="$archive" \
FAKE_RELEASE_CHECKSUM="$checksum" \
FAKE_DOCKER_LOG="$docker_log" \
FAKE_SYSTEMCTL_LOG="$systemctl_log" \
PATH="$fake_bin:$PATH" \
  "$project_dir/install.sh" center uninstall \
    --release-url https://releases.example.test/vastora-center-install.tar.gz \
    --install-dir "$default_install" >/dev/null
test ! -e "$default_install"
test ! -e "$systemd_dir/vastora-center-update.service"
test ! -e "$systemd_dir/vastora-center-update.path"
grep -Fqx 'compose down' "$docker_log"
if grep -Fq 'volume rm vastora_center-data' "$docker_log"; then
  echo "Default uninstall deleted the Center database" >&2
  exit 1
fi
grep -Fqx 'stop vastora-center-update.path' "$systemctl_log"
grep -Fqx 'disable vastora-center-update.path' "$systemctl_log"

cancelled_install="$temporary_dir/cancelled-center"
create_install "$cancelled_install"
printf '4\n' > "$temporary_dir/cancelled.input"
: > "$docker_log"
: > "$systemctl_log"
VASTORA_SYSTEMD_UNIT_DIR="$systemd_dir" \
  VASTORA_UNINSTALL_INPUT="$temporary_dir/cancelled.input" \
  VASTORA_UNINSTALL_OUTPUT="$temporary_dir/cancelled.output" \
  FAKE_DOCKER_LOG="$docker_log" \
  FAKE_SYSTEMCTL_LOG="$systemctl_log" \
  PATH="$fake_bin:$PATH" \
    "$project_dir/deploy/center/uninstall.sh" --install-dir "$cancelled_install" >/dev/null
test -d "$cancelled_install"
test -e "$systemd_dir/vastora-center-update.service"
test ! -s "$systemctl_log"
if grep -Fq 'compose down' "$docker_log"; then
  echo "Cancelled uninstall mutated the Center runtime" >&2
  exit 1
fi

rm -rf "$cancelled_install"
rm -f "$systemd_dir/vastora-center-update.service" "$systemd_dir/vastora-center-update.path"
failed_cleanup_install="$temporary_dir/failed-cleanup-center"
create_install "$failed_cleanup_install"
printf '2\ny\n' > "$temporary_dir/failed-cleanup.input"
: > "$docker_log"
: > "$systemctl_log"
if VASTORA_SYSTEMD_UNIT_DIR="$systemd_dir" \
  VASTORA_UNINSTALL_INPUT="$temporary_dir/failed-cleanup.input" \
  VASTORA_UNINSTALL_OUTPUT="$temporary_dir/failed-cleanup.output" \
  FAKE_DOCKER_MODE=cleanup-fail \
  FAKE_DOCKER_LOG="$docker_log" \
  FAKE_SYSTEMCTL_LOG="$systemctl_log" \
  PATH="$fake_bin:$PATH" \
    "$project_dir/deploy/center/uninstall.sh" --install-dir "$failed_cleanup_install" >/dev/null 2>&1; then
  echo "Center uninstall continued after application cleanup failed" >&2
  exit 1
fi
test -d "$failed_cleanup_install"
test -e "$systemd_dir/vastora-center-update.path"
grep -Fqx 'start vastora-center-update.path' "$systemctl_log"
if grep -Fq 'compose down' "$docker_log"; then
  echo "Failed application cleanup stopped Center" >&2
  exit 1
fi

rm -rf "$failed_cleanup_install"
rm -f "$systemd_dir/vastora-center-update.service" "$systemd_dir/vastora-center-update.path"
keep_data_install="$temporary_dir/keep-data-center"
create_install "$keep_data_install"
printf '2\ny\n' > "$temporary_dir/keep-data.input"
: > "$docker_log"
: > "$systemctl_log"
VASTORA_SYSTEMD_UNIT_DIR="$systemd_dir" \
VASTORA_UNINSTALL_INPUT="$temporary_dir/keep-data.input" \
VASTORA_UNINSTALL_OUTPUT="$temporary_dir/keep-data.output" \
FAKE_DOCKER_LOG="$docker_log" \
FAKE_SYSTEMCTL_LOG="$systemctl_log" \
PATH="$fake_bin:$PATH" \
  "$project_dir/deploy/center/uninstall.sh" --install-dir "$keep_data_install" >/dev/null
test ! -e "$keep_data_install"
grep -Fqx 'compose exec -T center /usr/local/bin/vastora center decommission-applications --data-dir /var/lib/vastora' "$docker_log"
if grep -Fq -- '--delete-data' "$docker_log"; then
  echo "Keep-data selection requested application data deletion" >&2
  exit 1
fi

delete_data_install="$temporary_dir/delete-data-center"
create_install "$delete_data_install"
printf '3\nDELETE\n' > "$temporary_dir/delete-data.input"
: > "$docker_log"
: > "$systemctl_log"
VASTORA_SYSTEMD_UNIT_DIR="$systemd_dir" \
VASTORA_UNINSTALL_INPUT="$temporary_dir/delete-data.input" \
VASTORA_UNINSTALL_OUTPUT="$temporary_dir/delete-data.output" \
FAKE_DOCKER_LOG="$docker_log" \
FAKE_SYSTEMCTL_LOG="$systemctl_log" \
PATH="$fake_bin:$PATH" \
  "$project_dir/deploy/center/uninstall.sh" --install-dir "$delete_data_install" >/dev/null
test ! -e "$delete_data_install"
grep -Fqx 'compose exec -T center /usr/local/bin/vastora center decommission-applications --data-dir /var/lib/vastora --delete-data' "$docker_log"

offline_install="$temporary_dir/offline-center"
create_install "$offline_install"
printf '2\ny\nFORCE\n' > "$temporary_dir/offline.input"
: > "$docker_log"
: > "$systemctl_log"
VASTORA_SYSTEMD_UNIT_DIR="$systemd_dir" \
VASTORA_UNINSTALL_INPUT="$temporary_dir/offline.input" \
VASTORA_UNINSTALL_OUTPUT="$temporary_dir/offline.output" \
FAKE_OFFLINE_AGENT_REPORT='Wuhan node (node-offline)\n  sudo vastora agent uninstall --purge' \
FAKE_DOCKER_LOG="$docker_log" \
FAKE_SYSTEMCTL_LOG="$systemctl_log" \
PATH="$fake_bin:$PATH" \
  "$project_dir/deploy/center/uninstall.sh" --install-dir "$offline_install" >/dev/null
test ! -e "$offline_install"
grep -Fq 'sudo vastora agent uninstall --purge' "$temporary_dir/offline.output"
grep -Fqx 'compose exec -T center /usr/local/bin/vastora center decommission-applications --data-dir /var/lib/vastora --force-offline' "$docker_log"

legacy_bundle="$temporary_dir/legacy-bundle"
install -d "$legacy_bundle"
install -m 0755 "$project_dir/deploy/center/uninstall.sh" "$legacy_bundle/uninstall.sh"
cat > "$legacy_bundle/upgrade.sh" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_UPGRADE_LOG"
EOF
chmod 0755 "$legacy_bundle/upgrade.sh"
legacy_install="$temporary_dir/legacy-center"
create_install "$legacy_install"
printf '2\ny\n' > "$temporary_dir/legacy.input"
: > "$docker_log"
: > "$systemctl_log"
: > "$temporary_dir/upgrade.log"
VASTORA_SYSTEMD_UNIT_DIR="$systemd_dir" \
VASTORA_UNINSTALL_INPUT="$temporary_dir/legacy.input" \
VASTORA_UNINSTALL_OUTPUT="$temporary_dir/legacy.output" \
FAKE_DOCKER_CAPABILITIES=old \
FAKE_UPGRADE_LOG="$temporary_dir/upgrade.log" \
FAKE_DOCKER_LOG="$docker_log" \
FAKE_SYSTEMCTL_LOG="$systemctl_log" \
PATH="$fake_bin:$PATH" \
  "$legacy_bundle/uninstall.sh" --install-dir "$legacy_install" >/dev/null
test ! -e "$legacy_install"
grep -Fqx -- "--install-dir $legacy_install" "$temporary_dir/upgrade.log"
grep -Fqx 'compose exec -T center /usr/local/bin/vastora center decommission-applications --data-dir /var/lib/vastora' "$docker_log"

cli_install="$temporary_dir/cli-center"
create_install "$cli_install"
install -m 0644 /dev/null "$cli_install/.host-cli-installed"
install -d "$temporary_dir/host-bin"
cat > "$temporary_dir/host-bin/vastora" <<'EOF'
#!/bin/sh
if [ "${1:-}" = help ]; then printf '%s\n' 'Vastora control-plane tools'; fi
EOF
chmod 0755 "$temporary_dir/host-bin/vastora"
printf '1\ny\n' > "$temporary_dir/cli.input"
: > "$docker_log"
: > "$systemctl_log"
VASTORA_SYSTEMD_UNIT_DIR="$systemd_dir" \
VASTORA_HOST_CLI_PATH="$temporary_dir/host-bin/vastora" \
VASTORA_UNINSTALL_INPUT="$temporary_dir/cli.input" \
VASTORA_UNINSTALL_OUTPUT="$temporary_dir/cli.output" \
FAKE_DOCKER_LOG="$docker_log" \
FAKE_SYSTEMCTL_LOG="$systemctl_log" \
PATH="$fake_bin:$PATH" \
  "$project_dir/deploy/center/uninstall.sh" --install-dir "$cli_install" >/dev/null
test ! -e "$temporary_dir/host-bin/vastora"

unsafe_install="$temporary_dir/unsafe-center"
install -d "$unsafe_install"
printf '%s\n' 'unrelated=true' > "$unsafe_install/.env"
printf '%s\n' 'name: unrelated' > "$unsafe_install/compose.yaml"
if VASTORA_SYSTEMD_UNIT_DIR="$systemd_dir" \
  FAKE_DOCKER_LOG="$docker_log" \
  FAKE_SYSTEMCTL_LOG="$systemctl_log" \
  PATH="$fake_bin:$PATH" \
    "$project_dir/deploy/center/uninstall.sh" --install-dir "$unsafe_install" >/dev/null 2>&1; then
  echo "Uninstaller accepted an unrelated directory" >&2
  exit 1
fi
test -d "$unsafe_install"

echo "Center uninstall test passed"
