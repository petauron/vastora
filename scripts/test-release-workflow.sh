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

require_line '      checks: write'
require_line '      - name: Start release metadata check'
require_line '          GH_TOKEN: ${{ github.token }}'
require_line "              -f name='Release metadata'"
require_line '            gh api --method POST "/repos/$GITHUB_REPOSITORY/check-runs"'
require_line '              -f details_url="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID"'
require_line '        id: validate_release_metadata'
require_line '      - name: Finish release metadata check'
require_line '          CHECK_CONCLUSION: ${{ steps.validate_release_metadata.outcome == '\''success'\'' && '\''success'\'' || '\''failure'\'' }}'
require_line '          gh api --method PATCH "/repos/$GITHUB_REPOSITORY/check-runs/$CHECK_ID"'

if printf '%s\n' "$prepare_job" | grep -Fq 'gh workflow run'; then
  echo 'Release metadata pull requests must not duplicate source workflows' >&2
  exit 1
fi

echo "Release metadata check workflow test passed"
