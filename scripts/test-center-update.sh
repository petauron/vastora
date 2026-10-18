#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-center-update-test.XXXXXX")"
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM

fake_bin="$temporary_dir/bin"
install_dir="$temporary_dir/center"
mkdir -p "$fake_bin" "$install_dir"
image_57="ghcr.io/petauron/vastora-center@sha256:5757575757575757575757575757575757575757575757575757575757575757"
image_58="ghcr.io/petauron/vastora-center@sha256:5858585858585858585858585858585858585858585858585858585858585858"
image_59="ghcr.io/petauron/vastora-center@sha256:5959595959595959595959595959595959595959595959595959595959595959"
image_60="ghcr.io/petauron/vastora-center@sha256:6060606060606060606060606060606060606060606060606060606060606060"
printf '%s\n' 'VASTORA_VERSION=0.1.0-alpha.57' "VASTORA_CENTER_IMAGE=$image_57" > "$install_dir/release.env"
printf '%s\n' 'VASTORA_CENTER_BOOTSTRAP_PORT=8080' > "$install_dir/.env"

cat > "$fake_bin/id" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "-u" ]; then printf '%s\n' 0; else exec /usr/bin/id "$@"; fi
EOF
cat > "$temporary_dir/installer.sh" <<'EOF'
#!/bin/sh
set -eu
[ "${1:-}" = "center" ]
shift
release_url=""
release_resolve=""
install_dir=""
expected_version=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --release-url) release_url="$2"; shift 2 ;;
    --release-resolve) release_resolve="$2"; shift 2 ;;
    --install-dir) install_dir="$2"; shift 2 ;;
    --expected-version) expected_version="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done
[ "$release_url" = "https://vastora.petauron.com/releases/v$expected_version/vastora-center-install.tar.gz" ]
[ "$release_resolve" = "vastora.petauron.com:443:203.0.113.10" ]
[ "$VASTORA_UPDATE_STATUS_FILE" = "$install_dir/.update-status.json" ]
[ "$VASTORA_UPDATE_TARGET_VERSION" = "$expected_version" ]
grep -Fq '"message":"Installing the verified release."' "$VASTORA_UPDATE_STATUS_FILE"
printf '%s\n' "$release_url" > "$FAKE_INSTALLER_LOG"
printf 'VASTORA_VERSION=%s\nVASTORA_CENTER_IMAGE=%s\n' "$expected_version" "$FAKE_TARGET_IMAGE" > "$install_dir/release.env"
EOF
cat > "$fake_bin/curl" <<'EOF'
#!/bin/sh
set -eu
headers=""
output=""
resolve=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --noproxy|--connect-timeout|--max-time) shift 2 ;;
    --proto) shift 2 ;;
    --tlsv1.2) shift ;;
    --dump-header) headers="$2"; shift 2 ;;
    --write-out) shift 2 ;;
    --resolve) resolve="$2"; shift 2 ;;
    -o) output="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  http://127.0.0.1:*/healthz|http://127.0.0.1:*/readyz)
    [ "${FAKE_UNHEALTHY:-0}" = 0 ]
    case "$url" in */readyz) [ "${FAKE_NOT_READY:-0}" = 0 ] ;; esac
    exit
    ;;
esac
[ "$resolve" = "vastora.petauron.com:443:203.0.113.10" ]
[ "$url" = "https://vastora.petauron.com/releases/v$FAKE_TARGET_VERSION/install.sh" ]
status="${FAKE_RESPONSE_STATUS:-200}"
if [ "$status" != "200" ]; then
  printf '%s' "$status"
  exit 22
fi
cp "$FAKE_INSTALLER_SOURCE" "$output"
digest="$(sha256sum "$output" | awk 'NR == 1 {print $1}')"
{
  printf 'HTTP/2 200\r\n'
  printf 'X-Vastora-Version: %s\r\n' "${FAKE_RESPONSE_VERSION:-$FAKE_TARGET_VERSION}"
  printf 'X-Vastora-Center-Image: %s\r\n' "$FAKE_TARGET_IMAGE"
  printf 'X-Vastora-SHA256: %s\r\n\r\n' "$digest"
} > "$headers"
printf '%s' "$status"
EOF
cat > "$fake_bin/docker" <<'EOF'
#!/bin/sh
set -eu
case "$1" in
  compose) printf '%s\n' test-center ;;
  inspect)
    case "$4" in
      vastora-gateway-*)
        if [ "${FAKE_GATEWAY_STOPPED:-0}" = 1 ]; then printf '%s\n' false; exit; fi
        exit 1
        ;;
    esac
    case "$3" in
      *State.Running*) printf '%s\n' true ;;
      *Config.Image*) printf '%s\n' "${FAKE_RUNNING_IMAGE:-$FAKE_TARGET_IMAGE}" ;;
      *) exit 2 ;;
    esac
    ;;
  exec) printf '%s\n' "${FAKE_RUNNING_VERSION:-$FAKE_TARGET_VERSION}" ;;
  *) exit 2 ;;
esac
EOF
cat > "$fake_bin/systemctl" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod 0755 "$fake_bin/id" "$fake_bin/curl" "$fake_bin/docker" "$fake_bin/systemctl" "$temporary_dir/installer.sh"

run_update() {
  target_version="$1"
  response_version="$2"
  target_image="$3"
  printf '%s\n' \
    "$target_version" \
    "https://vastora.petauron.com/releases" \
    "vastora.petauron.com" \
    "443" \
    "203.0.113.10" \
    "$target_image" > "$install_dir/.update-request"
  FAKE_INSTALLER_LOG="$temporary_dir/installer.log" \
  FAKE_INSTALLER_SOURCE="$temporary_dir/installer.sh" \
  FAKE_RESPONSE_VERSION="$response_version" \
  FAKE_TARGET_VERSION="$target_version" \
  FAKE_TARGET_IMAGE="$target_image" \
  VASTORA_AGENT_UNIT="$temporary_dir/agent.service" \
  PATH="$fake_bin:$PATH" \
    "$project_dir/deploy/center/update-center.sh" --install-dir "$install_dir"
}

run_update 0.1.0-alpha.58 0.1.0-alpha.58 "$image_58"
grep -Fq '"state":"succeeded"' "$install_dir/.update-status.json"
grep -Fq '"targetVersion":"0.1.0-alpha.58"' "$install_dir/.update-status.json"
grep -Fqx 'VASTORA_VERSION=0.1.0-alpha.58' "$install_dir/release.env"
grep -Fqx "VASTORA_CENTER_IMAGE=$image_58" "$install_dir/release.env"
grep -Fqx 'https://vastora.petauron.com/releases/v0.1.0-alpha.58/vastora-center-install.tar.gz' "$temporary_dir/installer.log"
test ! -e "$install_dir/.update-request"

# Files matching a target are not proof that the runtime is healthy.
for failure in unhealthy not-ready wrong-version wrong-image stopped-agent stopped-gateway; do
  case "$failure" in
    unhealthy) export FAKE_UNHEALTHY=1 ;;
    not-ready) export FAKE_NOT_READY=1 ;;
    wrong-version) export FAKE_RUNNING_VERSION=0.1.0-alpha.57 ;;
    wrong-image) export FAKE_RUNNING_IMAGE="$image_57" ;;
    stopped-agent) printf '%s\n' 'Description=Vastora Agent' > "$temporary_dir/agent.service" ;;
    stopped-gateway) export FAKE_GATEWAY_STOPPED=1 ;;
  esac
  if run_update 0.1.0-alpha.58 0.1.0-alpha.58 "$image_58" >/dev/null 2>&1; then
    echo "Same-release verification accepted $failure." >&2
    exit 1
  fi
  grep -Fq '"state":"failed"' "$install_dir/.update-status.json"
  unset FAKE_UNHEALTHY FAKE_NOT_READY FAKE_RUNNING_VERSION FAKE_RUNNING_IMAGE FAKE_GATEWAY_STOPPED
  rm -f "$temporary_dir/agent.service"
done
printf '%s\n' untouched > "$temporary_dir/installer.log"
run_update 0.1.0-alpha.58 0.1.0-alpha.58 "$image_58"
grep -Fq '"state":"succeeded"' "$install_dir/.update-status.json"
grep -Fqx untouched "$temporary_dir/installer.log"

if run_update 0.1.0-alpha.59 0.1.0-alpha.58 "$image_59" >/dev/null 2>&1; then
  echo "Center updater accepted an installer for the wrong version." >&2
  exit 1
fi
grep -Fq '"state":"failed"' "$install_dir/.update-status.json"
grep -Fq 'installer version did not match' "$install_dir/.update-status.json"
test ! -e "$install_dir/.update-request"

if FAKE_RESPONSE_STATUS=404 run_update 0.1.0-alpha.60 0.1.0-alpha.60 "$image_60" >/dev/null 2>&1; then
  echo "Center updater accepted a pruned immutable release." >&2
  exit 1
fi
grep -Fq 'release is no longer active' "$install_dir/.update-status.json"
grep -Fq 'Refresh Settings' "$install_dir/.update-status.json"
test ! -e "$install_dir/.update-request"

echo "Center automatic update tests: OK"
