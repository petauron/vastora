#!/bin/sh
set -eu

command_name="${1:-}"
if [ "$#" -gt 0 ]; then shift; fi
tag=""
directory="dist"

usage() {
  cat <<'EOF'
Usage: github-installer-release-assets.sh upload --tag TAG [--directory DIR]
       github-installer-release-assets.sh download --tag TAG [--directory DIR]

Uploads or downloads the exact four historical installer assets while keeping
the GitHub Release in draft state, then verifies every asset digest.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 ;;
    --directory) directory="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
case "$command_name" in upload|download) ;; *) usage >&2; exit 2 ;; esac
if ! printf '%s' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "Release tag must be valid Vastora SemVer." >&2
  exit 2
fi
for required in awk cmp find gh grep jq mkdir mktemp sha256sum; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done

expected_assets='["install.sh","vastora-center-install.tar.gz","vastora-center-install.tar.gz.sha256","vastora-release-manifest.json"]'
release_json="$(gh release view "$tag" --json isDraft,assets)"
if [ "$(printf '%s' "$release_json" | jq -r '.isDraft')" != "true" ]; then
  echo "GitHub Release $tag is not a draft; refusing installer asset mutation." >&2
  exit 1
fi
if ! printf '%s' "$release_json" | jq -e --argjson expected "$expected_assets" '([.assets[].name] - $expected) == []' >/dev/null; then
  echo "GitHub Release $tag contains an unexpected asset; refusing to replace it." >&2
  exit 1
fi

validate_assets() {
  asset_dir="$1"
  manifest="$asset_dir/vastora-release-manifest.json"
  for asset in install.sh vastora-center-install.tar.gz vastora-center-install.tar.gz.sha256 vastora-release-manifest.json; do
    if [ ! -f "$asset_dir/$asset" ]; then
      echo "GitHub installer release asset is missing: $asset" >&2
      return 1
    fi
  done
  version="${tag#v}"
  prefix="vastora/releases/$tag/"
  if ! jq -e --arg version "$version" --arg prefix "$prefix" '
    (.schema == 1) and (.version == $version) and
    ((.assets | keys | sort) == ["install.sh", "vastora-center-install.tar.gz", "vastora-center-install.tar.gz.sha256"]) and
    (.assets["install.sh"].key == ($prefix + "install.sh")) and
    (.assets["vastora-center-install.tar.gz"].key == ($prefix + "vastora-center-install.tar.gz")) and
    (.assets["vastora-center-install.tar.gz.sha256"].key == ($prefix + "vastora-center-install.tar.gz.sha256")) and
    (all(.assets[]; .sha256 | test("^[0-9a-f]{64}$")))
  ' "$manifest" >/dev/null; then
    echo "GitHub installer release manifest is invalid." >&2
    return 1
  fi
  for asset in install.sh vastora-center-install.tar.gz vastora-center-install.tar.gz.sha256; do
    expected="$(jq -r --arg asset "$asset" '.assets[$asset].sha256' "$manifest")"
    actual="$(sha256sum "$asset_dir/$asset" | awk 'NR == 1 {print tolower($1)}')"
    if [ "$actual" != "$expected" ]; then
      echo "GitHub installer release asset digest mismatch: $asset" >&2
      return 1
    fi
  done
}

if [ "$command_name" = "download" ]; then
  mkdir -p "$directory"
  if find "$directory" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
    echo "Download directory must be empty: $directory" >&2
    exit 1
  fi
  gh release download "$tag" --dir "$directory"
  validate_assets "$directory"
  echo "Downloaded and verified GitHub installer assets for $tag."
  exit 0
fi

validate_assets "$directory"
gh release upload "$tag" \
  "$directory/install.sh" \
  "$directory/vastora-center-install.tar.gz" \
  "$directory/vastora-center-install.tar.gz.sha256" \
  "$directory/vastora-release-manifest.json" \
  --clobber

temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-github-release.XXXXXX")"
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM
gh release download "$tag" --dir "$temporary_dir"
validate_assets "$temporary_dir"
for asset in install.sh vastora-center-install.tar.gz vastora-center-install.tar.gz.sha256 vastora-release-manifest.json; do
  if ! cmp -s "$directory/$asset" "$temporary_dir/$asset"; then
    echo "Downloaded GitHub Release asset differs from the uploaded file: $asset" >&2
    exit 1
  fi
done
release_json="$(gh release view "$tag" --json isDraft,assets)"
if ! printf '%s' "$release_json" | jq -e --argjson expected "$expected_assets" '.isDraft == true and ([.assets[].name] | sort) == ($expected | sort)' >/dev/null; then
  echo "GitHub Release does not contain exactly the verified installer assets in draft state." >&2
  exit 1
fi
echo "Uploaded and verified GitHub installer assets for $tag."
