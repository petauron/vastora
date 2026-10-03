#!/bin/sh
set -eu
lab=${1:?pass an absolute prepared lab directory}
case "$lab" in /*) ;; *) echo 'absolute directory required' >&2; exit 1;; esac
[ "$(uname -s)" = Linux ] || { echo 'Linux required' >&2; exit 1; }
# The Docker daemon may remap container root. This directory contains only
# disposable synthetic credentials, never production exports.
chmod 755 "$lab"
chmod 644 "$lab"/*.json "$lab"/*.pem "$lab"/*.py "$lab"/*.cfg
prefix=vastora-recovery-lab-$$
network=$prefix-network
xr=ghcr.io/xtls/xray-core:26.7.28@sha256:b697cda1588faca696ab7f7755dd1161f60862af3ff6026300e44cff6aedd558
py=python@sha256:399babc8b49529dabfd9c922f2b5eea81d611e4512e3ed250d75bd2e7683f4b0
ha=docker.io/library/haproxy:3.2.7-alpine@sha256:3b80483d47e1c7d1fc7eb4b9104f33d9a51259769be299eb675524dca2bc8157
# Fail before taking ownership if any target already exists.
for role in witness landing entry client gateway; do
 if docker container inspect "$prefix-$role" >/dev/null 2>&1; then echo 'resource collision' >&2; exit 1; fi
done
if docker network inspect "$network" >/dev/null 2>&1; then echo 'network collision' >&2; exit 1; fi
cleanup() {
 status=$?
 if [ "$status" -ne 0 ]; then
  for role in client gateway entry landing witness; do
   docker logs "$prefix-$role" >"$lab/$role.log" 2>&1 || true
   chmod 600 "$lab/$role.log"
  done
 fi
 for role in client gateway entry landing witness; do docker rm -f "$prefix-$role" >/dev/null 2>&1 || true; done
 docker network rm "$network" >/dev/null 2>&1 || true
}
# No host ports, host mounts outside this fixture, private-controller changes,
# privileged containers or production configuration are involved.
docker network create --internal --subnet 192.168.241.0/24 --label vastora.recovery-lab=true "$network" >/dev/null
trap cleanup EXIT INT TERM
run() {
 run_role=$1; run_address=$2; run_image=$3; shift 3
 docker run -d --name "$prefix-$run_role" --label vastora.recovery-lab=true --network "$network" --ip "$run_address" --user 0 --cpus 1 --memory 128m --pids-limit 64 --cap-drop ALL --security-opt no-new-privileges:true --read-only --mount "type=bind,src=$lab,dst=/lab,readonly" "$run_image" "$@" >/dev/null
}
run witness 192.168.241.4 "$py" python /lab/witness.py
run landing 192.168.241.5 "$xr" run -c /lab/landing.json
for phase in initial replacement; do
 address=192.168.241.2
 if [ "$phase" = replacement ]; then
  # Remove only the owned runtime. Reconstruct on a different machine address
  # without rewriting the saved native/fixed client credentials.
  docker rm -f "$prefix-gateway" "$prefix-entry" >/dev/null
  address=192.168.241.7
 fi
 run entry "$address" "$xr" run -c "/lab/server-$phase.json"
 run gateway 192.168.241.6 "$ha" haproxy -f "/lab/gateway-$phase.cfg"
 for kind in native fixed invalid; do
  run client 192.168.241.3 "$xr" run -c "/lab/$kind.json"
  # Readiness is separate from authenticated acceptance. Retry only this
  # harmless local socket check, never a mutation or failed client request.
  docker exec "$prefix-witness" python -c 'import socket,time
for attempt in range(50):
 try:
  with socket.create_connection(("192.168.241.3",1080),.2): pass
  break
 except OSError: time.sleep(.1)
else: raise RuntimeError("client not ready")'
  expected=$address
  if [ "$kind" = fixed ]; then expected=192.168.241.5; fi
  docker exec "$prefix-witness" python /lab/probe.py "$phase" "$kind" "$expected"
  docker rm -f "$prefix-client" >/dev/null
 done
done
