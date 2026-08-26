#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
workflow="$project_dir/.github/workflows/release.yml"
prepare_job="$(sed -n '/^  prepare:/,/^  publish:/p' "$workflow")"
publish_job="$(sed -n '/^  publish:/,/^  release-pr:/p' "$workflow")"
release_pr_job="$(sed -n '/^  release-pr:/,$p' "$workflow")"

require_in() {
  section="$1"
  expected="$2"
  if ! printf '%s\n' "$section" | grep -Fq "$expected"; then
    echo "Release workflow is missing: $expected" >&2
    exit 1
  fi
}

require_in "$prepare_job" '          skip-github-pull-request: true'
require_in "$prepare_job" '      pull-requests: write'
require_in "$publish_job" '      artifact-metadata: write'
require_in "$publish_job" '          platforms: linux/amd64,linux/arm64'
require_in "$publish_job" '          DOCKER_CONFIG="$anonymous_config" scripts/assert-image-platforms.sh "$VASTORA_CENTER_IMAGE"'
require_in "$release_pr_job" '    needs: [prepare, publish]'
require_in "$release_pr_job" "    if: always() && needs.prepare.result == 'success' && (needs.publish.result == 'success' || needs.publish.result == 'skipped')"
require_in "$release_pr_job" '          skip-github-release: true'
require_in "$release_pr_job" "            echo \"ci_id=\$(start_check 'CI / gate')\""
require_in "$release_pr_job" "            echo \"codeql_id=\$(start_check 'CodeQL / gate')\""
require_in "$release_pr_job" '        run: scripts/validate-release-metadata.sh "$BASE_SHA"'

if printf '%s\n' "$prepare_job" | grep -Fq 'skip-github-release: true'; then
  echo 'Release creation must not update the next release pull request.' >&2
  exit 1
fi
if printf '%s\n' "$release_pr_job" | grep -Fq 'skip-github-pull-request: true'; then
  echo 'Release pull request preparation must not publish a release.' >&2
  exit 1
fi

echo "Release workflow sequencing test passed"
