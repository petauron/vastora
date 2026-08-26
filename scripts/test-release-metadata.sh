#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-release-metadata-test.XXXXXX")"
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT HUP INT TERM

git -C "$temporary_dir" init -q
git -C "$temporary_dir" config user.name test
git -C "$temporary_dir" config user.email test@example.invalid
printf '%s\n' '0.1.0-alpha.15' > "$temporary_dir/version.txt"
printf '%s\n' '{".":"0.1.0-alpha.15"}' > "$temporary_dir/.release-please-manifest.json"
printf '%s\n' '# Changelog' '## [0.1.0-alpha.15](https://example.invalid)' > "$temporary_dir/CHANGELOG.md"
git -C "$temporary_dir" add -- .release-please-manifest.json CHANGELOG.md version.txt
git -C "$temporary_dir" commit -qm baseline
base_sha="$(git -C "$temporary_dir" rev-parse HEAD)"

assert_compare() {
  actual="$(awk -f "$script_dir/compare-semver.awk" "$1" "$2")"
  if [ "$actual" != "$3" ]; then
    echo "Unexpected SemVer ordering for $1 and $2: $actual" >&2
    exit 1
  fi
}

assert_compare 0.1.0-alpha.9 0.1.0-alpha.10 -1
assert_compare 0.1.0-alpha.10 0.1.0-alpha.10 0
assert_compare 1.0.0-alpha 1.0.0 -1
assert_compare 2.0.0 1.99.99 1

printf '%s\n' '0.1.0-alpha.16' > "$temporary_dir/version.txt"
printf '%s\n' '{".":"0.1.0-alpha.16"}' > "$temporary_dir/.release-please-manifest.json"
printf '%s\n' '# Changelog' '## [0.1.0-alpha.16](https://example.invalid)' '## [0.1.0-alpha.15](https://example.invalid)' > "$temporary_dir/CHANGELOG.md"
"$script_dir/validate-release-metadata.sh" "$base_sha" "$temporary_dir" >/dev/null

printf '%s\n' 'unexpected source change' > "$temporary_dir/unexpected.txt"
git -C "$temporary_dir" add -- unexpected.txt
if "$script_dir/validate-release-metadata.sh" "$base_sha" "$temporary_dir" >/dev/null 2>&1; then
  echo "Release metadata validator accepted an unexpected file." >&2
  exit 1
fi
rm "$temporary_dir/unexpected.txt"
git -C "$temporary_dir" reset -q -- unexpected.txt

printf '%s\n' '{".":"0.1.0-alpha.99"}' > "$temporary_dir/.release-please-manifest.json"
if "$script_dir/validate-release-metadata.sh" "$base_sha" "$temporary_dir" >/dev/null 2>&1; then
  echo "Release metadata validator accepted mismatched versions." >&2
  exit 1
fi

printf '%s\n' '0.1.0-alpha.14' > "$temporary_dir/version.txt"
printf '%s\n' '{".":"0.1.0-alpha.14"}' > "$temporary_dir/.release-please-manifest.json"
printf '%s\n' '# Changelog' '## [0.1.0-alpha.14](https://example.invalid)' > "$temporary_dir/CHANGELOG.md"
if "$script_dir/validate-release-metadata.sh" "$base_sha" "$temporary_dir" >/dev/null 2>&1; then
  echo "Release metadata validator accepted a version downgrade." >&2
  exit 1
fi

echo "Release metadata validator test passed"
