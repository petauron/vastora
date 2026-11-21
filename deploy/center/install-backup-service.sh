#!/bin/sh
set -eu
install_dir="/opt/vastora/center"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --install-dir) install_dir="$2"; shift 2 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
case "$install_dir" in /*) ;; *) exit 2 ;; esac
case "$install_dir" in *[!A-Za-z0-9_./-]*) exit 2 ;; esac
[ "$(id -u)" -eq 0 ] || { echo 'Root is required.' >&2; exit 1; }
for dependency in python3 restic docker systemctl flock; do
  command -v "$dependency" >/dev/null || { echo "Install $dependency first." >&2; exit 1; }
done
[ -f "$install_dir/.host-backup/scope.json" ] || { echo 'Review the host scope first; see docs/host-backups.md.' >&2; exit 1; }
for name in service timer; do
  target="/etc/systemd/system/vastora-host-backup.$name"
  if [ -L "$target" ] || { [ -e "$target" ] && ! grep -qx '# Managed by Vastora host backup' "$target"; }; then
    echo "Refusing to replace unowned unit $target" >&2; exit 1
  fi
done
cat > /etc/systemd/system/vastora-host-backup.service <<EOF
# Managed by Vastora host backup
[Unit]
Description=Vastora consistent host backup
After=docker.service
Requires=docker.service
[Service]
Type=oneshot
ExecStart=/usr/bin/env python3 $install_dir/host-backup.py --install-dir $install_dir
UMask=0077
TimeoutStartSec=6h
Nice=10
IOSchedulingClass=idle
EOF
cat > /etc/systemd/system/vastora-host-backup.timer <<'EOF'
# Managed by Vastora host backup
[Unit]
Description=Vastora backup schedule and interrupted-service recovery
[Timer]
OnBootSec=30s
OnUnitInactiveSec=60s
[Install]
WantedBy=timers.target
EOF
systemctl daemon-reload
systemctl enable --now vastora-host-backup.timer
install -m 0644 /dev/null "$install_dir/.backup-service-enabled"
