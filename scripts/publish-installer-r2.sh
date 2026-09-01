#!/bin/sh
set -eu

command_name="${1:-}"
if [ "$#" -gt 0 ]; then shift; fi
version=""
bucket=""
endpoint=""
source_dir="dist"

usage() {
  cat <<'EOF'
Usage: publish-installer-r2.sh stage --version VERSION --bucket BUCKET --endpoint HTTPS_URL [--source-dir DIR]
       publish-installer-r2.sh activate --version VERSION --bucket BUCKET --endpoint HTTPS_URL

Stages immutable Center installer assets in R2, or atomically activates an
already staged version by replacing the current release manifest.
EOF
}

if [ "$command_name" = "-h" ] || [ "$command_name" = "--help" ]; then
  usage
  exit 0
fi

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) version="${2:-}"; shift 2 ;;
    --bucket) bucket="${2:-}"; shift 2 ;;
    --endpoint) endpoint="${2:-}"; shift 2 ;;
    --source-dir) source_dir="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$command_name" in
  stage|activate) ;;
  *) usage >&2; exit 2 ;;
esac
if [ -z "$version" ] || [ -z "$bucket" ] || [ -z "$endpoint" ]; then
  echo "Version, bucket, and endpoint are required." >&2
  usage >&2
  exit 2
fi
if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "Version must be valid Vastora SemVer." >&2
  exit 2
fi
case "$bucket" in
  *[!a-z0-9.-]*|'') echo "R2 bucket name is invalid." >&2; exit 2 ;;
esac
case "$endpoint" in
  https://*.r2.cloudflarestorage.com) ;;
  *) echo "R2 endpoint must use the Cloudflare HTTPS endpoint." >&2; exit 2 ;;
esac

for required in awk aws cmp grep jq mktemp sha256sum; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
semver_comparator="$script_dir/compare-semver.awk"
if [ ! -f "$semver_comparator" ]; then
  echo "Release tooling is incomplete: compare-semver.awk is missing." >&2
  exit 1
fi

tag="v$version"
prefix="vastora/releases/$tag"
manifest_key="$prefix/manifest.json"
activated_key="$prefix/activated.json"
current_key="vastora/current.json"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-r2-release.XXXXXX")"
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM

aws_r2() {
  aws --cli-connect-timeout 10 --cli-read-timeout 60 s3api "$@" --endpoint-url "$endpoint" --no-cli-pager
}

retry() {
  attempt=1
  while ! "$@"; do
    if [ "$attempt" -ge 3 ]; then
      return 1
    fi
    attempt=$((attempt + 1))
    sleep 2
  done
}

remote_digest() {
  key="$1"
  aws_r2 head-object --bucket "$bucket" --key "$key" --query 'Metadata.sha256' --output text 2>/dev/null || true
}

upload_immutable() {
  source="$1"
  key="$2"
  content_type="$3"
  digest="$(sha256sum "$source" | awk 'NR == 1 {print tolower($1)}')"
  head="$temporary_dir/head.json"
  if aws_r2 head-object --bucket "$bucket" --key "$key" >"$head" 2>/dev/null; then
    existing="$(jq -r '.Metadata.sha256 // empty' "$head")"
    if [ "$existing" != "$digest" ]; then
      echo "R2 release object already exists with different content: $key" >&2
      return 1
    fi
  else
    retry aws_r2 put-object \
      --bucket "$bucket" \
      --key "$key" \
      --body "$source" \
      --content-type "$content_type" \
      --cache-control 'public, max-age=31536000, immutable' \
      --metadata "sha256=$digest" >/dev/null
  fi
  if [ "$(remote_digest "$key")" != "$digest" ]; then
    echo "R2 release object failed metadata verification: $key" >&2
    return 1
  fi
  printf '%s' "$digest"
}

verify_manifest() {
  verify_manifest_path="$1"
  verify_manifest_version="${2:-$version}"
  verify_manifest_prefix="vastora/releases/v$verify_manifest_version/"
  jq -e \
    --arg version "$verify_manifest_version" \
    --arg prefix "$verify_manifest_prefix" \
    '(.schema == 1) and (.version == $version) and
     ((.assets | keys | sort) == ["install.sh", "vastora-center-install.tar.gz", "vastora-center-install.tar.gz.sha256"]) and
     (.assets["install.sh"].key == ($prefix + "install.sh")) and
     (.assets["vastora-center-install.tar.gz"].key == ($prefix + "vastora-center-install.tar.gz")) and
     (.assets["vastora-center-install.tar.gz.sha256"].key == ($prefix + "vastora-center-install.tar.gz.sha256")) and
     (all(.assets[]; .sha256 | test("^[0-9a-f]{64}$")))' \
    "$verify_manifest_path" >/dev/null
}

verify_remote_assets() {
  verify_remote_manifest_path="$1"
  jq -r '.assets[] | [.key, .sha256] | @tsv' "$verify_remote_manifest_path" |
    while IFS="$(printf '\t')" read -r key digest; do
      if [ "$(remote_digest "$key")" != "$digest" ]; then
        echo "R2 manifest references an unavailable or mismatched object: $key" >&2
        exit 1
      fi
    done
}

if [ "$command_name" = "stage" ]; then
  for required in sh tar; do
    if ! command -v "$required" >/dev/null 2>&1; then
      echo "Required command is not installed: $required" >&2
      exit 1
    fi
  done
  installer="$source_dir/install.sh"
  bundle="$source_dir/vastora-center-install.tar.gz"
  checksum="$source_dir/vastora-center-install.tar.gz.sha256"
  manifest="$source_dir/vastora-release-manifest.json"
  for required_file in "$installer" "$bundle" "$checksum" "$manifest"; do
    if [ ! -f "$required_file" ]; then
      echo "Release asset is missing: $required_file" >&2
      exit 1
    fi
  done
  if ! sh -n "$installer"; then
    echo "The Center installer script is invalid." >&2
    exit 1
  fi
  bundle_version="$(tar -xOzf "$bundle" ./release.env 2>/dev/null | awk -F= '$1 == "VASTORA_VERSION" {print $2; exit}')"
  if [ "$bundle_version" != "$version" ]; then
    echo "The Center bundle version does not match the R2 release version." >&2
    exit 1
  fi
  expected_bundle_digest="$(awk 'NR == 1 {print tolower($1)}' "$checksum")"
  actual_bundle_digest="$(sha256sum "$bundle" | awk 'NR == 1 {print tolower($1)}')"
  if ! printf '%s\n' "$expected_bundle_digest" | grep -Eq '^[0-9a-f]{64}$' || [ "$actual_bundle_digest" != "$expected_bundle_digest" ]; then
    echo "The Center bundle checksum is invalid." >&2
    exit 1
  fi

  verify_manifest "$manifest"
  for asset in install.sh vastora-center-install.tar.gz vastora-center-install.tar.gz.sha256; do
    expected_digest="$(jq -r --arg asset "$asset" '.assets[$asset].sha256' "$manifest")"
    actual_digest="$(sha256sum "$source_dir/$asset" | awk 'NR == 1 {print tolower($1)}')"
    if [ "$actual_digest" != "$expected_digest" ]; then
      echo "The release manifest digest does not match $asset." >&2
      exit 1
    fi
  done

  upload_immutable "$installer" "$prefix/install.sh" 'text/x-shellscript; charset=utf-8' >/dev/null
  upload_immutable "$bundle" "$prefix/vastora-center-install.tar.gz" 'application/gzip' >/dev/null
  upload_immutable "$checksum" "$prefix/vastora-center-install.tar.gz.sha256" 'text/plain; charset=utf-8' >/dev/null
  upload_immutable "$manifest" "$manifest_key" 'application/json; charset=utf-8' >/dev/null
  verify_remote_assets "$manifest"
  echo "Staged Vastora $version installer assets in R2."
  exit 0
fi

manifest="$temporary_dir/manifest.json"
retry aws_r2 get-object --bucket "$bucket" --key "$manifest_key" "$manifest" >/dev/null
verify_manifest "$manifest"
verify_remote_assets "$manifest"
active_manifest="$temporary_dir/active-current.json"
active_head="$temporary_dir/active-current-head.json"
active_head_error="$temporary_dir/active-current-head.err"
active_exists=no
if aws_r2 head-object --bucket "$bucket" --key "$current_key" >"$active_head" 2>"$active_head_error"; then
  active_exists=yes
elif ! grep -Eq '(^|[^0-9])404([^0-9]|$)|NoSuchKey|Not Found' "$active_head_error"; then
  echo "R2 current release pointer could not be inspected; refusing activation." >&2
  cat "$active_head_error" >&2
  exit 1
fi
if [ "$active_exists" = yes ]; then
  retry aws_r2 get-object --bucket "$bucket" --key "$current_key" "$active_manifest" >/dev/null
  active_version="$(jq -er '.version | strings' "$active_manifest")"
  if ! printf '%s' "$active_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
    echo "R2 current release pointer contains an invalid version; refusing to replace it." >&2
    exit 1
  fi
  if ! verify_manifest "$active_manifest" "$active_version"; then
    echo "R2 current release pointer is invalid; refusing to replace it." >&2
    exit 1
  fi
  if [ "$(awk -f "$semver_comparator" "$version" "$active_version")" = "-1" ]; then
    echo "Refusing to move R2 current release backward from $active_version to $version." >&2
    exit 1
  fi
fi
upload_immutable "$manifest" "$activated_key" 'application/json; charset=utf-8' >/dev/null
manifest_digest="$(sha256sum "$manifest" | awk 'NR == 1 {print tolower($1)}')"
retry aws_r2 put-object \
  --bucket "$bucket" \
  --key "$current_key" \
  --body "$manifest" \
  --content-type 'application/json; charset=utf-8' \
  --cache-control 'no-cache' \
  --metadata "sha256=$manifest_digest,version=$version" >/dev/null
activated="$temporary_dir/current.json"
retry aws_r2 get-object --bucket "$bucket" --key "$current_key" "$activated" >/dev/null
if ! cmp -s "$manifest" "$activated"; then
  echo "R2 current release pointer verification failed." >&2
  exit 1
fi
echo "Activated Vastora $version installer release in R2."
