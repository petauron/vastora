#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-center-install-test.XXXXXX")"
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
expected_digest="$(awk 'NR == 1 {print $1}' "$archive.sha256")"
if command -v sha256sum >/dev/null 2>&1; then
  actual_digest="$(sha256sum "$archive" | awk 'NR == 1 {print $1}')"
else
  actual_digest="$(shasum -a 256 "$archive" | awk 'NR == 1 {print $1}')"
fi
test "$expected_digest" = "$actual_digest"
tar -xzf "$archive" -C "$temporary_dir"
grep -Fqx 'VASTORA_VERSION=0.1.0-test' "$temporary_dir/release.env"
grep -Fqx "VASTORA_CENTER_IMAGE=$image" "$temporary_dir/release.env"
test -x "$temporary_dir/setup.sh"
test -f "$temporary_dir/compose.yaml"
test -f "$temporary_dir/headscale/config.yaml"
test -f "$temporary_dir/headscale/policy.hujson"
grep -Fq '127.0.0.1:${VASTORA_CENTER_BOOTSTRAP_PORT:-8080}' "$temporary_dir/compose.yaml"
grep -Fq 'network_mode: host' "$temporary_dir/compose.yaml"
if sed -n '/^  center:/,/^  headscale:/p' "$temporary_dir/compose.yaml" | grep -Fq '    ports:'; then
  echo "Center install bundle still publishes a Docker port" >&2
  exit 1
fi
if grep -Fq '${VASTORA_CENTER_PORT:-443}:8080' "$temporary_dir/compose.yaml"; then
  echo "Center install bundle still claims public port 443" >&2
  exit 1
fi
grep -Fq 'ssh -N -L 18082:127.0.0.1:$bootstrap_port' "$temporary_dir/setup.sh"
grep -Fq 'Public port 443: unchanged' "$temporary_dir/setup.sh"

if "$project_dir/scripts/package-center-install.sh" \
  --version 0.1.0-test \
  --image 'ghcr.io/petauron/vastora-center@sha256:not-a-digest' \
  --output-dir "$temporary_dir/invalid" >/dev/null 2>&1; then
  echo "Invalid image digest was accepted" >&2
  exit 1
fi

echo "Center install bundle test passed"
