#!/bin/sh
set -eu

base_url=""
expected_version=""
attempts=1
retry_delay=5

usage() {
  cat <<'EOF'
Usage: verify-installer-release.sh --base-url HTTPS_URL --expected-version VERSION [--attempts COUNT] [--retry-delay SECONDS]

Downloads and verifies one complete R2-backed Vastora installer release.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --base-url) base_url="${2:-}"; shift 2 ;;
    --expected-version) expected_version="${2:-}"; shift 2 ;;
    --attempts) attempts="${2:-}"; shift 2 ;;
    --retry-delay) retry_delay="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

for required in awk curl grep mktemp sha256sum sh tar tr; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done
base_url="${base_url%/}"
if ! printf '%s\n' "$expected_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "The expected installer version is invalid." >&2
  exit 2
fi
case "$base_url" in
  https://vastora.petauron.com|"https://vastora.petauron.com/releases/v$expected_version") ;;
  *) echo "The installer base URL must be an official Vastora release endpoint." >&2; exit 2 ;;
esac
case "$attempts:$retry_delay" in
  *[!0-9:]*|0:*|*:) echo "Attempts and retry delay must be non-negative integers, with at least one attempt." >&2; exit 2 ;;
esac

temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-installer-verify.XXXXXX")"
# shellcheck disable=SC2329 # Invoked by the trap below.
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM

header_value() {
  header_name="$1"
  header_file="$2"
  awk -v expected="$header_name" '
    index($0, ":") > 0 && tolower(substr($0, 1, index($0, ":") - 1)) == expected {
      value = substr($0, index($0, ":") + 1)
      sub(/^[[:space:]]+/, "", value)
      sub(/\r$/, "", value)
      result = value
    }
    END { print result }
  ' "$header_file"
}

download_asset() {
  asset="$1"
  output="$temporary_dir/$asset"
  headers="$temporary_dir/$asset.headers"
  curl --connect-timeout 10 --max-time 30 --proto '=https' --tlsv1.2 -fsS \
    --dump-header "$headers" "$base_url/$asset" -o "$output" || return 1
  [ "$(header_value x-vastora-version "$headers")" = "$expected_version" ] || return 1
  expected_digest="$(header_value x-vastora-sha256 "$headers" | tr '[:upper:]' '[:lower:]')"
  printf '%s\n' "$expected_digest" | grep -Eq '^[0-9a-f]{64}$' || return 1
  [ "$(sha256sum "$output" | awk 'NR == 1 {print tolower($1)}')" = "$expected_digest" ] || return 1
}

verify_once() {
  rm -f "$temporary_dir"/install.sh "$temporary_dir"/vastora-center-install.tar.gz "$temporary_dir"/vastora-center-install.tar.gz.sha256
  download_asset install.sh || return 1
  download_asset vastora-center-install.tar.gz || return 1
  download_asset vastora-center-install.tar.gz.sha256 || return 1
  sh -n "$temporary_dir/install.sh" || return 1
  (cd "$temporary_dir" && sha256sum --check vastora-center-install.tar.gz.sha256) || return 1
  actual_version="$(tar -xOzf "$temporary_dir/vastora-center-install.tar.gz" ./release.env 2>/dev/null | awk -F= '$1 == "VASTORA_VERSION" {print $2; exit}')"
  [ "$actual_version" = "$expected_version" ]
}

attempt=1
while [ "$attempt" -le "$attempts" ]; do
  if verify_once; then
    echo "Verified Vastora $expected_version installer at $base_url."
    exit 0
  fi
  if [ "$attempt" -lt "$attempts" ]; then
    echo "Installer endpoint has not selected $expected_version yet (attempt $attempt/$attempts)." >&2
    sleep "$retry_delay"
  fi
  attempt=$((attempt + 1))
done

echo "Installer endpoint did not provide a complete verified $expected_version release: $base_url" >&2
exit 1
