#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
project_dir="$(CDPATH='' cd -- "$script_dir/.." && pwd)"
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-runtime-network-test.XXXXXX")"
cleanup() { rm -rf "$test_dir"; }
trap cleanup EXIT HUP INT TERM

state_dir="$test_dir/state"
mkdir -p "$state_dir/networks" "$state_dir/aliases"
center_id="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
managed_id="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
unowned_id="cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
install_dir="/opt/vastora/center"

network_file() { printf '%s/networks/%s.%s' "$state_dir" "$1" "$2"; }

create_test_network() {
  name="$1"
  identity="$2"
  printf '%s\n' bridge >"$(network_file "$name" driver)"
  {
    printf '%s\n' 'io.vastora.managed=true' 'io.vastora.component=runtime-network'
    if [ -n "$identity" ]; then printf '%s\n' "io.vastora.network=$identity"; fi
  } >"$(network_file "$name" labels)"
  : >"$(network_file "$name" members)"
}

add_member() {
  name="$1"
  container_id="$2"
  shift 2
  printf '%s\n' "$container_id" >>"$(network_file "$name" members)"
  : >"$state_dir/aliases/$name.$container_id"
  for alias in "$@"; do printf '%s\n' "$alias" >>"$state_dir/aliases/$name.$container_id"; done
}

remove_member() {
  name="$1"
  container_id="$2"
  members="$(network_file "$name" members)"
  temporary="$members.next"
  awk -v id="$container_id" '$0 != id' "$members" >"$temporary"
  mv "$temporary" "$members"
  rm -f "$state_dir/aliases/$name.$container_id"
}

label_value() {
  name="$1"
  key="$2"
  awk -F= -v key="$key" '$1 == key {sub(/^[^=]*=/, ""); print; exit}' "$(network_file "$name" labels)"
}

docker() {
  object="${1:-}"
  action="${2:-}"
  shift 2 || true
  case "$object:$action" in
    network:inspect)
      [ "${1:-}" = "--format" ] || return 2
      format="$2"
      name="$3"
      [ -f "$(network_file "$name" driver)" ] || return 1
      case "$format" in
        '{{.Driver}}') cat "$(network_file "$name" driver)" ;;
        *io.vastora.managed*) label_value "$name" io.vastora.managed ;;
        *io.vastora.component*) label_value "$name" io.vastora.component ;;
        *io.vastora.network*) label_value "$name" io.vastora.network ;;
        *'.Containers'*) cat "$(network_file "$name" members)" ;;
        *) return 2 ;;
      esac
      ;;
    network:create)
      labels_file="$test_dir/create-labels"
      : >"$labels_file"
      name=""
      while [ "$#" -gt 0 ]; do
        case "$1" in
          --driver) driver="$2"; shift 2 ;;
          --label) printf '%s\n' "$2" >>"$labels_file"; shift 2 ;;
          *) name="$1"; shift ;;
        esac
      done
      [ -n "$name" ] || return 2
      [ ! -f "$(network_file "$name" driver)" ] || return 1
      printf '%s\n' "${driver:-bridge}" >"$(network_file "$name" driver)"
      cp "$labels_file" "$(network_file "$name" labels)"
      : >"$(network_file "$name" members)"
      ;;
    network:connect)
      aliases_file="$test_dir/connect-aliases"
      : >"$aliases_file"
      while [ "$#" -gt 2 ]; do
        [ "$1" = "--alias" ] || return 2
        printf '%s\n' "$2" >>"$aliases_file"
        shift 2
      done
      name="$1"
      container_id="$2"
      if [ -f "$state_dir/fail-next-current-connect" ] &&
         [ "$name" = "vastora-runtime" ] &&
         [ "$(label_value "$name" io.vastora.network)" = "vastora-runtime" ]; then
        rm -f "$state_dir/fail-next-current-connect"
        return 1
      fi
      grep -Fqx "$container_id" "$(network_file "$name" members)" 2>/dev/null && return 1
      printf '%s\n' "$container_id" >>"$(network_file "$name" members)"
      cp "$aliases_file" "$state_dir/aliases/$name.$container_id"
      ;;
    network:disconnect)
      if [ "${1:-}" = "-f" ]; then shift; fi
      name="$1"
      container_id="$2"
      grep -Fqx "$container_id" "$(network_file "$name" members)" || return 1
      remove_member "$name" "$container_id"
      ;;
    network:rm)
      name="$1"
      [ ! -s "$(network_file "$name" members)" ] || return 1
      rm -f "$(network_file "$name" driver)" "$(network_file "$name" labels)" "$(network_file "$name" members)"
      ;;
    container:inspect)
      [ "${1:-}" = "--format" ] || return 2
      format="$2"
      container_id="$3"
      case "$format" in
        *io.vastora.managed*)
          if [ "$container_id" = "$managed_id" ] || { [ -f "$state_dir/center-current" ] && [ "$container_id" = "$center_id" ]; }; then printf '%s\n' true; fi
          ;;
        *io.vastora.component*)
          if [ "$container_id" = "$managed_id" ]; then printf '%s\n' center-headscale
          elif [ -f "$state_dir/center-current" ] && [ "$container_id" = "$center_id" ]; then printf '%s\n' center; fi
          ;;
        *com.docker.compose.project.working_dir*) [ "$container_id" = "$center_id" ] && printf '%s\n' "$install_dir" ;;
        *com.docker.compose.project.config_files*) [ "$container_id" = "$center_id" ] && printf '%s\n' "$install_dir/compose.yaml" ;;
        *com.docker.compose.project*) [ "$container_id" = "$center_id" ] && printf '%s\n' vastora ;;
        *com.docker.compose.service*) [ "$container_id" = "$center_id" ] && printf '%s\n' center ;;
        *'.IPAMConfig'*) printf '%s\n' null ;;
        *'.Links'*) printf '%s\n' null ;;
        *'.DriverOpts'*) printf '%s\n' null ;;
        *'.Aliases'*) cat "$state_dir/aliases/vastora-runtime.$container_id" ;;
        *) return 2 ;;
      esac
      ;;
    *) return 2 ;;
  esac
}

# shellcheck source=../deploy/center/runtime-network.sh
. "$project_dir/deploy/center/runtime-network.sh"

create_test_network vastora-runtime ""
add_member vastora-runtime "$center_id" vastora-center-1 center vastora-center
add_member vastora-runtime "$managed_id" vastora-center-headscale
migrate_legacy_vastora_runtime_network "$install_dir" >/dev/null
[ "$(label_value vastora-runtime io.vastora.network)" = vastora-runtime ]
grep -Fqx "$center_id" "$(network_file vastora-runtime members)"
grep -Fqx "$managed_id" "$(network_file vastora-runtime members)"
grep -Fqx vastora-center "$state_dir/aliases/vastora-runtime.$center_id"
test -z "$(find "$state_dir/networks" -name 'vastora-runtime-migration-*' -print -quit)"
ensure_vastora_runtime_network_for_upgrade "$install_dir"
if validate_vastora_runtime_network >/dev/null 2>&1; then
  echo "Strict network validation accepted a legacy unlabeled Center container." >&2
  exit 1
fi
touch "$state_dir/center-current"
validate_vastora_runtime_network
rm -f "$state_dir/center-current"

rm -rf "$state_dir/networks" "$state_dir/aliases"
mkdir -p "$state_dir/networks" "$state_dir/aliases"
create_test_network vastora-runtime ""
add_member vastora-runtime "$unowned_id" unrelated
if migrate_legacy_vastora_runtime_network "$install_dir" >/dev/null 2>&1; then
  echo "Legacy migration accepted an unowned attached container." >&2
  exit 1
fi
[ -z "$(label_value vastora-runtime io.vastora.network)" ]
grep -Fqx "$unowned_id" "$(network_file vastora-runtime members)"

rm -rf "$state_dir/networks" "$state_dir/aliases"
mkdir -p "$state_dir/networks" "$state_dir/aliases"
create_test_network vastora-runtime ""
add_member vastora-runtime "$center_id" vastora-center-1 center vastora-center
add_member vastora-runtime "$managed_id" vastora-center-headscale
touch "$state_dir/fail-next-current-connect"
if migrate_legacy_vastora_runtime_network "$install_dir" >/dev/null 2>&1; then
  echo "Legacy migration did not report a failed final reconnect." >&2
  exit 1
fi
[ -z "$(label_value vastora-runtime io.vastora.network)" ]
grep -Fqx "$center_id" "$(network_file vastora-runtime members)"
grep -Fqx "$managed_id" "$(network_file vastora-runtime members)"
test -z "$(find "$state_dir/networks" -name 'vastora-runtime-migration-*' -print -quit)"

echo "Runtime network migration test passed"
