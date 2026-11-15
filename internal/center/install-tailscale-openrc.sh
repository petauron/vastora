# Included only in the Alpine private-network bootstrap. The distribution APK
# currently predates Vastora's security floor; use the reviewed official static
# release instead, without glibc compatibility or an edge repository.
install_alpine_tailscale() (
  set -eu
  case "$tailscale_version:$arch" in
    1.102.3:amd64) expected=36ddd9b51be57ffc2990cf76323cfa13643bfbb1b8a969f6183fa164741cdef5 ;;
    1.102.3:arm64) expected=a0fa1b154af8c61f862a2259f559f7396d96c0225f4a863eae2333e1546bbe25 ;;
    *) echo 'No reviewed Alpine Tailscale distribution for this version/architecture.' >&2; exit 1 ;;
  esac
  # Do not overwrite an external or partially installed daemon. An interrupted
  # install can resume only when its protected, complete receipt still matches.
  receipt=/var/lib/vastora/agent/tailscale-static.sha256
  if [ -f "$receipt" ] && [ ! -L "$receipt" ]; then
    sha256sum -c "$receipt" >/dev/null || exit 1
    exit 0
  fi
  for path in /usr/bin/tailscale /usr/sbin/tailscaled /etc/init.d/tailscale; do
    if [ -e "$path" ] || [ -L "$path" ]; then
      echo 'Existing Tailscale resource requires inspection; nothing was replaced.' >&2
      exit 1
    fi
  done
  stage=$(mktemp -d -t vastora-tailscale.XXXXXX)
  trap 'rm -rf "$stage"' EXIT HUP INT TERM
  archive="tailscale_${tailscale_version}_${arch}.tgz"
  curl --proto '=https' --tlsv1.2 --max-filesize 134217728 -fsS \
    "https://pkgs.tailscale.com/stable/$archive" -o "$stage/archive.tgz"
  printf '%s  %s\n' "$expected" "$stage/archive.tgz" | sha256sum -c - >/dev/null
  tar -xzf "$stage/archive.tgz" -C "$stage"
  source="$stage/tailscale_${tailscale_version}_${arch}"
  "$source/tailscale" version --json >/dev/null
  install -d -m 0755 /etc/init.d /usr/sbin /usr/bin
  install -d -m 0700 /var/lib/vastora/agent /var/lib/tailscale
  cat > "$stage/service" <<'RC'
#!/sbin/openrc-run
# Managed by Vastora
# Resource: static-tailscale
supervisor=supervise-daemon
command=/usr/sbin/tailscaled
command_args="--state=/var/lib/tailscale/tailscaled.state --socket=/run/tailscale/tailscaled.sock --port=${port:-0} ${command_args:-}"
respawn_delay=5
respawn_max=0
output_logger='/usr/bin/logger -t tailscaled'
error_logger='/usr/bin/logger -t tailscaled'
depend() { need net; use logger; before vastora-agent; }
start_pre() { checkpath --directory --mode 0755 /run/tailscale; }
RC
  install -m 0755 "$source/tailscale" /usr/bin/tailscale
  install -m 0755 "$source/tailscaled" /usr/sbin/tailscaled
  install -m 0755 "$stage/service" /etc/init.d/tailscale
  sha256sum /usr/bin/tailscale /usr/sbin/tailscaled /etc/init.d/tailscale > "$stage/receipt"
  install -m 0600 "$stage/receipt" "$receipt"
  rc-update add tailscale default
)
