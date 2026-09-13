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
require_line "$center_dockerfile" 'COPY catalog/trust/ /app/catalog-trust/'
require_line "$project_dir/.dockerignore" '**/*_test.go'
require_line "$project_dir/deploy/center/compose.yaml" '${VASTORA_OFFICIAL_CATALOG_ROOT:-/app/catalog-trust/1.root.json}'
require_line "$project_dir/.github/workflows/release.yml" 'go run ./cmd/catalog-check --catalog catalog/catalog.json --root-directory catalog/trust'
require_line "$ci_workflow" '    name: Go race tests'
require_line "$ci_workflow" '    name: Go quality and security'
require_line "$ci_workflow" '    name: Go cross-compile'
require_line "$ci_workflow" '    name: Alpha minimal checks'
require_line "$ci_workflow" "    if: github.event_name == 'pull_request' && vars.VASTORA_CI_MODE == 'alpha'"
require_line "$ci_workflow" '          ALPHA_RESULT: ${{ needs.alpha-minimal.result }}'
require_line "$ci_workflow" '          for result in "$CHANGES_RESULT" "$ALPHA_RESULT"'
require_line "$ci_workflow" "vars.VASTORA_CI_MODE != 'alpha'"
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
require_line "$codeql_workflow" "vars.VASTORA_CI_MODE != 'alpha'"
require_line "$codeql_workflow" '  group: codeql-${{ github.workflow }}-${{ github.ref }}'
require_line "$codeql_workflow" "  cancel-in-progress: \${{ github.event_name == 'pull_request' }}"

# Alpha only validates source/configuration shape. Compilation and the minimal
# executable check happen once, on the actual release artifact.
alpha_job="$(sed -n '/^  alpha-minimal:/,/^  go-race:/p' "$ci_workflow")"
if ! printf '%s\n' "$alpha_job" | grep -Fq 'cache: false' ||
   ! printf '%s\n' "$alpha_job" | grep -Fq 'run: make go-format-check' ||
   printf '%s\n' "$alpha_job" | grep -Eq '(go test|go build|go-static-check|web-check|cache: true|docker build)'; then
  echo 'Alpha CI must not restore the Go build cache or duplicate release builds/full checks.' >&2
  exit 1
fi
for job in go-race go-quality go-build web deployment security container-image-security; do
  condition="$(sed -n "/^  $job:/,/^    needs:/p" "$ci_workflow" | grep '^    if:')"
  if ! printf '%s\n' "$condition" | grep -Fq "vars.VASTORA_CI_MODE != 'alpha'" ||
     ! printf '%s\n' "$condition" | grep -Fq "github.event_name == 'workflow_dispatch'" ||
     ! printf '%s\n' "$condition" | grep -Fq "github.event_name == 'schedule'"; then
    echo "$job must defer full Alpha checks to manual or scheduled runs." >&2
    exit 1
  fi
done
for job in analyze-go analyze-javascript; do
  condition="$(sed -n "/^  $job:/,/^    needs:/p" "$codeql_workflow" | grep '^    if:')"
  if ! printf '%s\n' "$condition" | grep -Fq "vars.VASTORA_CI_MODE != 'alpha'" ||
     printf '%s\n' "$condition" | grep -Fq "github.event_name != 'pull_request'"; then
    echo 'CodeQL must not run full Alpha analysis on every main-branch push.' >&2
    exit 1
  fi
done
require_line "$project_dir/.github/workflows/catalog-check.yml" "    if: github.event_name != 'pull_request' || vars.VASTORA_CI_MODE != 'alpha'"
require_line "$project_dir/.github/workflows/catalog-check.yml" '  schedule:'

if grep -Fq 'cache-to: type=gha' "$ci_workflow"; then
  echo 'Pull-request image builds must not write GitHub Actions caches.' >&2
  exit 1
fi
ci_frontend="$(printf '%s\n' 'web/src/App.tsx' | "$classifier" --files ci)"
ci_lockfile="$(printf '%s\n' 'web/package-lock.json' | "$classifier" --files ci)"
codeql_go="$(printf '%s\n' 'internal/center/server.go' | "$classifier" --files codeql)"
ci_openapi="$(printf '%s\n' 'docs/openapi.json' | "$classifier" --files ci)"
ci_catalog="$(printf '%s\n' 'catalog/catalog.json' | "$classifier" --files ci)"
ci_catalog_root="$(printf '%s\n' 'catalog/trust/1.root.json' | "$classifier" --files ci)"
if ! printf '%s\n' "$ci_catalog" | grep -Fq 'container=false' || ! printf '%s\n' "$ci_catalog_root" | grep -Fq 'container=true'; then
  echo 'Catalog content is independently published; reviewed bootstrap roots must be packaged with Center.' >&2
  exit 1
fi
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
