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
workers="${WORKERS:-1}"
case "$shard" in ''|*[!0-9]*) exit 1 ;; esac
case "$shards" in ''|*[!0-9]*) exit 1 ;; esac
case "$workers" in ''|*[!0-9]*) exit 1 ;; esac
if [ "$shard" -lt 1 ] || [ "$shard" -gt "$shards" ] || [ "$workers" -lt 1 ]; then exit 1; fi

# Deterministic partitioning covers all top-level tests, examples and fuzz seeds.
binary="$RUNNER_TEMP/center.test"
go test "$@" -c -o "$binary" ./internal/center
"$binary" -test.list '^(Test|Example|Fuzz)' > "$RUNNER_TEMP/center-tests.txt"
tests=$(grep -E '^(Test|Example|Fuzz)[^[:space:]]*$' "$RUNNER_TEMP/center-tests.txt" | LC_ALL=C sort)
test -n "$tests"
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

total_shards=$((shards * workers))
worker_dir=$(mktemp -d "${RUNNER_TEMP}/center-workers.XXXXXX")
cleanup_workers() {
  trap - EXIT HUP INT TERM
  for pid_file in "$worker_dir"/worker-*.pid; do
    if [ -f "$pid_file" ]; then
      pid=$(cat "$pid_file")
      kill "$pid" 2>/dev/null || true
    fi
  done
  wait 2>/dev/null || true
  rm -rf "$worker_dir"
}
trap cleanup_workers EXIT
trap 'cleanup_workers; exit 129' HUP
trap 'cleanup_workers; exit 130' INT
trap 'cleanup_workers; exit 143' TERM

launched=0
w=1
while [ "$w" -le "$workers" ]; do
  # Stride sub-shards across runners so every runner gets an even mix of heavy
  # and light buckets and small synthetic test lists still spread across shards.
  sub_shard=$((shard + (w - 1) * shards))
  selected=$(printf '%s\n' "$tests" | SHARD="$sub_shard" SHARDS="$total_shards" sh "$script_dir/select-center-tests.sh" "$script_dir/center-test-timings.txt")
  if [ -n "$selected" ]; then
    launched=$((launched + 1))
    pattern=$(printf '%s\n' "$selected" | paste -sd '|' -)
    printf '%s\n' "$sub_shard" > "$worker_dir/worker-$w.subshard"
    mkdir -p "$worker_dir/tmp-$w"
    # Match go test's package working directory for testdata and relative paths.
    (
      cd internal/center
      export TMPDIR="$worker_dir/tmp-$w"
      exec "$binary" -test.v -test.count=1 -test.timeout=20m -test.run "^($pattern)$"
    ) > "$worker_dir/worker-$w.log" 2>&1 &
    printf '%s\n' "$!" > "$worker_dir/worker-$w.pid"
  fi
  w=$((w + 1))
done

if [ "$launched" -eq 0 ]; then
  echo "No Center tests selected for shard $shard/$shards (workers=$workers)" >&2
  exit 1
fi

failed=0
w=1
while [ "$w" -le "$workers" ]; do
  if [ -f "$worker_dir/worker-$w.pid" ]; then
    pid=$(cat "$worker_dir/worker-$w.pid")
    sub_shard=$(cat "$worker_dir/worker-$w.subshard")
    worker_status=0
    wait "$pid" || worker_status=$?
    rm -f "$worker_dir/worker-$w.pid"
    echo "=== Center shard $shard/$shards worker $w/$workers (sub-shard $sub_shard/$total_shards) ==="
    cat "$worker_dir/worker-$w.log"
    if [ "$worker_status" -ne 0 ]; then
      echo "Center shard $shard/$shards worker $w/$workers failed with exit status $worker_status" >&2
      failed=1
    fi
  fi
  w=$((w + 1))
done

exit "$failed"
