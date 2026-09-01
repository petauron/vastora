#!/bin/sh
set -eu

version=""
source_dir="dist"

usage() {
  cat <<'EOF'
Usage: create-installer-release-manifest.sh --version VERSION [--source-dir DIR]

Validates the three packaged installer files and writes the fourth durable
asset, vastora-release-manifest.json, used by both GitHub Releases and R2.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) version="${2:-}"; shift 2 ;;
    --source-dir) source_dir="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "Version must be valid Vastora SemVer." >&2
  exit 2
fi
for required in awk grep jq sh sha256sum tar; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done

installer="$source_dir/install.sh"
bundle="$source_dir/vastora-center-install.tar.gz"
checksum="$source_dir/vastora-center-install.tar.gz.sha256"
manifest="$source_dir/vastora-release-manifest.json"
for required_file in "$installer" "$bundle" "$checksum"; do
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
  echo "The Center bundle version does not match the release version." >&2
  exit 1
fi
expected_bundle_digest="$(awk 'NR == 1 {print tolower($1)}' "$checksum")"
actual_bundle_digest="$(sha256sum "$bundle" | awk 'NR == 1 {print tolower($1)}')"
if ! printf '%s\n' "$expected_bundle_digest" | grep -Eq '^[0-9a-f]{64}$' || [ "$actual_bundle_digest" != "$expected_bundle_digest" ]; then
  echo "The Center bundle checksum is invalid." >&2
  exit 1
fi

prefix="vastora/releases/v$version"
installer_digest="$(sha256sum "$installer" | awk 'NR == 1 {print tolower($1)}')"
checksum_digest="$(sha256sum "$checksum" | awk 'NR == 1 {print tolower($1)}')"
jq -n -S \
  --arg version "$version" \
  --arg installer_key "$prefix/install.sh" \
  --arg installer_digest "$installer_digest" \
  --arg bundle_key "$prefix/vastora-center-install.tar.gz" \
  --arg bundle_digest "$actual_bundle_digest" \
  --arg checksum_key "$prefix/vastora-center-install.tar.gz.sha256" \
  --arg checksum_digest "$checksum_digest" \
  '{schema: 1, version: $version, assets: {
    "install.sh": {key: $installer_key, sha256: $installer_digest},
    "vastora-center-install.tar.gz": {key: $bundle_key, sha256: $bundle_digest},
    "vastora-center-install.tar.gz.sha256": {key: $checksum_key, sha256: $checksum_digest}
  }}' > "$manifest"

echo "Created $manifest"
