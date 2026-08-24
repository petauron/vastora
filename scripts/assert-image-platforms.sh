#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "Usage: $0 <image-reference>" >&2
  exit 2
fi
if ! command -v docker >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then
  echo "docker buildx and jq are required." >&2
  exit 2
fi

image="$1"
manifest="$(docker buildx imagetools inspect --raw "$image")"
for architecture in amd64 arm64; do
  if ! printf '%s' "$manifest" | jq -e --arg architecture "$architecture" \
    'any(.manifests[]?.platform; .os == "linux" and .architecture == $architecture)' >/dev/null; then
    echo "$image does not provide linux/$architecture." >&2
    exit 1
  fi
done
