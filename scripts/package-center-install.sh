#!/bin/sh
set -eu

version=""
image=""
output_dir="dist"

usage() {
  cat <<'EOF'
Usage: package-center-install.sh --version VERSION \
  --image IMAGE@sha256:DIGEST [--output-dir DIR]

Creates the verified release assets consumed by the public one-line installer.
This command does not build or upload a container image.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) version="$2"; shift 2 ;;
    --image) image="$2"; shift 2 ;;
    --output-dir) output_dir="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -z "$version" ] || [ -z "$image" ]; then
  echo "Version and immutable Center image are required." >&2
  usage >&2
  exit 2
fi
case "$version" in
  *[!A-Za-z0-9._-]*) echo "Version may contain only letters, numbers, dot, underscore, and hyphen." >&2; exit 2 ;;
esac
case "$image" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *) echo "Center image must be pinned by a complete sha256 digest." >&2; exit 2 ;;
esac
image_digest="${image##*@sha256:}"
case "$image_digest" in
  *[!0-9a-fA-F]*) echo "Center image sha256 digest is invalid." >&2; exit 2 ;;
esac
case "$image" in
  *[[:space:]]*) echo "Center image cannot contain whitespace." >&2; exit 2 ;;
esac

for required in awk basename install mktemp tar; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done
if command -v sha256sum >/dev/null 2>&1; then
  digest_command="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  digest_command="shasum -a 256"
else
  echo "sha256sum or shasum is required." >&2
  exit 1
fi

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-center-package.XXXXXX")"
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM

install -m 0755 "$project_dir/install.sh" "$temporary_dir/install.sh"
install -m 0755 "$project_dir/deploy/center/setup.sh" "$temporary_dir/setup.sh"
install -m 0755 "$project_dir/deploy/center/upgrade.sh" "$temporary_dir/upgrade.sh"
install -m 0755 "$project_dir/deploy/center/uninstall.sh" "$temporary_dir/uninstall.sh"
install -m 0755 "$project_dir/deploy/center/install-host-cli.sh" "$temporary_dir/install-host-cli.sh"
install -m 0755 "$project_dir/deploy/center/install-update-service.sh" "$temporary_dir/install-update-service.sh"
install -m 0755 "$project_dir/deploy/center/update-center.sh" "$temporary_dir/update-center.sh"
install -m 0644 "$project_dir/scripts/compare-semver.awk" "$temporary_dir/compare-semver.awk"
install -m 0644 "$project_dir/deploy/center/runtime-network.sh" "$temporary_dir/runtime-network.sh"
install -m 0644 "$project_dir/deploy/center/compose.yaml" "$temporary_dir/compose.yaml"
{
  printf 'VASTORA_VERSION=%s\n' "$version"
  printf 'VASTORA_CENTER_IMAGE=%s\n' "$image"
} > "$temporary_dir/release.env"
chmod 0644 "$temporary_dir/release.env"

install -d -m 0755 "$output_dir"
archive="$output_dir/vastora-center-install.tar.gz"
checksum="$archive.sha256"
tar -czf "$archive" -C "$temporary_dir" .
archive_digest="$($digest_command "$archive" | awk 'NR == 1 {print $1}')"
printf '%s  %s\n' "$archive_digest" "$(basename "$archive")" > "$checksum"
echo "Created $archive"
echo "Created $checksum"
