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
integration=false
dependencies=false
release_metadata=false
javascript=false
full=false

while IFS= read -r path; do
  [ -n "$path" ] || continue

  case "$path" in
    cmd/*|internal/*|go.mod|go.sum|docs/openapi.json|scripts/generate-openapi.mjs|scripts/run-ci-go-tests.sh|scripts/select-center-tests.sh|scripts/center-test-timings.txt) go=true ;;
  esac
  case "$path" in
    web/*) web=true ;;
  esac
  case "$path" in
    deploy/*|scripts/*|install.sh|catalog/*|internal/center/testdata/reviewed-catalog-v4.json|Dockerfile*|release-please-config.json) deployment=true ;;
  esac
  case "$path" in
    go.mod|go.sum|web/package.json|web/package-lock.json) dependencies=true ;;
  esac
  case "$path" in
    internal/agent/*|internal/landing/*|internal/recovery/*|internal/dantebundle/*|internal/deployer/*|internal/meridianruntime/*|internal/center/*composite*|internal/center/*landing*|cmd/*|deploy/center/*.sh|deploy/center/compose.yaml|deploy/landing/Dockerfile|scripts/run-composite-runtime-lab.sh|scripts/build-landing-dante.sh|scripts/install-landing-build-deps.sh|scripts/meridian-recovery-lab/*|catalog/*|internal/center/testdata/reviewed-catalog-v4.json|go.mod|go.sum|Dockerfile*) integration=true ;;
  esac
  case "$path" in
    .release-please-manifest.json|version.txt) release_metadata=true; full=true ;;
    *) security=true ;;
  esac
  case "$path" in
    web/*|deploy/installer-worker/*|scripts/*.mjs|scripts/*.js) javascript=true ;;
  esac
  case "$path" in
    .github/workflows/*|.github/actions/*|.github/dependabot.yml|Makefile|release-please-config.json|scripts/classify-ci-changes.sh) full=true ;;
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

printf 'go=%s\nweb=%s\ndeployment=%s\nsecurity=%s\nintegration=%s\ndependencies=%s\nrelease_metadata=%s\nfull=%s\n' \
  "$go" "$web" "$deployment" "$security" "$integration" "$dependencies" "$release_metadata" "$full"
