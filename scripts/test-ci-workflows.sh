#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
ci_workflow="$project_dir/.github/workflows/ci.yml"
codeql_workflow="$project_dir/.github/workflows/codeql.yml"
container_filter="$(sed -n '/^            container:/,/^            non_release:/p' "$ci_workflow")"

require_line() {
  file="$1"
  expected="$2"
  if ! grep -Fq "$expected" "$file"; then
    echo "$file is missing: $expected" >&2
    exit 1
  fi
}

require_line "$ci_workflow" '    name: CI / gate'
require_line "$ci_workflow" '    name: Release metadata'
require_line "$ci_workflow" '        run: scripts/validate-release-metadata.sh "$BASE_SHA"'
require_line "$ci_workflow" '        uses: dorny/paths-filter@ceb8a2b8f2d89434be7ff52d3de7ec3738c5cc9d # v4.0.3'
require_line "$ci_workflow" '          predicate-quantifier: some-with-excludes'
require_line "$ci_workflow" '    name: Warm dependency caches'
require_line "$ci_workflow" '      DOCKER_BUILD_RECORD_UPLOAD: "false"'
require_line "$ci_workflow" '        run: scripts/check-runtime-image-platforms.sh'
require_line "$codeql_workflow" '    name: CodeQL / gate'
require_line "$codeql_workflow" '  group: codeql-${{ github.workflow }}-${{ github.ref }}'
require_line "$codeql_workflow" "  cancel-in-progress: \${{ github.event_name == 'pull_request' }}"

if grep -Fq 'cache-to: type=gha' "$ci_workflow"; then
  echo 'Pull-request image builds must not write GitHub Actions caches.' >&2
  exit 1
fi
if printf '%s\n' "$container_filter" | grep -Fq "              - 'web/**'"; then
  echo 'Ordinary frontend source changes must not trigger both image builds.' >&2
  exit 1
fi
if ! printf '%s\n' "$container_filter" | grep -Fq "              - 'web/package-lock.json'"; then
  echo 'Container dependency changes must trigger both image builds.' >&2
  exit 1
fi
if grep -Fq 'runtime-image-platforms:' "$ci_workflow"; then
  echo 'Runtime image validation should share the deployment runner.' >&2
  exit 1
fi
for workflow in "$ci_workflow" "$codeql_workflow"; do
  if ! grep -Fq 'timeout-minutes:' "$workflow"; then
    echo "$workflow has no job timeouts." >&2
    exit 1
  fi
done

echo "CI workflow policy test passed"
