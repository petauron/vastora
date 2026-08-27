#!/bin/sh
set -eu

source_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
install_dir="/opt/vastora/center"
systemd_unit_dir="${VASTORA_SYSTEMD_UNIT_DIR:-/etc/systemd/system}"
center_data_volume="vastora_center-data"
deployer_socket_volume="vastora_deployer-socket"
input_device="${VASTORA_UNINSTALL_INPUT:-/dev/tty}"
output_device="${VASTORA_UNINSTALL_OUTPUT:-/dev/tty}"
application_cleanup="keep"
delete_application_data=no
force_offline=no
local_agent_id=""
update_path_paused=no
host_cli="${VASTORA_HOST_CLI_PATH:-/usr/local/bin/vastora}"
agent_unit="$systemd_unit_dir/vastora-agent.service"

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
case "$host_cli" in
  /*/vastora) ;;
  *) echo "The Vastora host command path must be an absolute path ending in /vastora." >&2; exit 2 ;;
esac
if [ "$(id -u)" -ne 0 ]; then
  echo "Run the Center uninstaller with sudo." >&2
  exit 1
fi
for required in awk cat docker grep id rm systemctl; do
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

verify_managed_runtime_volume() {
  volume_name="$1"
  compose_volume="$2"
  container_name="$3"
  component="$4"
  if ! volume_exists "$volume_name"; then
    return 0
  fi
  project_label="$(docker volume inspect --format '{{ index .Labels "com.docker.compose.project" }}' "$volume_name")"
  volume_label="$(docker volume inspect --format '{{ index .Labels "com.docker.compose.volume" }}' "$volume_name")"
  managed_label="$(docker volume inspect --format '{{ index .Labels "io.vastora.managed" }}' "$volume_name")"
  component_label="$(docker volume inspect --format '{{ index .Labels "io.vastora.component" }}' "$volume_name")"
  if { [ "$project_label" = "vastora" ] && [ "$volume_label" = "$compose_volume" ]; } || \
     { [ "$managed_label" = "true" ] && [ "$component_label" = "center-headscale-storage" ]; }; then
    return 0
  fi
  if ! docker container inspect "$container_name" >/dev/null 2>&1; then
    echo "Refusing to delete legacy volume $volume_name because its owning Vastora container is unavailable." >&2
    exit 1
  fi
  container_managed="$(docker container inspect --format '{{ index .Config.Labels "io.vastora.managed" }}' "$container_name")"
  container_component="$(docker container inspect --format '{{ index .Config.Labels "io.vastora.component" }}' "$container_name")"
  mounted_volumes="$(docker container inspect --format '{{ range .Mounts }}{{ println .Name }}{{ end }}' "$container_name")"
  if [ "$container_managed" != "true" ] || [ "$container_component" != "$component" ] || \
     ! printf '%s\n' "$mounted_volumes" | grep -Fqx "$volume_name"; then
    echo "Refusing to delete legacy volume $volume_name because its runtime ownership could not be proven." >&2
    exit 1
  fi
}

if [ "$managed_install" = yes ] && ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required to remove Center safely." >&2
  exit 1
fi
verify_managed_volume "$deployer_socket_volume" "deployer-socket"

managed_container_remove() {
  container_name="$1"
  component="$2"
  if ! docker container inspect "$container_name" >/dev/null 2>&1; then
    return 0
  fi
  managed_label="$(docker container inspect --format '{{ index .Config.Labels "io.vastora.managed" }}' "$container_name")"
  component_label="$(docker container inspect --format '{{ index .Config.Labels "io.vastora.component" }}' "$container_name")"
  if [ "$managed_label" != "true" ] || [ "$component_label" != "$component" ]; then
    echo "Refusing to remove container $container_name because its Vastora ownership labels do not match." >&2
    exit 1
  fi
  docker rm -f "$container_name" >/dev/null
}

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
fi

if [ "$managed_install" = yes ] && [ "$application_cleanup" = "remove" ]; then
  if [ -x "$host_cli" ] && [ -s /var/lib/vastora/agent/agent.db ]; then
    local_agent_id="$($host_cli agent status --data-dir /var/lib/vastora/agent 2>/dev/null | awk -F ': ' '$1 == "Agent ID" {print $2; exit}')"
  fi
  if ! (
    cd "$install_dir"
    capabilities="$(docker compose exec -T center /usr/local/bin/vastora center capabilities 2>/dev/null)"
    printf '%s\n' "$capabilities" | grep -Fqx 'decommission-applications' &&
      printf '%s\n' "$capabilities" | grep -Fqx 'agent-host-decommission'
  ); then
    echo "Updating this older Center before managed application cleanup..."
    "$source_dir/upgrade.sh" --install-dir "$install_dir"
  fi
  offline_report="$({
    cd "$install_dir"
    set -- --data-dir /var/lib/vastora
    if [ -n "$local_agent_id" ]; then
      set -- "$@" --deferred-agent-id "$local_agent_id"
    fi
    docker compose exec -T center /usr/local/bin/vastora center offline-agent-cleanups "$@"
  })"
  if [ -n "$offline_report" ]; then
    echo >&4
    echo "以下离线节点无法自动清理。请稍后在每台主机运行所列命令：" >&4
    printf '%s\n' "$offline_report" >&4
    echo "继续会把这些节点标记为未完成清理，不会声称清理成功。" >&4
    printf '请输入 FORCE 继续卸载 Center: ' >&4
    if ! IFS= read -r offline_confirmation <&3 || [ "$offline_confirmation" != "FORCE" ]; then
      echo "卸载已取消；没有删除任何服务或数据。" >&4
      exit 0
    fi
    force_offline=yes
  fi
  exec 3<&-
  exec 4>&-
  if [ -e "$path_unit" ]; then
    echo "Pausing Center updates during application cleanup..."
    systemctl stop vastora-center-update.path >/dev/null
    update_path_paused=yes
  fi
  echo "Requesting application removal from every Agent..."
  (
    cd "$install_dir"
    set -- --data-dir /var/lib/vastora
    if [ "$delete_application_data" = yes ]; then
      set -- "$@" --delete-data
    fi
    if [ "$force_offline" = yes ]; then
      set -- "$@" --force-offline
    fi
    if [ -n "$local_agent_id" ]; then
      set -- "$@" --deferred-agent-id "$local_agent_id"
    fi
    docker compose exec -T center /usr/local/bin/vastora center decommission-applications "$@"
  )
fi

if [ "$managed_install" = yes ]; then
  exec 3<&- 2>/dev/null || true
  exec 4>&- 2>/dev/null || true
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

if [ "$managed_install" = yes ] && [ "$application_cleanup" = "remove" ]; then
  echo "Removing bundled Headscale and the shared gateway..."
  verify_managed_runtime_volume vastora_headscale-data headscale-data vastora-center-headscale center-headscale
  verify_managed_runtime_volume vastora_headscale-config headscale-config vastora-center-headscale center-headscale
  verify_managed_runtime_volume vastora_headscale-caddy-data headscale-caddy-data vastora-gateway-caddy gateway
  verify_managed_runtime_volume vastora_headscale-caddy-config headscale-caddy-config vastora-gateway-caddy gateway
  managed_container_remove vastora-center-headscale center-headscale
  managed_container_remove vastora-gateway-haproxy layer4-gateway
  managed_container_remove vastora-gateway-caddy gateway
  if [ -n "$local_agent_id" ] && [ -x "$host_cli" ]; then
    echo "Removing the Agent on the Center host..."
    set -- agent uninstall --purge --runtime-cleaned --keep-binary
    if [ "$delete_application_data" = yes ]; then
      set -- "$@" --delete-data
    fi
    "$host_cli" "$@"
  fi
fi

if volume_exists "$deployer_socket_volume"; then
  verify_managed_volume "$deployer_socket_volume" "deployer-socket"
  docker volume rm "$deployer_socket_volume" >/dev/null
fi
if [ "$managed_install" = yes ] && [ "$application_cleanup" = "remove" ]; then
  for volume_spec in \
    "vastora_headscale-data:headscale-data" \
    "vastora_headscale-config:headscale-config" \
    "vastora_headscale-caddy-data:headscale-caddy-data" \
    "vastora_headscale-caddy-config:headscale-caddy-config"; do
    volume_name="${volume_spec%%:*}"
    compose_name="${volume_spec#*:}"
    if volume_exists "$volume_name"; then
      docker volume rm "$volume_name" >/dev/null
    fi
  done
  if volume_exists "$center_data_volume"; then
    verify_managed_volume "$center_data_volume" "center-data"
    docker volume rm "$center_data_volume" >/dev/null
  fi
fi
if [ "$managed_install" = yes ] && [ -f "$install_dir/.host-cli-installed" ]; then
  if [ -f "$agent_unit" ] && grep -Fq 'Description=Vastora Agent' "$agent_unit"; then
    echo "Keeping the Vastora command because this host still runs an Agent."
  elif [ -f "$host_cli" ] && [ ! -L "$host_cli" ] && "$host_cli" help 2>&1 | grep -Fq 'Vastora control-plane tools'; then
    rm -f "$host_cli"
    if [ -f "$host_cli.previous" ] && "$host_cli.previous" help 2>&1 | grep -Fq 'Vastora control-plane tools'; then
      rm -f "$host_cli.previous"
    fi
  else
    echo "Keeping the unrecognized command at $host_cli." >&2
  fi
fi
if [ "$managed_install" = yes ]; then
  rm -rf "$install_dir"
fi

echo
echo "Vastora Center has been uninstalled."
if [ "$application_cleanup" = "keep" ]; then
  echo "Preserved: Agent, Headscale, Caddy, HAProxy, Center database, applications, and application data."
  echo "Applications and application data: preserved"
elif [ "$delete_application_data" = yes ]; then
  echo "Center, bundled Headscale, gateway, reachable Agents, applications, and application data: removed"
  echo "Applications and application data: deleted"
else
  echo "Center, bundled Headscale, gateway, reachable Agents, and applications: removed"
  echo "Applications: removed; application data: preserved"
fi
if [ "$force_offline" = yes ]; then
  echo "Offline Agents: cleanup incomplete; run the manual commands shown above on those hosts."
fi
