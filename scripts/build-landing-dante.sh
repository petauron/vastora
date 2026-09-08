#!/bin/sh
# Release tooling only: never invoked on a managed node.
set -eu

architecture=${1:?expected amd64 or arm64}
destination=${2:?expected output directory}
case "$architecture" in
  amd64) triple=x86_64-linux-gnu ;;
  arm64) triple=aarch64-linux-gnu ;;
  *) echo 'Unsupported Dante architecture' >&2; exit 1 ;;
esac
case "$destination" in
  /*) ;;
  *) echo 'Output directory must be absolute' >&2; exit 1 ;;
esac

version=1.4.4
digest=1973c7732f1f9f0a4c0ccf2c1ce462c7c25060b25643ea90f9b98f53a813faec
work=$(mktemp -d)
cd "$work"
curl --fail --show-error --silent --location --proto '=https' --proto-redir '=https' \
  --connect-timeout 15 --max-time 120 --retry 3 \
  "https://www.inet.no/dante/files/dante-$version.tar.gz" -o source.tar.gz
printf '%s  source.tar.gz\n' "$digest" | sha256sum --check --status
tar -xzf source.tar.gz
cd "dante-$version"

# No PAM, Kerberos or libwrap dependency: access control is the generated
# exact-source configuration plus the Agent-owned network policy.
CC="$triple-gcc" LIBS="/usr/lib/$triple/libcrypt.so" CFLAGS='-O2 -fstack-protector-strong -D_FORTIFY_SOURCE=2' \
  LDFLAGS='-Wl,-z,relro,-z,now' ./configure --host="$triple" \
  --prefix=/usr/local/lib/vastora-landing --disable-client --disable-preload \
  --without-pam --without-gssapi --without-libwrap
make -j2
"$triple-strip" sockd/sockd
mkdir -p "$destination"
gzip -n -9 -c sockd/sockd > "$destination/danted-linux-$architecture.gz"
cp LICENSE "$destination/Dante-LICENSE"
printf '%s\n' 'This product includes software developed by Inferno Nettverk A/S, Norway.' \
  > "$destination/Dante-NOTICE"
(cd "$destination" && sha256sum "danted-linux-$architecture.gz" > "danted-linux-$architecture.gz.sha256")
