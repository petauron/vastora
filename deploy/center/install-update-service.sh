#!/bin/sh
set -eu

install_dir="/opt/vastora/center"
systemd_unit_dir="${VASTORA_SYSTEMD_UNIT_DIR:-/etc/systemd/system}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --install-dir) install_dir="$2"; shift 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done

case "$install_dir" in
  /*) ;;
  *) echo "The Center installation directory must be absolute." >&2; exit 2 ;;
esac
case "$install_dir" in
  *[!A-Za-z0-9_./-]*)
    echo "Automatic updates require an installation path containing only letters, numbers, dot, underscore, slash, and hyphen." >&2
    exit 2
    ;;
esac
case "$systemd_unit_dir" in
  /*) ;;
  *) echo "The systemd unit directory must be absolute." >&2; exit 2 ;;
esac
if [ "$systemd_unit_dir" = "/etc/systemd/system" ] && [ "$(id -u)" -ne 0 ]; then
  echo "Installing the Center update service requires root." >&2
  exit 1
fi
for required in install mktemp systemctl; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done
if [ ! -x "$install_dir/update-center.sh" ]; then
  echo "The Center update runner is missing from $install_dir." >&2
  exit 1
fi

service_unit="$(mktemp "${TMPDIR:-/tmp}/vastora-center-update-service.XXXXXX")"
path_unit="$(mktemp "${TMPDIR:-/tmp}/vastora-center-update-path.XXXXXX")"
cleanup() { rm -f "$service_unit" "$path_unit"; }
trap cleanup EXIT HUP INT TERM

cat > "$service_unit" <<EOF
[Unit]
Description=Vastora Center verified update
Documentation=https://github.com/petauron/vastora
After=docker.service network-online.target
Wants=network-online.target
Requires=docker.service

[Service]
Type=oneshot
ExecStart=$install_dir/update-center.sh --install-dir $install_dir
PrivateTmp=true
UMask=0077
TimeoutStartSec=30min
EOF

cat > "$path_unit" <<EOF
[Unit]
Description=Watch for a Vastora Center update request

[Path]
PathExists=$install_dir/.update-request
Unit=vastora-center-update.service

[Install]
WantedBy=multi-user.target
EOF

install -d -m 0755 "$systemd_unit_dir"
install -m 0644 "$service_unit" "$systemd_unit_dir/vastora-center-update.service"
install -m 0644 "$path_unit" "$systemd_unit_dir/vastora-center-update.path"
systemctl daemon-reload
systemctl enable --now vastora-center-update.path >/dev/null
install -m 0644 /dev/null "$install_dir/.update-service-enabled"
