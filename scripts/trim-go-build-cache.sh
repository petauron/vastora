#!/bin/sh
set -eu

# Only runs inside the Linux build image, after compilation. Keep each
# architecture's newest compiler artifacts within 512 MiB before exporting.
# Go cache filenames are hashes; discarded entries are safely rebuilt by Go.
# Do not include modules, toolchains, npm or Docker layers in this cache.
find /root/.cache/go-build -type f -printf '%T@ %s %p\n' |
  LC_ALL=C sort -k1,1nr |
  awk '{ bytes += $2; if (bytes > 536870912) print $3 }' |
  xargs -r rm --
