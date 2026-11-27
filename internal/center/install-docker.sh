#!/bin/sh
set -eu

docker_install_as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  else
    sudo "$@"
  fi
}

# Configure bounded per-container json-file log rotation defaults before Docker
# packages are installed or started. Existing log-driver/log-opts settings,
# unknown keys, and pre-installed Docker daemons are preserved.
configure_docker_logging() {
  if [ "$(id -u)" -eq 0 ] &&
    [ -n "${VASTORA_AGENT_BIN:-}" ] &&
    [ -f "$VASTORA_AGENT_BIN" ] &&
    [ -x "$VASTORA_AGENT_BIN" ] &&
    [ ! -L "$VASTORA_AGENT_BIN" ]; then
    "$VASTORA_AGENT_BIN" agent prepare-docker-logging
    return 0
  fi

  docker_install_as_root sh -eu -s -- /etc/docker/daemon.json /var/lib/docker <<'VASTORA_DOCKER_LOG_EOF'
daemon_json="$1"
default_data_root="$2"
daemon_dir="$(dirname "$daemon_json")"
threshold_kib=15728640

select_max_size_for_dir() {
  target_dir="$1"
  if ! command -v df >/dev/null 2>&1 || ! command -v awk >/dev/null 2>&1; then
    printf '100m\n'
    return 0
  fi
  total_kib="$(df -Pk "$target_dir" </dev/null 2>/dev/null | awk 'NR == 2 && $2 ~ /^[0-9]+$/ { print $2; exit }')"
  if [ -n "$total_kib" ] && awk -v kib="$total_kib" -v limit="$threshold_kib" 'BEGIN { exit (kib + 0 >= limit ? 0 : 1) }' </dev/null; then
    printf '200m\n'
  else
    printf '100m\n'
  fi
}

select_default_max_size() {
  probe="$default_data_root"
  while [ ! -e "$probe" ]; do
    parent="$(dirname "$probe")"
    if [ "$parent" = "$probe" ]; then
      printf '100m\n'
      return 0
    fi
    probe="$parent"
  done
  if [ ! -d "$probe" ]; then
    printf '100m\n'
    return 0
  fi
  select_max_size_for_dir "$probe"
}

if [ -L "$daemon_dir" ]; then
  echo "Refusing to modify Docker configuration: $daemon_dir is a symbolic link." >&2
  exit 1
fi
if [ -e "$daemon_dir" ] && [ ! -d "$daemon_dir" ]; then
  echo "Refusing to modify Docker configuration: $daemon_dir is not a directory." >&2
  exit 1
fi
if [ -L "$daemon_json" ]; then
  echo "Refusing to modify $daemon_json: symbolic links are not supported." >&2
  exit 1
fi
if [ -e "$daemon_json" ] && [ ! -f "$daemon_json" ]; then
  echo "Refusing to modify $daemon_json: not a regular file." >&2
  exit 1
fi

if [ ! -e "$daemon_json" ]; then
  max_size="$(select_default_max_size)"
  if [ ! -d "$daemon_dir" ]; then
    mkdir -p -m 0755 "$daemon_dir"
  fi
  if [ -L "$daemon_dir" ] || [ ! -d "$daemon_dir" ]; then
    echo "Refusing to modify Docker configuration: $daemon_dir is not a real directory." >&2
    exit 1
  fi
  tmp_file="$(mktemp "$daemon_dir/.daemon.json.vastora.XXXXXX")"
  trap 'rm -f "$tmp_file"' EXIT HUP INT TERM
  printf '{\n  "log-driver": "json-file",\n  "log-opts": {\n    "compress": "true",\n    "max-file": "5",\n    "max-size": "%s"\n  }\n}\n' "$max_size" > "$tmp_file"
  chmod 0644 "$tmp_file"
  mv "$tmp_file" "$daemon_json"
  trap - EXIT HUP INT TERM
  echo "Configured default Docker json-file log rotation ($max_size x 5, compressed) in $daemon_json."
  exit 0
fi

file_bytes="$(wc -c < "$daemon_json" | tr -d '[:space:]')"
if [ -z "$file_bytes" ] || [ "$file_bytes" -gt 1048576 ]; then
  echo "Refusing to modify $daemon_json: file exceeds 1 MiB." >&2
  exit 1
fi

if command -v python3 >/dev/null 2>&1 && python3 -c 'import json' </dev/null >/dev/null 2>&1; then
  python3 -c '
import errno
import json
import os
import stat
import sys
import tempfile

daemon_json = sys.argv[1]
default_data_root = sys.argv[2]
threshold_bytes = 15 * 1024 * 1024 * 1024


def reject_constant(value):
    raise ValueError("invalid JSON constant: " + value)


def fs_total_bytes(path):
    try:
        st = os.statvfs(path)
        block_size = st.f_frsize if st.f_frsize > 0 else st.f_bsize
        if block_size > 0 and st.f_blocks > 0:
            return block_size * st.f_blocks
    except OSError:
        pass
    return None


def select_max_size(doc):
    has_data_root = "data-root" in doc
    has_graph = "graph" in doc
    if has_data_root and has_graph:
        return "100m"
    if has_data_root or has_graph:
        key = "data-root" if has_data_root else "graph"
        val = doc.get(key)
        if not isinstance(val, str) or not val.startswith("/"):
            return "100m"
        try:
            st = os.stat(val)
        except OSError:
            return "100m"
        if not stat.S_ISDIR(st.st_mode):
            return "100m"
        total = fs_total_bytes(val)
        if total is not None and total >= threshold_bytes:
            return "200m"
        return "100m"
    probe = default_data_root
    while True:
        try:
            st = os.stat(probe)
            if stat.S_ISDIR(st.st_mode):
                total = fs_total_bytes(probe)
                if total is not None and total >= threshold_bytes:
                    return "200m"
                return "100m"
            return "100m"
        except OSError as exc:
            if exc.errno != errno.ENOENT:
                return "100m"
            parent = os.path.dirname(probe)
            if parent == probe:
                return "100m"
            probe = parent


daemon_dir = os.path.dirname(daemon_json)
st = os.lstat(daemon_json)
if stat.S_ISLNK(st.st_mode) or not stat.S_ISREG(st.st_mode):
    sys.stderr.write("Refusing to modify %s: not a regular file.\n" % daemon_json)
    sys.exit(1)
if st.st_size > 1048576:
    sys.stderr.write("Refusing to modify %s: file exceeds 1 MiB.\n" % daemon_json)
    sys.exit(1)

mode = stat.S_IMODE(st.st_mode)
with open(daemon_json, "rb") as handle:
    raw = handle.read()

try:
    doc = json.loads(raw.decode("utf-8"), parse_constant=reject_constant)
except Exception as exc:
    sys.stderr.write("Refusing to overwrite invalid JSON in %s: %s\n" % (daemon_json, exc))
    sys.exit(1)

if not isinstance(doc, dict):
    sys.stderr.write("Refusing to overwrite %s: top-level JSON value must be an object.\n" % daemon_json)
    sys.exit(1)

if "log-driver" in doc or "log-opts" in doc:
    print("Preserving existing Docker logging configuration in %s." % daemon_json)
    sys.exit(0)

max_size = select_max_size(doc)
doc["log-driver"] = "json-file"
doc["log-opts"] = {
    "compress": "true",
    "max-file": "5",
    "max-size": max_size,
}
payload = (json.dumps(doc, indent=2, sort_keys=True, ensure_ascii=False) + "\n").encode("utf-8")

fd, tmp_path = tempfile.mkstemp(prefix=".daemon.json.vastora.", dir=daemon_dir)
wrapped = False
try:
    os.fchmod(fd, mode)
    with os.fdopen(fd, "wb") as tmp_file:
        wrapped = True
        tmp_file.write(payload)
        tmp_file.flush()
        os.fsync(tmp_file.fileno())
    os.replace(tmp_path, daemon_json)
    dir_fd = os.open(daemon_dir, os.O_RDONLY)
    try:
        os.fsync(dir_fd)
    finally:
        os.close(dir_fd)
except Exception:
    if not wrapped:
        try:
            os.close(fd)
        except OSError:
            pass
    try:
        os.unlink(tmp_path)
    except OSError:
        pass
    raise

print("Configured default Docker json-file log rotation (%s x 5, compressed) in %s." % (max_size, daemon_json))
' "$daemon_json" "$default_data_root" </dev/null
  exit 0
fi

if command -v jq >/dev/null 2>&1 && jq -n '{}' </dev/null >/dev/null 2>&1; then
  if ! jq -e -s 'length == 1 and (.[0] | type == "object")' "$daemon_json" </dev/null >/dev/null 2>&1; then
    echo "Refusing to overwrite invalid or non-object JSON in $daemon_json." >&2
    exit 1
  fi
  if jq -e 'has("log-driver") or has("log-opts")' "$daemon_json" </dev/null >/dev/null 2>&1; then
    echo "Preserving existing Docker logging configuration in $daemon_json."
    exit 0
  fi
  if jq -e 'has("data-root") and has("graph")' "$daemon_json" </dev/null >/dev/null 2>&1; then
    max_size=100m
  elif jq -e 'has("data-root") or has("graph")' "$daemon_json" </dev/null >/dev/null 2>&1; then
    custom_root="$(jq -er 'if has("data-root") then .["data-root"] else .["graph"] end | select(type == "string" and startswith("/") and (contains("\n") | not))' "$daemon_json" </dev/null 2>/dev/null || true)"
    if [ -n "$custom_root" ] && [ -d "$custom_root" ]; then
      max_size="$(select_max_size_for_dir "$custom_root")"
    else
      max_size=100m
    fi
  else
    max_size="$(select_default_max_size)"
  fi
  file_mode="$(stat -c '%a' "$daemon_json" </dev/null 2>/dev/null || stat -f '%Lp' "$daemon_json" </dev/null 2>/dev/null || printf '644')"
  case "$file_mode" in
    [0-7][0-7][0-7]|[0-7][0-7][0-7][0-7]) ;;
    *) file_mode=644 ;;
  esac
  tmp_file="$(mktemp "$daemon_dir/.daemon.json.vastora.XXXXXX")"
  trap 'rm -f "$tmp_file"' EXIT HUP INT TERM
  jq --arg max_size "$max_size" \
    '. + {"log-driver": "json-file", "log-opts": {"compress": "true", "max-file": "5", "max-size": $max_size}}' \
    "$daemon_json" </dev/null > "$tmp_file"
  chmod "$file_mode" "$tmp_file"
  mv "$tmp_file" "$daemon_json"
  trap - EXIT HUP INT TERM
  echo "Configured default Docker json-file log rotation ($max_size x 5, compressed) in $daemon_json."
  exit 0
fi

echo "Existing $daemon_json was preserved unchanged; default Docker log rotation was not configured because neither python3 nor jq is installed." >&2
exit 0
VASTORA_DOCKER_LOG_EOF
}

# Alpine uses its maintained distribution packages and native OpenRC service.
if [ -r /etc/os-release ]; then
 . /etc/os-release
 if [ "${ID:-}" = alpine ]; then
  case "${VERSION_ID:-}" in 3.24|3.24.*) ;; *) echo 'Supported Alpine version: 3.24' >&2; exit 1 ;; esac
  [ "$(id -u)" -eq 0 ] || { echo 'Run the Alpine installer as root.' >&2; exit 1; }
  command -v rc-service >/dev/null 2>&1 && command -v apk >/dev/null 2>&1 || exit 1
  if command -v docker >/dev/null 2>&1; then
   docker info >/dev/null 2>&1 || { echo 'Existing Docker daemon is not running; no changes made.' >&2; exit 1; }
   exit 0
  fi
  configure_docker_logging
  apk add --no-cache docker docker-openrc docker-cli-compose
  rc-update add docker default
  rc-service docker start
  docker info >/dev/null
  exit 0
 fi
fi

if ! command -v apt-get >/dev/null 2>&1 ||
  ! command -v dpkg >/dev/null 2>&1 ||
  ! command -v dpkg-query >/dev/null 2>&1 ||
  ! command -v systemctl >/dev/null 2>&1; then
  echo "Docker installation requires apt, dpkg, and systemd." >&2
  exit 1
fi

if [ ! -r /etc/os-release ]; then
  echo "Cannot identify this server: /etc/os-release is missing." >&2
  exit 1
fi
. /etc/os-release
case "${ID:-}:${VERSION_ID:-}" in
  debian:12) distro=debian; codename=bookworm ;;
  debian:13) distro=debian; codename=trixie ;;
  ubuntu:22.04) distro=ubuntu; codename=jammy ;;
  ubuntu:24.04) distro=ubuntu; codename=noble ;;
  ubuntu:26.04) distro=ubuntu; codename=resolute ;;
  *)
    echo "Unsupported system: ${PRETTY_NAME:-unknown}. Use Debian 12/13 or Ubuntu 22.04/24.04/26.04." >&2
    exit 1 ;;
esac

arch="$(dpkg --print-architecture)"
case "$arch" in
  amd64|arm64) ;;
  *) echo "Unsupported architecture: $arch. Vastora requires amd64 or arm64." >&2; exit 1 ;;
esac

if command -v docker >/dev/null 2>&1; then
  if ! docker info >/dev/null 2>&1; then
    echo "Docker is installed but the daemon is not running. Start it before installing Vastora Agent." >&2
    exit 1
  fi
  echo "Docker is already installed and running. Nothing was changed."
  exit 0
fi

for package in docker.io docker-compose docker-compose-v2 docker-doc docker-buildx podman-docker containerd runc; do
  if [ "$(dpkg-query -W -f='${db:Status-Status}' "$package" 2>/dev/null || true)" = "installed" ]; then
    echo "Conflicting package: $package. Review the Docker documentation before replacing it; no packages were removed." >&2
    exit 1
  fi
done

if [ "$(id -u)" -ne 0 ] && ! command -v sudo >/dev/null 2>&1; then
  echo "Run this script as root, or install sudo first." >&2
  exit 1
fi

configure_docker_logging

echo "Installing Docker for $distro $VERSION_ID ($codename, $arch)..."
docker_install_as_root apt-get update
docker_install_as_root apt-get install --no-remove -y ca-certificates curl

docker_install_as_root install -m 0755 -d /etc/apt/keyrings
docker_install_as_root curl --proto '=https' --tlsv1.2 --max-filesize 1048576 -fsS \
  "https://download.docker.com/linux/$distro/gpg" -o /etc/apt/keyrings/docker.asc
docker_install_as_root chmod 0644 /etc/apt/keyrings/docker.asc

docker_install_as_root tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/$distro
Suites: $codename
Components: stable
Architectures: $arch
Signed-By: /etc/apt/keyrings/docker.asc
EOF
docker_install_as_root chmod 0644 /etc/apt/sources.list.d/docker.sources

docker_install_as_root apt-get update
docker_install_as_root apt-get install --no-remove -y \
  docker-ce \
  docker-ce-cli \
  containerd.io \
  docker-buildx-plugin \
  docker-compose-plugin

docker_install_as_root systemctl enable --now docker
if ! docker_install_as_root docker info >/dev/null 2>&1; then
  echo "Docker was installed, but its daemon did not become ready." >&2
  exit 1
fi
docker_install_as_root docker version
docker_install_as_root docker compose version
