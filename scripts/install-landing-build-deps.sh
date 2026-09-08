#!/bin/sh
# Runs only inside the Ubuntu 22.04 build stage, never on managed nodes.
set -eu
native=$(dpkg --print-architecture)
case "$native" in
  amd64) foreign=arm64; mirror=http://ports.ubuntu.com/ubuntu-ports ;;
  arm64) foreign=amd64; mirror=http://archive.ubuntu.com/ubuntu ;;
  *) echo 'Unsupported build architecture' >&2; exit 1 ;;
esac
dpkg --add-architecture "$foreign"
# Ubuntu publishes amd64 and arm64 on different mirrors. Restrict each source
# to its architecture instead of querying a mirror for packages it cannot serve.
sed -i "s/^deb /deb [arch=$native] /" /etc/apt/sources.list
printf 'deb [arch=%s] %s jammy main universe\ndeb [arch=%s] %s jammy-updates main universe\ndeb [arch=%s] %s jammy-security main universe\n' \
  "$foreign" "$mirror" "$foreign" "$mirror" "$foreign" "$mirror" \
  > /etc/apt/sources.list.d/vastora-cross.list
apt-get update -qq
apt-get install -y --no-install-recommends ca-certificates curl make \
  gcc-aarch64-linux-gnu gcc-x86-64-linux-gnu \
  libc6-dev-arm64-cross libc6-dev-amd64-cross libcrypt-dev:amd64 libcrypt-dev:arm64
