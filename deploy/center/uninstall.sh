#!/bin/sh
set -eu

install_dir="/opt/vastora/center"
systemd_unit_dir="${VASTORA_SYSTEMD_UNIT_DIR:-/etc/systemd/system}"
center_data_volume="vastora_center-data"
deployer_socket_volume="vastora_deployer-socket"
input_device="${VASTORA_UNINSTALL_INPUT:-/dev/tty}"
output_device="${VASTORA_UNINSTALL_OUTPUT:-/dev/tty}"
application_cleanup="keep"
delete_application_data=no
update_path_paused=no

resume_center_updates() {
  if [ "$update_path_paused" = yes ]; then
    systemctl start vastora-center-update.path >/dev/null 2>&1 || true
  fi
}
trap resume_center_updates EXIT

usage() {
  cat <<'EOF'
Usage: ./uninstall.sh [--install-dir DIR]

Opens an interactive terminal menu for choosing whether to preserve managed
applications, uninstall them while keeping their data, or uninstall them and
permanently delete their data. Center is removed only after selected application
cleanup has completed successfully on every Agent.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --install-dir)
      [ "$#" -ge 2 ] || { echo "--install-dir requires a value." >&2; exit 2; }
      install_dir="$2"
      shift 2
      ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$install_dir" in
  /*) ;;
  *) echo "The Center installation directory must be absolute." >&2; exit 2 ;;
esac
case "$install_dir" in
  /|/bin|/boot|/dev|/etc|/home|/lib|/lib64|/opt|/proc|/root|/run|/sbin|/srv|/sys|/tmp|/usr|/var|*/../*|*/..|*/./*|*/.|*/|*//*)
    echo "Refusing unsafe installation directory: $install_dir" >&2
    exit 2
    ;;
esac
case "$install_dir" in
  *[!A-Za-z0-9_./-]*)
    echo "The installation path may contain only letters, numbers, dot, underscore, slash, and hyphen." >&2
    exit 2
    ;;
esac
case "$systemd_unit_dir" in
  /*) ;;
  *) echo "The systemd unit directory must be absolute." >&2; exit 2 ;;
esac
if [ "$(id -u)" -ne 0 ]; then
  echo "Run the Center uninstaller with sudo." >&2
  exit 1
fi
for required in cat docker grep id rm systemctl; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "Required command is not installed: $required" >&2
    exit 1
  fi
done
if ! docker info >/dev/null 2>&1; then
  echo "Docker is installed but the daemon is not running." >&2
  exit 1
fi

managed_install=no
if [ -e "$install_dir" ]; then
  if [ ! -d "$install_dir" ] || [ -L "$install_dir" ]; then
    echo "$install_dir is not a managed Center installation; nothing was removed." >&2
    exit 1
  fi
  if [ ! -f "$install_dir/.env" ] || [ ! -f "$install_dir/compose.yaml" ] || \
     ! grep -Eq '^name:[[:space:]]+vastora[[:space:]]*$' "$install_dir/compose.yaml"; then
    echo "$install_dir is not a managed Center installation; nothing was removed." >&2
    exit 1
  fi
  physical_install_dir="$(CDPATH='' cd -- "$install_dir" && pwd -P)"
  if [ "$physical_install_dir" != "$install_dir" ]; then
    echo "Refusing an installation directory reached through a symbolic link: $install_dir" >&2
    exit 1
  fi
  managed_install=yes
fi

volume_exists() {
  docker volume inspect "$1" >/dev/null 2>&1
}

verify_managed_volume() {
  volume_name="$1"
  compose_volume="$2"
  if ! volume_exists "$volume_name"; then
    return 0
  fi
  project_label="$(docker volume inspect --format '{{ index .Labels "com.docker.compose.project" }}' "$volume_name")"
  volume_label="$(docker volume inspect --format '{{ index .Labels "com.docker.compose.volume" }}' "$volume_name")"
  if [ "$project_label" != "vastora" ] || [ "$volume_label" != "$compose_volume" ]; then
    echo "Refusing to delete volume $volume_name because its ownership labels do not match Vastora Center." >&2
    exit 1
  fi
}

if [ "$managed_install" = yes ] && ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required to remove Center safely." >&2
  exit 1
fi
verify_managed_volume "$deployer_socket_volume" "deployer-socket"

service_unit="$systemd_unit_dir/vastora-center-update.service"
path_unit="$systemd_unit_dir/vastora-center-update.path"
if [ -e "$service_unit" ]; then
  if ! grep -Fq 'Description=Vastora Center verified update' "$service_unit" || \
     ! grep -Fq "ExecStart=$install_dir/update-center.sh --install-dir $install_dir" "$service_unit"; then
    echo "Refusing to remove an unrecognized systemd unit: $service_unit" >&2
    exit 1
  fi
fi
if [ -e "$path_unit" ]; then
  if ! grep -Fq 'Description=Watch for a Vastora Center update request' "$path_unit" || \
     ! grep -Fq "PathExists=$install_dir/.update-request" "$path_unit"; then
    echo "Refusing to remove an unrecognized systemd unit: $path_unit" >&2
    exit 1
  fi
fi

if [ "$managed_install" = yes ]; then
  if ! exec 3< "$input_device"; then
    echo "An interactive terminal is required to choose the uninstall scope." >&2
    exit 1
  fi
  if ! exec 4> "$output_device"; then
    echo "Cannot open the interactive terminal for uninstall." >&2
    exit 1
  fi
  while :; do
    cat >&4 <<'EOF'

╭────────────────────────────────────────────╮
│           Vastora Center 卸载              │
╰────────────────────────────────────────────╯

  1) 仅卸载 Center
     保留所有 Agent、应用和数据，适合迁移或重装。

  2) 卸载 Center 和所有应用
     批量停止所有节点上的应用，但保留应用数据。

  3) 卸载 Center、所有应用及应用数据
     永久删除所有托管应用容器使用的数据卷。

  4) 取消

EOF
    printf '请选择 [1-4]: ' >&4
    if ! IFS= read -r choice <&3; then
      echo "No uninstall option was selected; nothing was changed." >&2
      exit 1
    fi
    case "$choice" in
      1) application_cleanup="keep"; break ;;
      2) application_cleanup="remove"; delete_application_data=no; break ;;
      3) application_cleanup="remove"; delete_application_data=yes; break ;;
      4) echo "已取消，没有修改任何内容。" >&4; exit 0 ;;
      *) echo "请输入 1、2、3 或 4。" >&4 ;;
    esac
  done
  if [ "$delete_application_data" = yes ]; then
    echo >&4
    echo "这会永久删除所有托管应用的数据卷，无法恢复。" >&4
    printf '请输入 DELETE 继续: ' >&4
    if ! IFS= read -r confirmation <&3 || [ "$confirmation" != "DELETE" ]; then
      echo "确认内容不匹配，已取消；没有修改任何内容。" >&4
      exit 0
    fi
  else
    echo >&4
    printf '确认执行？[y/N]: ' >&4
    if ! IFS= read -r confirmation <&3; then
      echo "No confirmation was received; nothing was changed." >&2
      exit 1
    fi
    case "$confirmation" in
      y|Y|yes|YES) ;;
      *) echo "已取消，没有修改任何内容。" >&4; exit 0 ;;
    esac
  fi
  exec 3<&-
  exec 4>&-
fi

if [ "$managed_install" = yes ] && [ "$application_cleanup" = "remove" ]; then
  if [ -e "$path_unit" ]; then
    echo "Pausing Center updates during application cleanup..."
    systemctl stop vastora-center-update.path >/dev/null
    update_path_paused=yes
  fi
  echo "Requesting application removal from every Agent..."
  (
    cd "$install_dir"
    if [ "$delete_application_data" = yes ]; then
      docker compose exec -T center /usr/local/bin/vastora center decommission-applications \
        --data-dir /var/lib/vastora --delete-data
    else
      docker compose exec -T center /usr/local/bin/vastora center decommission-applications \
        --data-dir /var/lib/vastora
    fi
  )
fi

if [ -e "$service_unit" ] || [ -e "$path_unit" ]; then
  echo "Disabling verified Center updates..."
  if [ -e "$path_unit" ]; then
    systemctl stop vastora-center-update.path >/dev/null
    systemctl disable vastora-center-update.path >/dev/null
  fi
  if [ -e "$service_unit" ]; then
    systemctl stop vastora-center-update.service >/dev/null
  fi
  rm -f "$service_unit" "$path_unit"
  systemctl daemon-reload
  update_path_paused=no
fi

if [ "$managed_install" = yes ]; then
  echo "Stopping Center and its restricted deployer..."
  (
    cd "$install_dir"
    docker compose down
  )
else
  for service_name in center deployer; do
    container_ids="$(docker ps -aq \
      --filter 'label=com.docker.compose.project=vastora' \
      --filter "label=com.docker.compose.service=$service_name")"
    for container_id in $container_ids; do
      docker rm -f "$container_id" >/dev/null
    done
  done
fi

if volume_exists "$deployer_socket_volume"; then
  verify_managed_volume "$deployer_socket_volume" "deployer-socket"
  docker volume rm "$deployer_socket_volume" >/dev/null
fi
if [ "$managed_install" = yes ]; then
  rm -rf "$install_dir"
fi

echo
echo "Vastora Center has been uninstalled."
echo "Preserved: Agent, Headscale, Caddy, HAProxy, and Center database volume $center_data_volume."
if [ "$application_cleanup" = "keep" ]; then
  echo "Applications and application data: preserved"
elif [ "$delete_application_data" = yes ]; then
  echo "Applications and application data: deleted"
else
  echo "Applications: removed; application data: preserved"
fi
