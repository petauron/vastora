#!/bin/sh
set -eu

usage() {
  echo "usage: classify-ci-changes.sh --git <ci|codeql> <base-sha> <head-sha>" >&2
  echo "       classify-ci-changes.sh --files <ci|codeql>" >&2
  exit 2
}

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"

race_policy_mode() {
  case "${1%%+*}" in
    *-alpha|*-alpha.*) printf 'alpha\n' ;;
    *) printf 'race\n' ;;
  esac
}

is_verified_version_only_release() {
  _repo_dir="$1"
  _base_ref="$2"
  _merge_base="$3"
  _head_ref="$4"

  if ! "$script_dir/validate-release-metadata.sh" "$_merge_base" "$_repo_dir" "$_head_ref" >/dev/null 2>&1; then
    return 1
  fi

  _semver_pattern='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
  _merge_base_version="$(git -C "$_repo_dir" show "$_merge_base:version.txt" 2>/dev/null || true)"
  _base_version="$(git -C "$_repo_dir" show "$_base_ref:version.txt" 2>/dev/null || true)"
  _head_version="$(git -C "$_repo_dir" show "$_head_ref:version.txt" 2>/dev/null || true)"
  for _ver in "$_merge_base_version" "$_base_version" "$_head_version"; do
    if [ -z "$_ver" ] || [ "$(printf '%s\n' "$_ver" | awk 'END { print NR }')" -ne 1 ] || ! printf '%s\n' "$_ver" | grep -Eq "$_semver_pattern"; then
      return 1
    fi
  done

  # Also check against the current target base ref when the PR is behind main.
  if [ "$(awk -f "$script_dir/compare-semver.awk" "$_base_version" "$_head_version" 2>/dev/null || true)" != '-1' ]; then
    return 1
  fi

  # Alpha-to-non-alpha (or reverse) transitions change the Go -race policy and
  # must always fail closed to the full CI suite.
  _merge_policy="$(race_policy_mode "$_merge_base_version")"
  _base_policy="$(race_policy_mode "$_base_version")"
  _head_policy="$(race_policy_mode "$_head_version")"
  if [ "$_merge_policy" != "$_head_policy" ] || [ "$_base_policy" != "$_head_policy" ]; then
    return 1
  fi

  return 0
}

mode="${1:-}"
profile="${2:-}"
case "$profile" in
  ci|codeql) ;;
  *) usage ;;
esac

version_only_release=false
case "$mode" in
  --git)
    [ "$#" -eq 4 ] || usage
    repo_dir="$(git rev-parse --show-toplevel)"
    merge_base="$(git merge-base "$3" "$4")"
    paths="$(git diff --name-only "$merge_base" "$4")"
    if is_verified_version_only_release "$repo_dir" "$3" "$merge_base" "$4"; then
      version_only_release=true
    fi
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
    internal/agent/*|internal/landing/*|internal/recovery/*|internal/dantebundle/*|internal/deployer/*|internal/glivelaruntime/*|internal/center/*composite*|internal/center/*landing*|internal/center/agent_*|internal/center/store_agent*|internal/center/business_handover*|internal/center/server*|internal/center/setup*|internal/center/initial_setup*|internal/center/topology*|internal/center/orchestration*|internal/center/schema*|internal/center/migrations/*|cmd/*|deploy/center/*.sh|deploy/center/compose.yaml|deploy/landing/Dockerfile|scripts/run-composite-runtime-lab.sh|scripts/build-landing-dante.sh|scripts/install-landing-build-deps.sh|scripts/glivela-recovery-lab/*|catalog/*|internal/center/testdata/reviewed-catalog-v4.json|go.mod|go.sum|Dockerfile*) integration=true ;;
  esac
  case "$path" in
    .release-please-manifest.json|version.txt)
      release_metadata=true
      if [ "$version_only_release" != true ]; then
        full=true
      fi
      ;;
    *) security=true ;;
  esac
  case "$path" in
    web/*|deploy/installer-worker/*|scripts/*.mjs|scripts/*.js) javascript=true ;;
  esac
  case "$path" in
    .github/workflows/*|.github/actions/*|.github/dependabot.yml|Makefile|release-please-config.json|scripts/classify-ci-changes.sh|scripts/validate-release-metadata.sh|scripts/compare-semver.awk) full=true ;;
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
