#!/bin/sh
set -eu
lab=${1:?pass the absolute prepared lab directory}
case "$lab" in /*) ;; *) echo "lab directory must be absolute" >&2; exit 1;; esac
[ "$(uname -s)" = Linux ] || { echo "Linux is required" >&2; exit 1; }
prefix=vastora-fullcone-$$
mkdir -p "$lab"
chmod 755 "$lab"
chmod 644 "$lab"/*.json "$lab"/*.pem "$lab"/*.py "$lab"/haproxy.cfg
network=$prefix-network
xr=ghcr.io/xtls/xray-core:26.7.28@sha256:b697cda1588faca696ab7f7755dd1161f60862af3ff6026300e44cff6aedd558
# Fetch pinned images before starting finite-lived fixtures or packet capture.
# Pull latency must not consume the capture window.
for image in "$xr" alpine@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b python@sha256:399babc8b49529dabfd9c922f2b5eea81d611e4512e3ed250d75bd2e7683f4b0 docker.io/library/haproxy:3.2.7-alpine@sha256:3b80483d47e1c7d1fc7eb4b9104f33d9a51259769be299eb675524dca2bc8157; do
 docker image inspect "$image" >/dev/null 2>&1 || docker pull "$image" >/dev/null
done
capture_pid=
cleanup(){
 if [ -n "$capture_pid" ];then wait "$capture_pid" || true;fi
 for name in ${prefix}-client ${prefix}-haproxy ${prefix}-xray ${prefix}-peer ${prefix}-net;do docker rm -f "$name" >/dev/null 2>&1 || true;done
 docker network rm "$network" >/dev/null 2>&1 || true
}
# Never take over pre-existing resources.
for name in ${prefix}-client ${prefix}-haproxy ${prefix}-xray ${prefix}-peer ${prefix}-net;do if docker container inspect "$name" >/dev/null 2>&1;then echo 'test resource already exists';exit 1;fi;done
if docker network inspect "$network" >/dev/null 2>&1;then echo 'test network already exists';exit 1;fi
trap cleanup EXIT INT TERM
docker network create --internal --subnet 192.168.240.0/24 "$network" >/dev/null
docker run -d --name ${prefix}-net --network "$network" --ip 192.168.240.2 --cap-drop ALL --security-opt no-new-privileges:true --read-only alpine@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b sleep 180 >/dev/null
# The namespace holder supplies the private host interface. Xray shares it
# directly with no port publication or extra NAT; the enclosing lab stays private.
docker run -d --name ${prefix}-peer --network "$network" --ip 192.168.240.4 --mount "type=bind,src=$lab,dst=/lab,readonly" python@sha256:399babc8b49529dabfd9c922f2b5eea81d611e4512e3ed250d75bd2e7683f4b0 sleep 180 >/dev/null
peer_pid=$(docker inspect --format '{{.State.Pid}}' ${prefix}-peer)
sudo -n nsenter -t "$peer_pid" -n ip addr add 192.168.240.5/24 dev eth0
sudo -n timeout 120s nsenter -t "$peer_pid" -n tcpdump -Z "$(id -un)" -U -i eth0 -w "$lab/stun.pcap" "udp portrange 3478-3479" >/dev/null 2>&1 &
capture_pid=$!
docker exec -d ${prefix}-peer sh -c 'python /lab/stun.py >/tmp/stun.log 2>&1'
docker run --rm --network none --user 0 --cap-drop ALL --security-opt no-new-privileges:true --read-only --mount "type=bind,src=$lab/rendered.json,dst=/etc/xray/config.json,readonly" "$xr" run -test -c /etc/xray/config.json >"$lab/config-check.log" 2>&1
docker run -d --name ${prefix}-xray --network container:${prefix}-net --user 0:65531 --cap-drop ALL --cap-add NET_BIND_SERVICE --security-opt no-new-privileges:true --read-only --pids-limit 128 --mount "type=bind,src=$lab/server.json,dst=/etc/xray/config.json,readonly" "$xr" run -c /etc/xray/config.json >/dev/null
docker run -d --name ${prefix}-haproxy --network "$network" --ip 192.168.240.6 --user 0:65530 --cap-drop ALL --cap-add NET_BIND_SERVICE --security-opt no-new-privileges:true --read-only --mount "type=bind,src=$lab/haproxy.cfg,dst=/usr/local/etc/haproxy/haproxy.cfg,readonly" docker.io/library/haproxy:3.2.7-alpine@sha256:3b80483d47e1c7d1fc7eb4b9104f33d9a51259769be299eb675524dca2bc8157 >/dev/null
sleep 1
docker exec ${prefix}-peer python /lab/boundaries.py
for phase in initial restarted;do
 if [ "$phase" = restarted ];then docker restart ${prefix}-xray ${prefix}-haproxy >/dev/null; sleep 1;fi
 echo "$phase"
 for protocol in vless hy2;do
 docker run -d --name ${prefix}-client --network "$network" --ip 192.168.240.3 --user 0 --cap-drop ALL --security-opt no-new-privileges:true --read-only --mount "type=bind,src=$lab/$protocol.json,dst=/etc/xray/config.json,readonly" "$xr" run -c /etc/xray/config.json >/dev/null
 sleep 1
 if ! docker exec ${prefix}-peer python /lab/probe.py "$protocol";then
  docker logs ${prefix}-xray >"$lab/server.log" 2>&1
  docker logs ${prefix}-client >"$lab/$protocol-client.log" 2>&1
  docker logs ${prefix}-haproxy >"$lab/haproxy.log" 2>&1
  docker exec ${prefix}-peer cat /tmp/stun.log >"$lab/stun.log"
  echo "FAILED: $protocol; saved local lab logs";exit 1
 fi
 docker rm -f ${prefix}-client >/dev/null
done
done
# Flush the isolated packet capture before removing its network namespace.
wait "$capture_pid" || [ "$?" = 124 ]
capture_pid=
docker exec ${prefix}-peer cat /tmp/stun.log >"$lab/stun.log"
docker logs ${prefix}-xray >"$lab/server.log" 2>&1
