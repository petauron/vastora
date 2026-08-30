#!/bin/sh

vastora_runtime_network="vastora-runtime"

validate_vastora_runtime_network() {
  driver="$(docker network inspect --format '{{.Driver}}' "$vastora_runtime_network" 2>/dev/null || true)"
  managed="$(docker network inspect --format '{{ index .Labels "io.vastora.managed" }}' "$vastora_runtime_network" 2>/dev/null || true)"
  component="$(docker network inspect --format '{{ index .Labels "io.vastora.component" }}' "$vastora_runtime_network" 2>/dev/null || true)"
  identity="$(docker network inspect --format '{{ index .Labels "io.vastora.network" }}' "$vastora_runtime_network" 2>/dev/null || true)"
  if [ "$driver" != "bridge" ] || [ "$managed" != "true" ] || [ "$component" != "runtime-network" ] || [ "$identity" != "$vastora_runtime_network" ]; then
    echo "Refusing to use Docker network $vastora_runtime_network because its driver or ownership labels do not match Vastora." >&2
    return 1
  fi
  container_ids="$(docker network inspect --format '{{range $id, $_ := .Containers}}{{$id}}{{"\n"}}{{end}}' "$vastora_runtime_network" 2>/dev/null || true)"
  for container_id in $container_ids; do
    container_managed="$(docker container inspect --format '{{ index .Config.Labels "io.vastora.managed" }}' "$container_id" 2>/dev/null || true)"
    container_component="$(docker container inspect --format '{{ index .Config.Labels "io.vastora.component" }}' "$container_id" 2>/dev/null || true)"
    if [ "$container_managed" != "true" ] || [ -z "$container_component" ]; then
      echo "Refusing to use Docker network $vastora_runtime_network because container $container_id is not owned by Vastora." >&2
      return 1
    fi
  done
}

ensure_vastora_runtime_network() {
  driver="$(docker network inspect --format '{{.Driver}}' "$vastora_runtime_network" 2>/dev/null || true)"
  if [ -n "$driver" ]; then
    validate_vastora_runtime_network
    return $?
  fi

  if docker network create \
    --driver bridge \
    --label io.vastora.managed=true \
    --label io.vastora.component=runtime-network \
    --label io.vastora.network="$vastora_runtime_network" \
    "$vastora_runtime_network" >/dev/null; then
    return 0
  fi

  if validate_vastora_runtime_network; then
    return 0
  fi
  echo "Could not create the shared Docker bridge network $vastora_runtime_network." >&2
  return 1
}
