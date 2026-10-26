#!/bin/sh
set -eu

# Use the checked source version, not a mutable repository setting. All callers
# (PR, release PR, manual and scheduled CI) have the same policy.
version=$(tr -d '\r\n' < version.txt)
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
  echo "Invalid CI version: $version" >&2
  exit 1
fi
set --
case "${version%%+*}" in
  *-alpha|*-alpha.*) echo "Alpha CI: complete ordinary tests; race disabled" ;;
  *) set -- -race; echo "Non-alpha CI: race enabled" ;;
esac

if [ "${SUITE:-center}" = packages ]; then
  packages=$(go list ./cmd/... ./internal/... | grep -v '^github.com/petauron/vastora/internal/center$')
  test -n "$packages"
  # Word splitting here is intentional: go list emits package paths.
  go test "$@" -v -count=1 -timeout=20m $packages
  exit
fi
[ "${SUITE:-center}" = center ]

shard="${SHARD:?set SHARD}"
shards="${SHARDS:?set SHARDS}"
case "$shard:$shards" in *[!0-9:]*|:*|*:) exit 1 ;; esac
if [ "$shard" -lt 1 ] || [ "$shard" -gt "$shards" ]; then exit 1; fi

# Deterministic partitioning covers all top-level tests, examples and fuzz seeds.
binary="$RUNNER_TEMP/center.test"
go test "$@" -c -o "$binary" ./internal/center
"$binary" -test.list '^(Test|Example|Fuzz)' > "$RUNNER_TEMP/center-tests.txt"
tests=$(grep -E '^(Test|Example|Fuzz)[^[:space:]]*$' "$RUNNER_TEMP/center-tests.txt" | LC_ALL=C sort)
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
selected=$(printf '%s\n' "$tests" | sh "$script_dir/select-center-tests.sh" "$script_dir/center-test-timings.txt")
test -n "$selected"
pattern=$(printf '%s\n' "$selected" | paste -sd '|' -)
# Match go test's package working directory for testdata and relative paths.
(cd internal/center && "$binary" -test.v -test.count=1 -test.timeout=20m -test.run "^($pattern)$")
