#!/bin/sh
set -eu

usage() {
  echo "usage: classify-ci-changes.sh --git <ci|codeql> <base-sha> <head-sha>" >&2
  echo "       classify-ci-changes.sh --files <ci|codeql>" >&2
  exit 2
}

mode="${1:-}"
profile="${2:-}"
case "$profile" in
  ci|codeql) ;;
  *) usage ;;
esac

case "$mode" in
  --git)
    [ "$#" -eq 4 ] || usage
    paths="$(git diff --name-only "$3" "$4")"
    ;;
  --files)
    [ "$#" -eq 2 ] || usage
    paths="$(cat)"
    ;;
  *) usage ;;
esac

go=false
web=false
deployment=false
security=false
container=false
release_metadata=false
javascript=false
full=false

while IFS= read -r path; do
  [ -n "$path" ] || continue

  case "$path" in
    cmd/*|internal/*|go.mod|go.sum|docs/openapi.json|scripts/generate-openapi.mjs) go=true ;;
  esac
  case "$path" in
    web/*) web=true ;;
  esac
  case "$path" in
    deploy/*|scripts/*|install.sh|catalog/*|Dockerfile*|release-please-config.json) deployment=true ;;
  esac
  case "$path" in
    Dockerfile.center|.dockerignore|go.mod|go.sum|web/package.json|web/package-lock.json|catalog/catalog.json) container=true ;;
  esac
  case "$path" in
    CHANGELOG.md|.release-please-manifest.json|version.txt) release_metadata=true ;;
    *) security=true ;;
  esac
  case "$path" in
    web/*|deploy/installer-worker/*|scripts/*.mjs|scripts/*.js) javascript=true ;;
  esac
  case "$path" in
    .github/workflows/*|.github/dependabot.yml|Makefile|release-please-config.json) full=true ;;
  esac

  case "$path" in
    cmd/*|internal/*|web/*|deploy/*|scripts/*|catalog/*|.github/*|docs/*|*.md|Dockerfile*|.dockerignore|go.mod|go.sum|install.sh|version.txt|.release-please-manifest.json|release-please-config.json|LICENSE) ;;
    *) full=true ;;
  esac
done <<EOF
$paths
EOF

if [ "$profile" = codeql ]; then
  printf 'go=%s\njavascript=%s\nfull=%s\n' "$go" "$javascript" "$full"
  exit 0
fi

printf 'go=%s\nweb=%s\ndeployment=%s\nsecurity=%s\ncontainer=%s\nrelease_metadata=%s\nfull=%s\n' \
  "$go" "$web" "$deployment" "$security" "$container" "$release_metadata" "$full"
