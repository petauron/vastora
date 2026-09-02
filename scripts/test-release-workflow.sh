#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
workflow="$project_dir/.github/workflows/release.yml"
release_notes_config="$project_dir/.github/release.yml"
pr_labels_workflow="$project_dir/.github/workflows/pr-release-labels.yml"
reconcile_workflow="$project_dir/.github/workflows/reconcile-installer-r2.yml"
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

require_in "$(cat "$workflow")" '  group: vastora-installer-r2'
require_in "$(cat "$workflow")" '  cancel-in-progress: false'
require_in "$prepare_job" '          skip-github-pull-request: true'
require_in "$prepare_job" '      pull-requests: write'
require_in "$prepare_job" '          release_sha="$(gh api "/repos/$GITHUB_REPOSITORY/commits/$RELEASE_TAG"'
require_in "$prepare_job" "      release_retry: \${{ steps.retry.outputs.release_retry || 'false' }}"
require_in "$publish_job" '      artifact-metadata: write'
require_in "$publish_job" '      AWS_ACCESS_KEY_ID: ${{ secrets.R2_ACCESS_KEY_ID }}'
require_in "$publish_job" '      R2_BUCKET_NAME: ${{ vars.R2_BUCKET_NAME }}'
require_in "$publish_job" '            scripts/create-installer-release-manifest.sh'
require_in "$publish_job" '            scripts/github-installer-release-assets.sh'
require_in "$publish_job" '            scripts/reconcile-installer-r2.sh'
require_in "$publish_job" '          platforms: linux/amd64,linux/arm64'
require_in "$publish_job" '          outputs: type=image,name=${{ env.CENTER_IMAGE }},push-by-digest=true,name-canonical=true,push=true'
require_in "$publish_job" '          provenance: mode=max'
require_in "$publish_job" '          sbom: true'
require_in "$publish_job" '      - name: Create durable installer release manifest'
require_in "$publish_job" '      - name: Upload and verify draft GitHub installer assets'
require_in "$publish_job" '        run: sh .release-tools/scripts/github-installer-release-assets.sh upload --tag "$RELEASE_TAG" --directory dist'
require_in "$publish_job" '      - name: Recover verified installer assets for release retry'
require_in "$publish_job" '        run: sh .release-tools/scripts/publish-installer-r2.sh stage --version "$RELEASE_VERSION" --bucket "$R2_BUCKET_NAME" --endpoint "$R2_ENDPOINT" --source-dir dist'
require_in "$publish_job" '        run: sh .release-tools/scripts/reconcile-installer-r2.sh --bucket "$R2_BUCKET_NAME" --endpoint "$R2_ENDPOINT"'
require_in "$publish_job" '      - name: Generate and publish GitHub release notes'
require_in "$publish_job" '          RELEASE_SHA: ${{ needs.prepare.outputs.release_sha }}'
require_in "$publish_job" '          gh api --method POST "/repos/$GITHUB_REPOSITORY/releases/generate-notes"'
require_in "$publish_job" "            -f configuration_file_path='.github/release.yml'"
require_in "$publish_job" '            --title "Petauron Vastora $RELEASE_TAG"'
require_in "$publish_job" '            --notes-file "$notes_file"'
require_in "$publish_job" '            --draft=false'
require_in "$release_pr_job" '    needs: [prepare, publish]'
require_in "$release_pr_job" "    if: always() && needs.prepare.result == 'success' && (needs.publish.result == 'success' || needs.publish.result == 'skipped')"
require_in "$release_pr_job" '          skip-github-release: true'
require_in "$release_pr_job" '        run: scripts/validate-release-metadata.sh "$BASE_SHA"'

require_fresh_release_step() {
  step_name="$1"
  step_block="$(printf '%s\n' "$publish_job" | sed -n "/^      - name: $step_name$/,/^      - name:/p")"
  require_in "$step_block" "        if: needs.prepare.outputs.release_retry != 'true'"
}

for step in \
  'Set up Docker Buildx' \
  'Log in to GitHub Container Registry' \
  'Build and push Center image' \
  'Scan released Center image for x64 vulnerabilities' \
  'Scan released Center image for ARM64 vulnerabilities' \
  'Publish verified Center image tags' \
  'Attest Center image' \
  'Package release installer' \
  'Verify release assets and public image access' \
  'Create durable installer release manifest' \
  'Upload and verify draft GitHub installer assets'; do
  require_fresh_release_step "$step"
done

for step in \
  'Scan released Center image for x64 vulnerabilities' \
  'Scan released Center image for ARM64 vulnerabilities'; do
  step_block="$(printf '%s\n' "$publish_job" | sed -n "/^      - name: $step$/,/^      - name:/p")"
  require_in "$step_block" "vars.VASTORA_CI_MODE != 'alpha'"
done

if printf '%s\n' "$prepare_job" | grep -Fq 'skip-github-release: true'; then
  echo 'Release creation must not update the next release pull request.' >&2
  exit 1
fi
if printf '%s\n' "$publish_job" | grep -Fq '          tags:'; then
  echo 'Official image tags must be promoted by digest after the applicable Alpha or stable checks.' >&2
  exit 1
fi

line_of() {
  printf '%s\n' "$publish_job" | grep -nF "$1" | head -n 1 | cut -d: -f1
}
scan_line="$(line_of 'Scan released Center image for ARM64 vulnerabilities')"
manifest_line="$(line_of 'Create durable installer release manifest')"
github_assets_line="$(line_of 'Upload and verify draft GitHub installer assets')"
stage_line="$(line_of 'Stage immutable installer assets in R2')"
activate_line="$(line_of 'Atomically activate installer release in R2')"
immutable_verify_line="$(line_of 'Verify immutable installer release')"
public_verify_line="$(line_of 'Verify public installer endpoint')"
prune_line="$(line_of 'Prune stale Vastora installer objects')"
immutable_reverify_line="$(line_of 'Verify immutable installer release after pruning')"
public_reverify_line="$(line_of 'Verify public installer endpoint after pruning')"
publish_line="$(line_of 'Generate and publish GitHub release notes')"
previous=0
for current in "$scan_line" "$manifest_line" "$github_assets_line" "$stage_line" "$activate_line" "$immutable_verify_line" "$public_verify_line" "$prune_line" "$immutable_reverify_line" "$public_reverify_line" "$publish_line"; do
  if [ -z "$current" ] || [ "$current" -le "$previous" ]; then
    echo 'Release workflow does not enforce build -> draft assets -> stage -> activate -> verify -> prune -> verify -> publish.' >&2
    exit 1
  fi
  previous="$current"
done

release_notes_text="$(cat "$release_notes_config")"
require_in "$release_notes_text" '    - title: 安全更新'
require_in "$release_notes_text" '        - security'
require_in "$release_notes_text" '    - title: 新功能'
require_in "$release_notes_text" '        - enhancement'
require_in "$release_notes_text" '    - title: 问题修复'
require_in "$release_notes_text" '        - bug'
require_in "$release_notes_text" '    - title: 维护与其他'
require_in "$release_notes_text" '        - "*"'
require_in "$release_notes_text" '      - "dependabot[bot]"'
require_in "$release_notes_text" '      - "github-actions[bot]"'

pr_labels_text="$(cat "$pr_labels_workflow")"
require_in "$pr_labels_text" '  pull_request_target:'
require_in "$pr_labels_text" '  issues: write'
require_in "$pr_labels_text" '          PR_TITLE: ${{ github.event.pull_request.title }}'
require_in "$pr_labels_text" "              release_label='security'"
require_in "$pr_labels_text" "              release_label='enhancement'"
require_in "$pr_labels_text" "              release_label='bug'"
if printf '%s\n' "$pr_labels_text" | grep -Fq 'actions/checkout'; then
  echo 'The pull_request_target labeler must not check out pull request code.' >&2
  exit 1
fi

reconcile_text="$(cat "$reconcile_workflow")"
require_in "$reconcile_text" '    - cron: "17 3 * * *"'
require_in "$reconcile_text" '  group: vastora-installer-r2'
require_in "$reconcile_text" '  cancel-in-progress: false'
require_in "$reconcile_text" '            --version-out "$RUNNER_TEMP/vastora-active-version"'
require_in "$reconcile_text" '      - name: Verify active installer endpoints'

echo "Release workflow sequencing test passed"
