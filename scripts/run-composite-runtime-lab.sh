#!/bin/sh
# Explicit opt-in only. Never execute this against a production namespace.
set -eu

if [ "$#" -ne 1 ]; then
  printf '%s\n' 'Usage: scripts/run-composite-runtime-lab.sh /absolute/path/to/official/xray' >&2
  exit 2
fi
core=$1
case "$core" in
  /*) ;;
  *) printf '%s\n' 'An absolute official core path is required.' >&2; exit 2 ;;
esac
test -x "$core"
test "$(uname -s)" = Linux
command -v go >/dev/null
command -v unshare >/dev/null
command -v ip >/dev/null
command -v sudo >/dev/null
lab_dir=$(mktemp -d /tmp/glivela-composite-lab.XXXXXXXX)
cleanup() {
  rm -f "$lab_dir/agent.test"
  rmdir "$lab_dir"
}
trap cleanup EXIT HUP INT TERM

# Module references must already be pinned to the independently reviewed
# Glivela commit, or jointly supplied through an external development go.work.
go test -c -o "$lab_dir/agent.test" ./internal/agent
sudo unshare --net sh -c '
  set -eu
  ip link set lo up
  exec env GLIVELA_COMPOSITE_LAB=1 GLIVELA_XRAY_BINARY="$1" "$2" -test.run="^TestCompositeRuntimeLab$" -test.timeout=90s
' glivela-lab "$core" "$lab_dir/agent.test"
