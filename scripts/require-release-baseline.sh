#!/bin/sh
set -eu

# Do not let release-please scan the entire history when a failed draft has
# advanced the manifest but has no published release/tag. Historical Release-As
# footers can otherwise override the next version (alpha.132 -> alpha.43).
version="$(jq -er '.["."] | select(type=="string")' .release-please-manifest.json)"
test "$version" = "$(tr -d '\r\n' < version.txt)" || {
  echo "Release manifest and version.txt disagree." >&2
  exit 1
}
tag="v$version"
if ! release="$(gh release view "$tag" --repo "$GITHUB_REPOSITORY" --json tagName,isDraft)"; then
  echo "Cannot verify release baseline $tag. Finish or repair the failed release before generating another release PR." >&2
  exit 1
fi
printf '%s' "$release" | jq -e --arg tag "$tag" '.tagName==$tag and .isDraft==false' >/dev/null || {
  echo "Release baseline $tag is not public. Finish or explicitly repair the failed release before generating another release PR; refusing a historical commit scan." >&2
  exit 1
}
# A release without its corresponding tag is not an authoritative boundary.
gh api "repos/$GITHUB_REPOSITORY/git/ref/tags/$tag" --jq '.object.sha' >/dev/null
