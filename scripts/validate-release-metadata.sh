#!/bin/sh
set -eu

base_ref="${1:-}"
script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="${2:-$(CDPATH='' cd -- "$script_dir/.." && pwd)}"

usage() {
  echo "Usage: validate-release-metadata.sh BASE_REF [PROJECT_DIR]" >&2
}

if [ -z "$base_ref" ] || [ "${base_ref#-}" != "$base_ref" ] || [ "$#" -gt 2 ]; then
  usage
  exit 2
fi
for required in awk git grep jq sed; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done
if ! git -C "$project_dir" rev-parse --git-dir >/dev/null 2>&1; then
  echo "Release metadata project is not a Git checkout: $project_dir" >&2
  exit 2
fi
if ! git -C "$project_dir" rev-parse --verify "$base_ref^{commit}" >/dev/null 2>&1; then
  echo "Release metadata base is not a commit: $base_ref" >&2
  exit 2
fi

expected_files='.release-please-manifest.json
CHANGELOG.md
version.txt'
actual_files="$(git -C "$project_dir" diff --name-only "$base_ref" | LC_ALL=C sort)"
if [ "$actual_files" != "$expected_files" ]; then
  echo "Release pull requests may modify only the three generated release metadata files." >&2
  printf 'Expected:\n%s\nActual:\n%s\n' "$expected_files" "${actual_files:-<none>}" >&2
  exit 1
fi

semver_pattern='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
version="$(sed -n '1p' "$project_dir/version.txt")"
version_lines="$(awk 'END {print NR}' "$project_dir/version.txt")"
if [ -z "$version" ] || [ "$version_lines" -ne 1 ]; then
  echo "version.txt must contain exactly one non-empty line." >&2
  exit 1
fi
if ! printf '%s\n' "$version" | grep -Eq "$semver_pattern"; then
  echo "Release version is not valid SemVer: $version" >&2
  exit 1
fi
base_version="$(git -C "$project_dir" show "$base_ref:version.txt")"
if ! printf '%s\n' "$base_version" | grep -Eq "$semver_pattern"; then
  echo "Base release version is not valid SemVer: $base_version" >&2
  exit 1
fi
if [ "$(awk -f "$script_dir/compare-semver.awk" "$base_version" "$version")" != '-1' ]; then
  echo "Release version must be greater than the base version: $version <= $base_version" >&2
  exit 1
fi
if ! jq -e --arg version "$version" \
  'type == "object" and length == 1 and .["."] == $version' \
  "$project_dir/.release-please-manifest.json" >/dev/null; then
  echo "Release manifest does not match version.txt." >&2
  exit 1
fi
if ! grep -Fq "## [$version](" "$project_dir/CHANGELOG.md"; then
  echo "CHANGELOG.md does not contain the release heading for $version." >&2
  exit 1
fi

echo "Release metadata is valid for $version."
