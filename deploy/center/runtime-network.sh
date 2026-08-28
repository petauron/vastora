#!/bin/sh

vastora_runtime_network="vastora-runtime"

ensure_vastora_runtime_network() {
  driver="$(docker network inspect --format '{{.Driver}}' "$vastora_runtime_network" 2>/dev/null || true)"
  if [ -n "$driver" ]; then
    if [ "$driver" != "bridge" ]; then
      echo "Docker network $vastora_runtime_network exists but uses the unsupported $driver driver." >&2
      return 1
    fi
    return 0
  fi

  if docker network create \
    --driver bridge \
    --label io.vastora.managed=true \
    --label io.vastora.component=runtime-network \
    "$vastora_runtime_network" >/dev/null; then
    return 0
  fi

  driver="$(docker network inspect --format '{{.Driver}}' "$vastora_runtime_network" 2>/dev/null || true)"
  if [ "$driver" = "bridge" ]; then
    return 0
  fi
  echo "Could not create the shared Docker bridge network $vastora_runtime_network." >&2
  return 1
}
