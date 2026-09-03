#!/bin/sh
set -eu

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
docker_install_as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  else
    sudo "$@"
  fi
}

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
