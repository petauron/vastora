#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-center-install-test.XXXXXX")"
temporary_dir="$(CDPATH='' cd -- "$temporary_dir" && pwd -P)"
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM

digest="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
image="ghcr.io/petauron/vastora-center@sha256:$digest"
"$project_dir/scripts/package-center-install.sh" \
  --version 0.1.0-test \
  --image "$image" \
  --output-dir "$temporary_dir/output" >/dev/null

archive="$temporary_dir/output/vastora-center-install.tar.gz"
test -f "$archive"
test -f "$archive.sha256"
test "$(tar -xOzf "$archive" ./release.env | awk -F= '$1 == "VASTORA_VERSION" {print $2; exit}')" = "0.1.0-test"
expected_digest="$(awk 'NR == 1 {print $1}' "$archive.sha256")"
test "$(awk 'NR == 1 {print $2}' "$archive.sha256")" = "vastora-center-install.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
  actual_digest="$(sha256sum "$archive" | awk 'NR == 1 {print $1}')"
else
  actual_digest="$(shasum -a 256 "$archive" | awk 'NR == 1 {print $1}')"
fi
test "$expected_digest" = "$actual_digest"

validation_bin="$temporary_dir/validation-bin"
mkdir -p "$validation_bin"
cat > "$validation_bin/id" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "-u" ]; then printf '%s\n' 0; else exec /usr/bin/id "$@"; fi
EOF
cat > "$validation_bin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
  -s) printf '%s\n' Linux ;;
  -m) printf '%s\n' x86_64 ;;
  *) printf '%s\n' Linux ;;
esac
EOF
cat > "$validation_bin/curl" <<'EOF'
#!/bin/sh
set -eu
output=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --proto|--proto-redir) shift 2 ;;
    --tlsv1.2|-fsSL) shift ;;
    -o) output="$2"; shift 2 ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  *.sha256) cp "$FAKE_RELEASE_CHECKSUM" "$output" ;;
  *) cp "$FAKE_RELEASE_ARCHIVE" "$output" ;;
esac
EOF
chmod 0755 "$validation_bin/id" "$validation_bin/uname" "$validation_bin/curl"
if FAKE_RELEASE_ARCHIVE="$archive" \
   FAKE_RELEASE_CHECKSUM="$archive.sha256" \
   PATH="$validation_bin:$PATH" \
   "$project_dir/install.sh" center \
     --release-url https://vastora.petauron.com/releases/v0.1.0-test/vastora-center-install.tar.gz \
     --install-dir "$temporary_dir/version-mismatch" \
     --expected-version 0.1.0-other >/dev/null 2>&1; then
  echo "Center installer accepted a bundle for a different requested version." >&2
  exit 1
fi
test ! -e "$temporary_dir/version-mismatch"

tar -xzf "$archive" -C "$temporary_dir"
grep -Fqx 'VASTORA_VERSION=0.1.0-test' "$temporary_dir/release.env"
grep -Fqx "VASTORA_CENTER_IMAGE=$image" "$temporary_dir/release.env"
test -x "$temporary_dir/setup.sh"
test -x "$temporary_dir/install.sh"
test -x "$temporary_dir/upgrade.sh"
test -x "$temporary_dir/uninstall.sh"
test -x "$temporary_dir/install-host-cli.sh"
test -x "$temporary_dir/install-update-service.sh"
test -x "$temporary_dir/update-center.sh"
test -f "$temporary_dir/runtime-network.sh"
test -f "$temporary_dir/compose.yaml"
grep -Fq 'docker cp "$agent_container:/usr/local/bin/vastora"' "$temporary_dir/upgrade.sh"
grep -Fq 'agent configure-center --data-dir "$agent_data_dir" --center-url "$local_center_url"' "$temporary_dir/upgrade.sh"
grep -Fq 'Co-located Agent updated to $new_version on the host-only Center channel.' "$temporary_dir/upgrade.sh"
grep -Fq 'http://127.0.0.1:$bootstrap_port/readyz' "$temporary_dir/upgrade.sh"
grep -Fq 'Co-located Agent remained connected through Center reconciliation.' "$temporary_dir/upgrade.sh"
test ! -e "$temporary_dir/headscale"
if grep -Eq 'headscale/(config\.yaml|policy\.hujson)' "$project_dir/install.sh"; then
  echo "Public installer still requires removed Headscale configuration files" >&2
  exit 1
fi
grep -Fq '127.0.0.1:${VASTORA_CENTER_BOOTSTRAP_PORT:-8080}' "$temporary_dir/compose.yaml"
grep -Fq 'name: vastora-runtime' "$temporary_dir/compose.yaml"
grep -Fq 'external: true' "$temporary_dir/compose.yaml"
grep -Fq 'ensure_vastora_runtime_network' "$temporary_dir/setup.sh"
if grep -Fq 'network_mode: host' "$temporary_dir/compose.yaml"; then
  echo "Center install bundle still uses the host network" >&2
  exit 1
fi
grep -Fq '    ports:' "$temporary_dir/compose.yaml"
if sed -n '/^  center:/,/^  deployer:/p' "$temporary_dir/compose.yaml" | grep -Fq '/var/run/docker.sock'; then
  echo "Center service must not mount the Docker socket" >&2
  exit 1
fi
grep -Fq -- '--deployer-socket' "$temporary_dir/compose.yaml"
grep -Fq '/var/run/docker.sock:/var/run/docker.sock' "$temporary_dir/compose.yaml"
grep -Fq '/run/vastora:/run/vastora' "$temporary_dir/compose.yaml"
if grep -Fq '${VASTORA_CENTER_PORT:-443}:8080' "$temporary_dir/compose.yaml"; then
  echo "Center install bundle still claims public port 443" >&2
  exit 1
fi
grep -Fq 'ssh -N -L 18082:127.0.0.1:$bootstrap_port' "$temporary_dir/setup.sh"
grep -Fq 'Public port 443: unchanged' "$temporary_dir/setup.sh"
grep -Fq 'aarch64|arm64)' "$project_dir/install.sh"

fake_bin="$temporary_dir/fake-bin"
existing="$temporary_dir/existing"
install -d "$fake_bin" "$existing"
install -m 0644 "$temporary_dir/compose.yaml" "$existing/compose.yaml"
printf '%s\n' 'old setup' > "$existing/setup.sh"
printf '%s\n' 'VASTORA_VERSION=old' 'VASTORA_CENTER_IMAGE=old-image' > "$existing/release.env"
printf '%s\n' 'VASTORA_CENTER_IMAGE=old-image' 'VASTORA_CENTER_BOOTSTRAP_PORT=19090' 'VASTORA_CUSTOM_VALUE=preserved' > "$existing/.env"
cat > "$fake_bin/docker" <<'EOF'
#!/bin/sh
case "${1:-}" in
  create) printf '%s\n' 'vastora-test-container'; exit 0 ;;
  cp) cp "$FAKE_VASTORA_BINARY" "$3"; exit 0 ;;
esac
exit 0
EOF
cat > "$fake_bin/curl" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$fake_bin/systemctl" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$fake_bin/id" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "-u" ]; then printf '%s\n' 0; else exec /usr/bin/id "$@"; fi
EOF
cat > "$fake_bin/ip" <<'EOF'
#!/bin/sh
printf '%s\n' '2: enp0s6    inet 10.0.0.157/24 brd 10.0.0.255 scope global enp0s6'
EOF
chmod 0755 "$fake_bin/docker" "$fake_bin/curl" "$fake_bin/systemctl" "$fake_bin/id" "$fake_bin/ip"
cat > "$temporary_dir/fake-vastora" <<'EOF'
#!/bin/sh
case "${1:-}" in
  version) printf '%s\n' '0.1.0-test' ;;
  help) printf '%s\n' 'Vastora control-plane tools' ;;
  agent)
    shift
    if [ "${1:-}" = "configure-center" ]; then
      printf '%s\n' "$*" >"$FAKE_AGENT_CONFIGURE_LOG"
      exit 0
    fi
    exit 1
    ;;
  *) exit 1 ;;
esac
EOF
chmod 0755 "$temporary_dir/fake-vastora"
agent_executable="$temporary_dir/installed-vastora"
agent_unit="$temporary_dir/vastora-agent.service"
agent_data_dir="$temporary_dir/agent-data"
install -m 0755 "$temporary_dir/fake-vastora" "$agent_executable"
printf '%s\n' 'Description=Vastora Agent' >"$agent_unit"
mkdir -p "$agent_data_dir"
VASTORA_SYSTEMD_UNIT_DIR="$temporary_dir/systemd" \
VASTORA_HOST_CLI_PATH="$temporary_dir/host-bin/vastora" \
VASTORA_AGENT_EXECUTABLE="$agent_executable" \
VASTORA_AGENT_UNIT="$agent_unit" \
VASTORA_AGENT_DATA_DIR="$agent_data_dir" \
FAKE_AGENT_CONFIGURE_LOG="$temporary_dir/agent-configure.log" \
FAKE_VASTORA_BINARY="$temporary_dir/fake-vastora" \
PATH="$fake_bin:$PATH" \
  "$temporary_dir/upgrade.sh" --install-dir "$existing" >/dev/null
grep -Fqx "VASTORA_CENTER_IMAGE=$image" "$existing/.env"
grep -Fqx 'VASTORA_CENTER_BOOTSTRAP_PORT=19090' "$existing/.env"
grep -Fqx 'VASTORA_CUSTOM_VALUE=preserved' "$existing/.env"
grep -Fqx 'VASTORA_HOST_NETWORK_ADDRESSES=enp0s6=10.0.0.157' "$existing/.env"
grep -Fqx 'VASTORA_VERSION=0.1.0-test' "$existing/release.env"
test -x "$existing/upgrade.sh"
test -x "$existing/uninstall.sh"
test -x "$existing/install-host-cli.sh"
test -x "$existing/update-center.sh"
test -x "$agent_executable"
test -f "$existing/.host-cli-installed"
test -f "$temporary_dir/systemd/vastora-center-update.service"
test -f "$temporary_dir/systemd/vastora-center-update.path"
grep -Fqx "configure-center --data-dir $agent_data_dir --center-url http://127.0.0.1:19090" "$temporary_dir/agent-configure.log"
grep -Fq "PathExists=$existing/.update-request" "$temporary_dir/systemd/vastora-center-update.path"
grep -Fq "$existing/update-center.sh --install-dir $existing" "$temporary_dir/systemd/vastora-center-update.service"

if "$project_dir/scripts/package-center-install.sh" \
  --version 0.1.0-test \
  --image 'ghcr.io/petauron/vastora-center@sha256:not-a-digest' \
  --output-dir "$temporary_dir/invalid" >/dev/null 2>&1; then
  echo "Invalid image digest was accepted" >&2
  exit 1
fi

echo "Center install bundle test passed"
