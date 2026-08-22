#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
workflow="$project_dir/.github/workflows/release.yml"
prepare_job="$(sed -n '/^  prepare:/,/^  publish:/p' "$workflow")"

require_line() {
  if ! printf '%s\n' "$prepare_job" | grep -Fq "$1"; then
    echo "Release workflow is missing: $1" >&2
    exit 1
  fi
}

require_line '      actions: write'
require_line '      - name: Run trusted checks for release pull request'
require_line '          GH_TOKEN: ${{ github.token }}'
require_line '          HEAD_BRANCH: ${{ steps.release_pr.outputs.head_branch }}'
require_line '          gh workflow run ci.yml --repo "$GITHUB_REPOSITORY" --ref "$HEAD_BRANCH"'
require_line '          gh workflow run codeql.yml --repo "$GITHUB_REPOSITORY" --ref "$HEAD_BRANCH"'

echo "Release workflow dispatch test passed"
