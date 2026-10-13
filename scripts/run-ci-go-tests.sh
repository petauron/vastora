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

shard="${SHARD:?set SHARD}"
shards="${SHARDS:?set SHARDS}"
case "$shard:$shards" in *[!0-9:]*|:*|*:) exit 1 ;; esac
[ "$shard" -ge 1 ] && [ "$shard" -le "$shards" ]

# Deterministic partitioning covers all top-level tests, examples and fuzz seeds.
go test "$@" ./internal/center -list '^(Test|Example|Fuzz)' > "$RUNNER_TEMP/center-tests.txt"
tests=$(grep -E '^(Test|Example|Fuzz)[^[:space:]]*$' "$RUNNER_TEMP/center-tests.txt" | LC_ALL=C sort)
selected=$(printf '%s\n' "$tests" | awk -v shard="$shard" -v shards="$shards" '(NR - 1) % shards == shard - 1')
test -n "$selected"
pattern=$(printf '%s\n' "$selected" | paste -sd '|' -)
go test "$@" -timeout=20m ./internal/center -run "^($pattern)$"
if [ "$shard" -eq 1 ]; then
  packages=$(go list ./cmd/... ./internal/... | grep -v '^github.com/petauron/vastora/internal/center$')
  test -n "$packages"
  # Word splitting here is intentional: go list emits package paths.
  go test "$@" -timeout=20m $packages
fi
