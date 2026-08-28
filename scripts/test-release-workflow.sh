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
require_in "$prepare_job" '          release_sha="$(gh api "/repos/$GITHUB_REPOSITORY/commits/$RELEASE_TAG"'
require_in "$prepare_job" "      release_retry: \${{ steps.retry.outputs.release_retry || 'false' }}"
require_in "$prepare_job" "            echo 'release_retry=true'"
require_in "$publish_job" '      artifact-metadata: write'
require_in "$publish_job" '      AWS_ACCESS_KEY_ID: ${{ secrets.R2_ACCESS_KEY_ID }}'
require_in "$publish_job" '      R2_BUCKET_NAME: ${{ vars.R2_BUCKET_NAME }}'
require_in "$publish_job" '      - name: Validate R2 release destination'
require_in "$publish_job" '      - name: Check out current release tooling'
require_in "$publish_job" '          path: .release-tools'
require_in "$publish_job" '            scripts/verify-installer-release.sh'
require_in "$publish_job" '          platforms: linux/amd64,linux/arm64'
require_in "$publish_job" '          outputs: type=image,name=${{ env.CENTER_IMAGE }},push-by-digest=true,name-canonical=true,push=true'
require_in "$publish_job" '          DOCKER_CONFIG="$anonymous_config" scripts/assert-image-platforms.sh "$VASTORA_CENTER_IMAGE"'
require_in "$publish_job" '          TRIVY_PLATFORM: linux/amd64'
require_in "$publish_job" '          TRIVY_PLATFORM: linux/arm64'
require_in "$publish_job" '      - name: Publish verified Center image tags'
require_in "$publish_job" '            --tag "$CENTER_IMAGE:$RELEASE_TAG"'
require_in "$publish_job" '            --tag "$CENTER_IMAGE:latest"'
require_in "$publish_job" '        run: .release-tools/scripts/publish-installer-r2.sh stage --version "$RELEASE_VERSION" --bucket "$R2_BUCKET_NAME" --endpoint "$R2_ENDPOINT" --installer install.sh'
require_in "$publish_job" '      - name: Verify immutable installer release'
require_in "$publish_job" '        run: .release-tools/scripts/verify-installer-release.sh --base-url "https://vastora.petauron.com/releases/v$RELEASE_VERSION" --expected-version "$RELEASE_VERSION"'
require_in "$publish_job" '        run: gh release edit "$RELEASE_TAG" --draft=false'
require_in "$publish_job" '        run: .release-tools/scripts/publish-installer-r2.sh activate --version "$RELEASE_VERSION" --bucket "$R2_BUCKET_NAME" --endpoint "$R2_ENDPOINT"'
require_in "$publish_job" '        run: .release-tools/scripts/verify-installer-release.sh --base-url https://vastora.petauron.com --expected-version "$EXPECTED_VERSION" --attempts 18 --retry-delay 5'
require_in "$release_pr_job" '    needs: [prepare, publish]'
require_in "$release_pr_job" "    if: always() && needs.prepare.result == 'success' && (needs.publish.result == 'success' || needs.publish.result == 'skipped')"
require_in "$release_pr_job" '          skip-github-release: true'
require_in "$release_pr_job" "            echo \"ci_id=\$(start_check 'CI / gate')\""
require_in "$release_pr_job" "            echo \"codeql_id=\$(start_check 'CodeQL / gate')\""
require_in "$release_pr_job" '        run: scripts/validate-release-metadata.sh "$BASE_SHA"'

require_fresh_release_step() {
  step_name="$1"
  step_block="$(printf '%s\n' "$publish_job" | sed -n "/^      - name: $step_name$/,/^      - name:/p")"
  require_in "$step_block" "        if: needs.prepare.outputs.release_retry != 'true'"
}

require_fresh_release_step 'Check out release commit'
require_fresh_release_step 'Set up Docker Buildx'
require_fresh_release_step 'Log in to GitHub Container Registry'
require_fresh_release_step 'Build and push Center image'
require_fresh_release_step 'Scan released Center image for x64 vulnerabilities'
require_fresh_release_step 'Scan released Center image for ARM64 vulnerabilities'
require_fresh_release_step 'Publish verified Center image tags'
require_fresh_release_step 'Attest Center image'
require_fresh_release_step 'Package release installer'
require_fresh_release_step 'Verify release assets and public image access'
require_fresh_release_step 'Stage immutable installer assets in R2'

if printf '%s\n' "$prepare_job" | grep -Fq 'skip-github-release: true'; then
  echo 'Release creation must not update the next release pull request.' >&2
  exit 1
fi
if printf '%s\n' "$release_pr_job" | grep -Fq 'skip-github-pull-request: true'; then
  echo 'Release pull request preparation must not publish a release.' >&2
  exit 1
fi
if printf '%s\n' "$publish_job" | grep -Fq 'gh release upload'; then
  echo 'Installer assets must be published to R2, not uploaded to GitHub Release.' >&2
  exit 1
fi
if printf '%s\n' "$publish_job" | grep -Fq '          tags:'; then
  echo 'Official image tags must be promoted only after both architecture scans pass.' >&2
  exit 1
fi

scan_line="$(printf '%s\n' "$publish_job" | grep -nF 'Scan released Center image for ARM64 vulnerabilities' | cut -d: -f1)"
promote_line="$(printf '%s\n' "$publish_job" | grep -nF 'Publish verified Center image tags' | cut -d: -f1)"
stage_line="$(printf '%s\n' "$publish_job" | grep -nF 'publish-installer-r2.sh stage' | cut -d: -f1)"
immutable_verify_line="$(printf '%s\n' "$publish_job" | grep -nF 'Verify immutable installer release' | cut -d: -f1)"
publish_line="$(printf '%s\n' "$publish_job" | grep -nF 'gh release edit "$RELEASE_TAG" --draft=false' | cut -d: -f1)"
activate_line="$(printf '%s\n' "$publish_job" | grep -nF 'publish-installer-r2.sh activate' | cut -d: -f1)"
verify_line="$(printf '%s\n' "$publish_job" | grep -nF 'Verify public installer endpoint' | cut -d: -f1)"
if [ "$scan_line" -ge "$promote_line" ] || [ "$promote_line" -ge "$stage_line" ] || [ "$stage_line" -ge "$publish_line" ] || [ "$publish_line" -ge "$activate_line" ] || [ "$activate_line" -ge "$immutable_verify_line" ] || [ "$immutable_verify_line" -ge "$verify_line" ]; then
  echo 'Release workflow must stage, publish metadata, activate R2, then verify the public endpoint.' >&2
  exit 1
fi

echo "Release workflow sequencing test passed"
