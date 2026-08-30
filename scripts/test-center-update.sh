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
printf '%s\n' 'VASTORA_VERSION=0.1.0-alpha.57' > "$install_dir/release.env"

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
install_dir=""
expected_version=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --release-url) release_url="$2"; shift 2 ;;
    --install-dir) install_dir="$2"; shift 2 ;;
    --expected-version) expected_version="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done
[ "$release_url" = "https://vastora.petauron.com/releases/v$expected_version/vastora-center-install.tar.gz" ]
[ "$VASTORA_UPDATE_STATUS_FILE" = "$install_dir/.update-status.json" ]
[ "$VASTORA_UPDATE_TARGET_VERSION" = "$expected_version" ]
grep -Fq '"message":"Installing the verified release."' "$VASTORA_UPDATE_STATUS_FILE"
printf '%s\n' "$release_url" > "$FAKE_INSTALLER_LOG"
printf 'VASTORA_VERSION=%s\n' "$expected_version" > "$install_dir/release.env"
EOF
cat > "$fake_bin/curl" <<'EOF'
#!/bin/sh
set -eu
headers=""
output=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --proto) shift 2 ;;
    --tlsv1.2) shift ;;
    --dump-header) headers="$2"; shift 2 ;;
    -o) output="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
[ "$url" = "https://vastora.petauron.com/releases/v$FAKE_TARGET_VERSION/install.sh" ]
cp "$FAKE_INSTALLER_SOURCE" "$output"
digest="$(sha256sum "$output" | awk 'NR == 1 {print $1}')"
{
  printf 'HTTP/2 200\r\n'
  printf 'X-Vastora-Version: %s\r\n' "${FAKE_RESPONSE_VERSION:-$FAKE_TARGET_VERSION}"
  printf 'X-Vastora-SHA256: %s\r\n\r\n' "$digest"
} > "$headers"
EOF
chmod 0755 "$fake_bin/id" "$fake_bin/curl" "$temporary_dir/installer.sh"

run_update() {
  target_version="$1"
  response_version="$2"
  printf '%s\n' "$target_version" > "$install_dir/.update-request"
  FAKE_INSTALLER_LOG="$temporary_dir/installer.log" \
  FAKE_INSTALLER_SOURCE="$temporary_dir/installer.sh" \
  FAKE_RESPONSE_VERSION="$response_version" \
  FAKE_TARGET_VERSION="$target_version" \
  PATH="$fake_bin:$PATH" \
    "$project_dir/deploy/center/update-center.sh" --install-dir "$install_dir"
}

run_update 0.1.0-alpha.58 0.1.0-alpha.58
grep -Fq '"state":"succeeded"' "$install_dir/.update-status.json"
grep -Fq '"targetVersion":"0.1.0-alpha.58"' "$install_dir/.update-status.json"
grep -Fqx 'VASTORA_VERSION=0.1.0-alpha.58' "$install_dir/release.env"
grep -Fqx 'https://vastora.petauron.com/releases/v0.1.0-alpha.58/vastora-center-install.tar.gz' "$temporary_dir/installer.log"
test ! -e "$install_dir/.update-request"

if run_update 0.1.0-alpha.59 0.1.0-alpha.58 >/dev/null 2>&1; then
  echo "Center updater accepted an installer for the wrong version." >&2
  exit 1
fi
grep -Fq '"state":"failed"' "$install_dir/.update-status.json"
grep -Fq 'installer version did not match' "$install_dir/.update-status.json"
test ! -e "$install_dir/.update-request"

echo "Center automatic update tests: OK"
