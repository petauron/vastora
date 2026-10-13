#!/bin/sh
set -eu

# The live Go test list is authoritative. Timings only influence placement:
# new tests always participate, and removed tests cannot be resurrected.
shard="${SHARD:?set SHARD}"
shards="${SHARDS:?set SHARDS}"
case "$shard:$shards" in *[!0-9:]*|:*|*:) exit 1 ;; esac
if [ "$shard" -lt 1 ] || [ "$shard" -gt "$shards" ]; then exit 1; fi
weights="${1:?timing file required}"
awk '
  FILENAME == ARGV[1] { if ($1 !~ /^#/ && NF == 2) weight[$1] = $2; next }
  /^(Test|Example|Fuzz)[^[:space:]]*$/ {
    if (!seen[$0]++) print (($0 in weight) ? weight[$0] : 0.1), $0
  }
' "$weights" - | LC_ALL=C sort -k1,1nr -k2,2 | awk -v shard="$shard" -v shards="$shards" '
  {
    target = 1
    for (i = 2; i <= shards; i++) if (load[i] < load[target]) target = i
    load[target] += $1
    if (target == shard) print $2
  }
'
