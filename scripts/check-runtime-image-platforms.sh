#!/bin/sh
set -eu

project_dir="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
temporary="$(mktemp -t vastora-runtime-images.XXXXXX)"
trap 'rm -f "$temporary"' EXIT HUP INT TERM

jq -r '.. | objects | .reference? // empty' "$project_dir/catalog/catalog.json" >"$temporary"
grep -RhoE --include='*.go' --exclude='*_test.go' \
  '(docker.io|ghcr.io)/[^"[:space:]]+@sha256:[0-9a-f]{64}' \
  "$project_dir/internal" >>"$temporary"

sort -u "$temporary" | xargs -n 1 -P 4 "$project_dir/scripts/assert-image-platforms.sh"
