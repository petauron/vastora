#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
ci_workflow="$project_dir/.github/workflows/ci.yml"
codeql_workflow="$project_dir/.github/workflows/codeql.yml"
cache_workflow="$project_dir/.github/workflows/dependency-cache.yml"
center_dockerfile="$project_dir/Dockerfile.center"
classifier="$project_dir/scripts/classify-ci-changes.sh"

require_line() {
  file="$1"
  expected="$2"
  if ! grep -Fq "$expected" "$file"; then
    echo "$file is missing: $expected" >&2
    exit 1
  fi
}

require_line "$ci_workflow" '    name: CI / gate'
require_line "$ci_workflow" '    name: Go race tests'
require_line "$ci_workflow" '    name: Go quality and security'
require_line "$ci_workflow" '    name: Go cross-compile'
require_line "$ci_workflow" '    name: Release metadata'
require_line "$ci_workflow" '        run: scripts/validate-release-metadata.sh "$BASE_SHA"'
require_line "$ci_workflow" '        run: scripts/classify-ci-changes.sh --git ci "$BASE_SHA" "$HEAD_SHA" >> "$GITHUB_OUTPUT"'
require_line "$codeql_workflow" '        run: scripts/classify-ci-changes.sh --git codeql "$BASE_SHA" "$HEAD_SHA" >> "$GITHUB_OUTPUT"'
require_line "$cache_workflow" '    name: Warm dependency caches'
require_line "$cache_workflow" '      - go.mod'
require_line "$cache_workflow" '      - go.sum'
require_line "$cache_workflow" '      - web/package-lock.json'
require_line "$ci_workflow" '      DOCKER_BUILD_RECORD_UPLOAD: "false"'
require_line "$ci_workflow" '          cache-from: type=registry,ref=ghcr.io/petauron/vastora-center:buildcache'
require_line "$ci_workflow" '        run: scripts/check-runtime-image-platforms.sh'
require_line "$ci_workflow" '        run: go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --redact --verbose .'
require_line "$codeql_workflow" '    name: CodeQL / gate'
require_line "$codeql_workflow" '  group: codeql-${{ github.workflow }}-${{ github.ref }}'
require_line "$codeql_workflow" "  cancel-in-progress: \${{ github.event_name == 'pull_request' }}"

if grep -Fq 'cache-to: type=gha' "$ci_workflow"; then
  echo 'Pull-request image builds must not write GitHub Actions caches.' >&2
  exit 1
fi
ci_frontend="$(printf '%s\n' 'web/src/App.tsx' | "$classifier" --files ci)"
ci_lockfile="$(printf '%s\n' 'web/package-lock.json' | "$classifier" --files ci)"
codeql_go="$(printf '%s\n' 'internal/center/server.go' | "$classifier" --files codeql)"
ci_openapi="$(printf '%s\n' 'docs/openapi.json' | "$classifier" --files ci)"
if ! printf '%s\n' "$ci_frontend" | grep -Fq 'web=true' || ! printf '%s\n' "$ci_frontend" | grep -Fq 'container=false'; then
  echo 'Ordinary frontend source changes were classified incorrectly.' >&2
  exit 1
fi
if ! printf '%s\n' "$ci_lockfile" | grep -Fq 'web=true' || ! printf '%s\n' "$ci_lockfile" | grep -Fq 'container=true'; then
  echo 'Container dependency changes were classified incorrectly.' >&2
  exit 1
fi
if ! printf '%s\n' "$codeql_go" | grep -Fq 'go=true' || ! printf '%s\n' "$codeql_go" | grep -Fq 'javascript=false'; then
  echo 'CodeQL language changes were classified incorrectly.' >&2
  exit 1
fi
if ! printf '%s\n' "$ci_openapi" | grep -Fq 'go=true'; then
  echo 'OpenAPI contract changes must run the Go contract validator.' >&2
  exit 1
fi
if grep -Fq 'dorny/paths-filter' "$ci_workflow" "$codeql_workflow" || grep -Fq 'gitleaks/gitleaks-action' "$ci_workflow"; then
  echo 'CI must not require additional third-party Actions permissions.' >&2
  exit 1
fi
if grep -Fq 'runtime-image-platforms:' "$ci_workflow"; then
  echo 'Runtime image validation should share the deployment runner.' >&2
  exit 1
fi
for architecture in amd64 arm64; do
  build_count="$(grep -Fc "GOARCH=$architecture " "$center_dockerfile")"
  if [ "$build_count" -ne 1 ]; then
    echo "Center image must compile $architecture exactly once; found $build_count builds." >&2
    exit 1
  fi
done
if grep -Fq 'target=/go/pkg/mod' "$center_dockerfile"; then
  echo 'Center image dependency downloads must live in an exportable layer, not an unexported cache mount.' >&2
  exit 1
fi
if grep -Eq '^  push:' "$ci_workflow"; then
  echo 'Main-branch dependency cache warming must use the path-filtered cache workflow.' >&2
  exit 1
fi
for workflow in "$ci_workflow" "$codeql_workflow" "$cache_workflow"; do
  if ! grep -Fq 'timeout-minutes:' "$workflow"; then
    echo "$workflow has no job timeouts." >&2
    exit 1
  fi
done

echo "CI workflow policy test passed"
