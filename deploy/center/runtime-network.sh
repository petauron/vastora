#!/bin/sh

vastora_runtime_network="vastora-runtime"

runtime_network_driver() {
  docker network inspect --format '{{.Driver}}' "$1" 2>/dev/null || true
}

runtime_network_label() {
  docker network inspect --format "{{ index .Labels \"$2\" }}" "$1" 2>/dev/null || true
}

runtime_network_container_ids() {
  docker network inspect --format '{{range $id, $_ := .Containers}}{{$id}}{{"\n"}}{{end}}' "$1" 2>/dev/null || true
}

runtime_network_has_current_identity() {
  [ "$(runtime_network_driver "$vastora_runtime_network")" = "bridge" ] &&
    [ "$(runtime_network_label "$vastora_runtime_network" io.vastora.managed)" = "true" ] &&
    [ "$(runtime_network_label "$vastora_runtime_network" io.vastora.component)" = "runtime-network" ] &&
    [ "$(runtime_network_label "$vastora_runtime_network" io.vastora.network)" = "$vastora_runtime_network" ]
}

container_has_vastora_ownership() {
  [ "$(docker container inspect --format '{{ index .Config.Labels "io.vastora.managed" }}' "$1" 2>/dev/null || true)" = "true" ] &&
    [ -n "$(docker container inspect --format '{{ index .Config.Labels "io.vastora.component" }}' "$1" 2>/dev/null || true)" ]
}

container_is_legacy_center_service() {
  vrn_container_id="$1"
  vrn_install_dir="$2"
  vrn_project="$(docker container inspect --format '{{ index .Config.Labels "com.docker.compose.project" }}' "$vrn_container_id" 2>/dev/null || true)"
  vrn_service="$(docker container inspect --format '{{ index .Config.Labels "com.docker.compose.service" }}' "$vrn_container_id" 2>/dev/null || true)"
  vrn_working_dir="$(docker container inspect --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' "$vrn_container_id" 2>/dev/null || true)"
  vrn_config_files="$(docker container inspect --format '{{ index .Config.Labels "com.docker.compose.project.config_files" }}' "$vrn_container_id" 2>/dev/null || true)"
  [ "$vrn_project" = "vastora" ] &&
    { [ "$vrn_service" = "center" ] || [ "$vrn_service" = "deployer" ]; } &&
    [ "$vrn_working_dir" = "$vrn_install_dir" ] &&
    [ "$vrn_config_files" = "$vrn_install_dir/compose.yaml" ]
}

validate_runtime_network_containers() {
  vrn_install_dir="${1:-}"
  vrn_allow_legacy="${2:-no}"
  vrn_container_ids="$(runtime_network_container_ids "$vastora_runtime_network")"
  for vrn_container_id in $vrn_container_ids; do
    case "$vrn_container_id" in
      ''|*[!0-9a-f]*)
        echo "Refusing Docker network $vastora_runtime_network because it contains an invalid container identity." >&2
        return 1
        ;;
    esac
    if container_has_vastora_ownership "$vrn_container_id"; then
      continue
    fi
    if [ "$vrn_allow_legacy" = "yes" ] && container_is_legacy_center_service "$vrn_container_id" "$vrn_install_dir"; then
      continue
    fi
    echo "Refusing to use Docker network $vastora_runtime_network because container $vrn_container_id is not owned by Vastora." >&2
    return 1
  done
}

validate_vastora_runtime_network() {
  if ! runtime_network_has_current_identity; then
    echo "Refusing to use Docker network $vastora_runtime_network because its driver or ownership labels do not match Vastora." >&2
    return 1
  fi
  validate_runtime_network_containers "" no
}

create_vastora_runtime_network() {
  docker network create \
    --driver bridge \
    --label io.vastora.managed=true \
    --label io.vastora.component=runtime-network \
    --label io.vastora.network="$vastora_runtime_network" \
    "$vastora_runtime_network" >/dev/null
}

ensure_vastora_runtime_network() {
  vrn_driver="$(runtime_network_driver "$vastora_runtime_network")"
  if [ -n "$vrn_driver" ]; then
    validate_vastora_runtime_network
    return $?
  fi

  if create_vastora_runtime_network; then
    return 0
  fi
  if validate_vastora_runtime_network; then
    return 0
  fi
  echo "Could not create the shared Docker bridge network $vastora_runtime_network." >&2
  return 1
}

ensure_vastora_runtime_network_for_upgrade() {
  vrn_install_dir="$1"
  vrn_driver="$(runtime_network_driver "$vastora_runtime_network")"
  if [ -z "$vrn_driver" ]; then
    if ! create_vastora_runtime_network && ! runtime_network_has_current_identity; then
      echo "Could not create the shared Docker bridge network $vastora_runtime_network." >&2
      return 1
    fi
  elif ! runtime_network_has_current_identity; then
    echo "Refusing to upgrade with Docker network $vastora_runtime_network because its ownership identity is invalid." >&2
    return 1
  fi
  validate_runtime_network_containers "$vrn_install_dir" yes
}

capture_runtime_network_endpoint() {
  vrn_container_id="$1"
  vrn_migration_dir="$2"
  vrn_ipam="$(docker container inspect --format "{{json (index .NetworkSettings.Networks \"$vastora_runtime_network\").IPAMConfig}}" "$vrn_container_id" 2>/dev/null || true)"
  vrn_links="$(docker container inspect --format "{{json (index .NetworkSettings.Networks \"$vastora_runtime_network\").Links}}" "$vrn_container_id" 2>/dev/null || true)"
  vrn_driver_options="$(docker container inspect --format "{{json (index .NetworkSettings.Networks \"$vastora_runtime_network\").DriverOpts}}" "$vrn_container_id" 2>/dev/null || true)"
  case "$vrn_ipam:$vrn_links:$vrn_driver_options" in
    null:null:null|'{}':null:null) ;;
    *)
      echo "Refusing to migrate Docker network $vastora_runtime_network because container $vrn_container_id uses custom endpoint settings." >&2
      return 1
      ;;
  esac
  docker container inspect \
    --format "{{range (index .NetworkSettings.Networks \"$vastora_runtime_network\").Aliases}}{{println .}}{{end}}" \
    "$vrn_container_id" >"$vrn_migration_dir/$vrn_container_id.aliases"
}

connect_runtime_network_endpoint() {
  vrn_network_name="$1"
  vrn_container_id="$2"
  vrn_aliases_file="$3"
  set -- docker network connect
  while IFS= read -r alias; do
    if [ -n "$alias" ]; then
      set -- "$@" --alias "$alias"
    fi
  done <"$vrn_aliases_file"
  "$@" "$vrn_network_name" "$vrn_container_id"
}

restore_legacy_runtime_network() {
  vrn_migration_dir="$1"
  vrn_temporary_network="$2"
  vrn_container_ids="$3"

  for vrn_container_id in $vrn_container_ids; do
    docker network disconnect -f "$vastora_runtime_network" "$vrn_container_id" >/dev/null 2>&1 || true
  done
  docker network rm "$vastora_runtime_network" >/dev/null 2>&1 || true
  if ! docker network create \
    --driver bridge \
    --label io.vastora.managed=true \
    --label io.vastora.component=runtime-network \
    "$vastora_runtime_network" >/dev/null; then
    echo "Automatic rollback could not recreate $vastora_runtime_network; containers remain connected to $vrn_temporary_network." >&2
    return 1
  fi
  for vrn_container_id in $vrn_container_ids; do
    if ! connect_runtime_network_endpoint "$vastora_runtime_network" "$vrn_container_id" "$vrn_migration_dir/$vrn_container_id.aliases"; then
      echo "Automatic rollback could not reconnect container $vrn_container_id; it remains connected to $vrn_temporary_network." >&2
      return 1
    fi
  done
  for vrn_container_id in $vrn_container_ids; do
    docker network disconnect -f "$vrn_temporary_network" "$vrn_container_id" >/dev/null 2>&1 || true
  done
  docker network rm "$vrn_temporary_network" >/dev/null 2>&1 || true
}

migrate_legacy_vastora_runtime_network() {
  vrn_install_dir="$1"
  vrn_driver="$(runtime_network_driver "$vastora_runtime_network")"
  if [ -z "$vrn_driver" ] || runtime_network_has_current_identity; then
    return 0
  fi
  vrn_managed="$(runtime_network_label "$vastora_runtime_network" io.vastora.managed)"
  vrn_component="$(runtime_network_label "$vastora_runtime_network" io.vastora.component)"
  vrn_identity="$(runtime_network_label "$vastora_runtime_network" io.vastora.network)"
  if [ "$vrn_driver" != "bridge" ] || [ "$vrn_managed" != "true" ] || [ "$vrn_component" != "runtime-network" ] || [ -n "$vrn_identity" ]; then
    echo "Refusing to migrate Docker network $vastora_runtime_network because it is not an exact legacy Vastora network." >&2
    return 1
  fi
  if ! validate_runtime_network_containers "$vrn_install_dir" yes; then
    return 1
  fi

  vrn_container_ids="$(runtime_network_container_ids "$vastora_runtime_network")"
  vrn_migration_dir="$(mktemp -d "${TMPDIR:-/tmp}/vastora-runtime-network.XXXXXX")"
  vrn_temporary_network="vastora-runtime-migration-$$"
  for vrn_container_id in $vrn_container_ids; do
    if ! capture_runtime_network_endpoint "$vrn_container_id" "$vrn_migration_dir"; then
      rm -rf "$vrn_migration_dir"
      return 1
    fi
  done
  if ! docker network create \
    --driver bridge \
    --label io.vastora.managed=true \
    --label io.vastora.component=runtime-network-migration \
    --label io.vastora.network="$vrn_temporary_network" \
    "$vrn_temporary_network" >/dev/null; then
    rm -rf "$vrn_migration_dir"
    echo "Could not create the temporary network required to migrate $vastora_runtime_network." >&2
    return 1
  fi

  for vrn_container_id in $vrn_container_ids; do
    if ! connect_runtime_network_endpoint "$vrn_temporary_network" "$vrn_container_id" "$vrn_migration_dir/$vrn_container_id.aliases"; then
      for vrn_cleanup_id in $vrn_container_ids; do
        docker network disconnect -f "$vrn_temporary_network" "$vrn_cleanup_id" >/dev/null 2>&1 || true
      done
      docker network rm "$vrn_temporary_network" >/dev/null 2>&1 || true
      rm -rf "$vrn_migration_dir"
      echo "Could not attach every Vastora container to the temporary migration network." >&2
      return 1
    fi
  done

  vrn_migration_failed=no
  for vrn_container_id in $vrn_container_ids; do
    if ! docker network disconnect "$vastora_runtime_network" "$vrn_container_id" >/dev/null; then
      vrn_migration_failed=yes
      break
    fi
  done
  if [ "$vrn_migration_failed" = no ] && ! docker network rm "$vastora_runtime_network" >/dev/null; then
    vrn_migration_failed=yes
  fi
  if [ "$vrn_migration_failed" = no ] && ! create_vastora_runtime_network; then
    vrn_migration_failed=yes
  fi
  if [ "$vrn_migration_failed" = no ]; then
    for vrn_container_id in $vrn_container_ids; do
      if ! connect_runtime_network_endpoint "$vastora_runtime_network" "$vrn_container_id" "$vrn_migration_dir/$vrn_container_id.aliases"; then
        vrn_migration_failed=yes
        break
      fi
    done
  fi
  if [ "$vrn_migration_failed" = yes ]; then
    restore_legacy_runtime_network "$vrn_migration_dir" "$vrn_temporary_network" "$vrn_container_ids" || true
    rm -rf "$vrn_migration_dir"
    echo "The legacy $vastora_runtime_network migration failed and the upgrade was stopped." >&2
    return 1
  fi

  for vrn_container_id in $vrn_container_ids; do
    docker network disconnect -f "$vrn_temporary_network" "$vrn_container_id" >/dev/null
  done
  docker network rm "$vrn_temporary_network" >/dev/null
  rm -rf "$vrn_migration_dir"
  echo "Migrated the legacy $vastora_runtime_network network to the current ownership contract."
}
