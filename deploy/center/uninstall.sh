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
locale_name="${LC_ALL:-${LC_MESSAGES:-${LANG:-C}}}"
case "$locale_name" in
  [zZ][hH]*) ui_language="zh" ;;
  *) ui_language="en" ;;
esac

translated() {
  if [ "$ui_language" = "zh" ]; then
    printf '%s' "$2"
  else
    printf '%s' "$1"
  fi
}

say() {
  translated "$1" "$2"
  printf '\n'
}

prompt() {
  translated "$1" "$2"
}

resume_center_updates() {
  if [ "$update_path_paused" = yes ]; then
    systemctl start vastora-center-update.path >/dev/null 2>&1 || true
  fi
}
trap resume_center_updates EXIT

usage() {
  if [ "$ui_language" = "zh" ]; then
    cat <<'EOF'
用法：./uninstall.sh [--install-dir 目录]

打开交互式终端菜单，可选择保留托管应用、卸载应用但保留数据，或卸载应用并
永久删除数据。只有所有 Agent 都成功完成所选的应用清理后，Center 才会删除。
EOF
  else
    cat <<'EOF'
Usage: ./uninstall.sh [--install-dir DIR]

Opens an interactive terminal menu for choosing whether to preserve managed
applications, uninstall them while keeping their data, or uninstall them and
permanently delete their data. Center is removed only after selected application
cleanup has completed successfully on every Agent.
EOF
  fi
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --install-dir)
      [ "$#" -ge 2 ] || { say "--install-dir requires a value." "--install-dir 需要一个目录参数。" >&2; exit 2; }
      install_dir="$2"
      shift 2
      ;;
    -h|--help) usage; exit 0 ;;
    *) say "Unknown argument: $1" "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$install_dir" in
  /*) ;;
  *) say "The Center installation directory must be absolute." "Center 安装目录必须是绝对路径。" >&2; exit 2 ;;
esac
case "$install_dir" in
  /|/bin|/boot|/dev|/etc|/home|/lib|/lib64|/opt|/proc|/root|/run|/sbin|/srv|/sys|/tmp|/usr|/var|*/../*|*/..|*/./*|*/.|*/|*//*)
    say "Refusing unsafe installation directory: $install_dir" "拒绝使用不安全的安装目录：$install_dir" >&2
    exit 2
    ;;
esac
case "$install_dir" in
  *[!A-Za-z0-9_./-]*)
    say "The installation path may contain only letters, numbers, dot, underscore, slash, and hyphen." "安装路径只能包含字母、数字、点、下划线、斜杠和连字符。" >&2
    exit 2
    ;;
esac
case "$systemd_unit_dir" in
  /*) ;;
  *) say "The systemd unit directory must be absolute." "systemd 单元目录必须是绝对路径。" >&2; exit 2 ;;
esac
case "$host_cli" in
  /*/vastora) ;;
  *) say "The Vastora host command path must be an absolute path ending in /vastora." "Vastora 主机命令必须是以 /vastora 结尾的绝对路径。" >&2; exit 2 ;;
esac
if [ "$(id -u)" -ne 0 ]; then
  say "Run the Center uninstaller with sudo." "请使用 sudo 运行 Center 卸载程序。" >&2
  exit 1
fi
for required in awk cat docker grep id rm systemctl; do
  if ! command -v "$required" >/dev/null 2>&1; then
    say "Required command is not installed: $required" "缺少必需命令：$required" >&2
    exit 1
  fi
done
if ! docker info >/dev/null 2>&1; then
  say "Docker is installed but the daemon is not running." "Docker 已安装，但守护进程未运行。" >&2
  exit 1
fi

managed_install=no
if [ -e "$install_dir" ]; then
  if [ ! -d "$install_dir" ] || [ -L "$install_dir" ]; then
    say "$install_dir is not a managed Center installation; nothing was removed." "$install_dir 不是受管的 Center 安装目录；未删除任何内容。" >&2
    exit 1
  fi
  if [ ! -f "$install_dir/.env" ] || [ ! -f "$install_dir/compose.yaml" ] || \
     ! grep -Eq '^name:[[:space:]]+vastora[[:space:]]*$' "$install_dir/compose.yaml"; then
    say "$install_dir is not a managed Center installation; nothing was removed." "$install_dir 不是受管的 Center 安装目录；未删除任何内容。" >&2
    exit 1
  fi
  physical_install_dir="$(CDPATH='' cd -- "$install_dir" && pwd -P)"
  if [ "$physical_install_dir" != "$install_dir" ]; then
    say "Refusing an installation directory reached through a symbolic link: $install_dir" "拒绝使用通过符号链接访问的安装目录：$install_dir" >&2
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
    say "Refusing to delete volume $volume_name because its ownership labels do not match Vastora Center." "拒绝删除卷 $volume_name，因为其所有权标签与 Vastora Center 不匹配。" >&2
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
    say "Refusing to delete legacy volume $volume_name because its owning Vastora container is unavailable." "拒绝删除旧卷 $volume_name，因为无法找到其所属的 Vastora 容器。" >&2
    exit 1
  fi
  container_managed="$(docker container inspect --format '{{ index .Config.Labels "io.vastora.managed" }}' "$container_name")"
  container_component="$(docker container inspect --format '{{ index .Config.Labels "io.vastora.component" }}' "$container_name")"
  mounted_volumes="$(docker container inspect --format '{{ range .Mounts }}{{ println .Name }}{{ end }}' "$container_name")"
  if [ "$container_managed" != "true" ] || [ "$container_component" != "$component" ] || \
     ! printf '%s\n' "$mounted_volumes" | grep -Fqx "$volume_name"; then
    say "Refusing to delete legacy volume $volume_name because its runtime ownership could not be proven." "拒绝删除旧卷 $volume_name，因为无法确认其运行时所有权。" >&2
    exit 1
  fi
}

if [ "$managed_install" = yes ] && ! docker compose version >/dev/null 2>&1; then
  say "Docker Compose v2 is required to remove Center safely." "安全卸载 Center 需要 Docker Compose v2。" >&2
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
    say "Refusing to remove container $container_name because its Vastora ownership labels do not match." "拒绝删除容器 $container_name，因为其 Vastora 所有权标签不匹配。" >&2
    exit 1
  fi
  docker rm -f "$container_name" >/dev/null
}

service_unit="$systemd_unit_dir/vastora-center-update.service"
path_unit="$systemd_unit_dir/vastora-center-update.path"
if [ -e "$service_unit" ]; then
  if ! grep -Fq 'Description=Vastora Center verified update' "$service_unit" || \
     ! grep -Fq "ExecStart=$install_dir/update-center.sh --install-dir $install_dir" "$service_unit"; then
    say "Refusing to remove an unrecognized systemd unit: $service_unit" "拒绝删除无法识别的 systemd 单元：$service_unit" >&2
    exit 1
  fi
fi
if [ -e "$path_unit" ]; then
  if ! grep -Fq 'Description=Watch for a Vastora Center update request' "$path_unit" || \
     ! grep -Fq "PathExists=$install_dir/.update-request" "$path_unit"; then
    say "Refusing to remove an unrecognized systemd unit: $path_unit" "拒绝删除无法识别的 systemd 单元：$path_unit" >&2
    exit 1
  fi
fi

if [ "$managed_install" = yes ]; then
  if ! exec 3< "$input_device"; then
    say "An interactive terminal is required to choose the uninstall scope." "选择卸载范围需要交互式终端。" >&2
    exit 1
  fi
  if ! exec 4> "$output_device"; then
    say "Cannot open the interactive terminal for uninstall." "无法打开卸载所需的交互式终端。" >&2
    exit 1
  fi
  while :; do
    if [ "$ui_language" = "zh" ]; then
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
    else
      cat >&4 <<'EOF'

╭────────────────────────────────────────────╮
│          Vastora Center Uninstall          │
╰────────────────────────────────────────────╯

  1) Uninstall Center only
     Keep all Agents, applications, and data for migration or reinstall.

  2) Uninstall Center and all applications
     Stop managed applications on every node but preserve application data.

  3) Uninstall Center, applications, and application data
     Permanently delete data volumes used by managed applications.

  4) Cancel

EOF
    fi
    prompt "Choose [1-4]: " "请选择 [1-4]: " >&4
    if ! IFS= read -r choice <&3; then
      say "No uninstall option was selected; nothing was changed." "未选择卸载选项；未修改任何内容。" >&2
      exit 1
    fi
    case "$choice" in
      1) application_cleanup="keep"; break ;;
      2) application_cleanup="remove"; delete_application_data=no; break ;;
      3) application_cleanup="remove"; delete_application_data=yes; break ;;
      4) say "Cancelled; nothing was changed." "已取消，没有修改任何内容。" >&4; exit 0 ;;
      *) say "Enter 1, 2, 3, or 4." "请输入 1、2、3 或 4。" >&4 ;;
    esac
  done
  if [ "$delete_application_data" = yes ]; then
    echo >&4
    say "This permanently deletes all managed application data volumes and cannot be undone." "这会永久删除所有托管应用的数据卷，无法恢复。" >&4
    prompt "Enter DELETE to continue: " "请输入 DELETE 继续: " >&4
    if ! IFS= read -r confirmation <&3 || [ "$confirmation" != "DELETE" ]; then
      say "Confirmation did not match; cancelled without changing anything." "确认内容不匹配，已取消；没有修改任何内容。" >&4
      exit 0
    fi
  else
    echo >&4
    prompt "Continue? [y/N]: " "确认执行？[y/N]: " >&4
    if ! IFS= read -r confirmation <&3; then
      say "No confirmation was received; nothing was changed." "未收到确认；未修改任何内容。" >&2
      exit 1
    fi
    case "$confirmation" in
      y|Y|yes|YES) ;;
      *) say "Cancelled; nothing was changed." "已取消，没有修改任何内容。" >&4; exit 0 ;;
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
    say "Updating this older Center before managed application cleanup..." "正在更新旧版 Center，以便执行托管应用清理……"
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
    say "The following offline nodes cannot be cleaned automatically. Run the listed command on each host later:" "以下离线节点无法自动清理。请稍后在每台主机运行所列命令：" >&4
    printf '%s\n' "$offline_report" >&4
    say "Continuing marks these nodes as incompletely cleaned and will not report them as successfully removed." "继续会把这些节点标记为未完成清理，不会声称清理成功。" >&4
    prompt "Enter FORCE to continue uninstalling Center: " "请输入 FORCE 继续卸载 Center: " >&4
    if ! IFS= read -r offline_confirmation <&3 || [ "$offline_confirmation" != "FORCE" ]; then
      say "Uninstall cancelled; no services or data were removed." "卸载已取消；没有删除任何服务或数据。" >&4
      exit 0
    fi
    force_offline=yes
  fi
  exec 3<&-
  exec 4>&-
  if [ -e "$path_unit" ]; then
    say "Pausing Center updates during application cleanup..." "正在暂停 Center 更新，以执行应用清理……"
    systemctl stop vastora-center-update.path >/dev/null
    update_path_paused=yes
  fi
  say "Requesting application removal from every Agent..." "正在请求所有 Agent 删除应用……"
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
  say "Disabling verified Center updates..." "正在停用 Center 安全更新服务……"
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
  say "Stopping Center and its restricted deployer..." "正在停止 Center 及其受限部署服务……"
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
  say "Removing bundled Headscale and the shared gateway..." "正在删除内置 Headscale 和共享网关……"
  verify_managed_runtime_volume vastora_headscale-data headscale-data vastora-center-headscale center-headscale
  verify_managed_runtime_volume vastora_headscale-config headscale-config vastora-center-headscale center-headscale
  verify_managed_runtime_volume vastora_headscale-caddy-data headscale-caddy-data vastora-gateway-caddy gateway
  verify_managed_runtime_volume vastora_headscale-caddy-config headscale-caddy-config vastora-gateway-caddy gateway
  managed_container_remove vastora-center-headscale center-headscale
  managed_container_remove vastora-gateway-haproxy layer4-gateway
  managed_container_remove vastora-gateway-caddy gateway
  if [ -n "$local_agent_id" ] && [ -x "$host_cli" ]; then
    say "Removing the Agent on the Center host..." "正在删除 Center 主机上的 Agent……"
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
    say "Keeping the Vastora command because this host still runs an Agent." "此主机仍在运行 Agent，因此保留 Vastora 命令。"
  elif [ -f "$host_cli" ] && [ ! -L "$host_cli" ] && "$host_cli" help 2>&1 | grep -Fq 'Vastora control-plane tools'; then
    rm -f "$host_cli"
    if [ -f "$host_cli.previous" ] && "$host_cli.previous" help 2>&1 | grep -Fq 'Vastora control-plane tools'; then
      rm -f "$host_cli.previous"
    fi
  else
    say "Keeping the unrecognized command at $host_cli." "无法识别 $host_cli，因此予以保留。" >&2
  fi
fi
if [ "$managed_install" = yes ]; then
  rm -rf "$install_dir"
fi

echo
say "Vastora Center has been uninstalled." "Vastora Center 已卸载。"
if [ "$application_cleanup" = "keep" ]; then
  say "Preserved: Agent, Headscale, Caddy, HAProxy, Center database, applications, and application data." "已保留：Agent、Headscale、Caddy、HAProxy、Center 数据库、应用及应用数据。"
  say "Applications and application data: preserved" "应用和应用数据：已保留"
elif [ "$delete_application_data" = yes ]; then
  say "Center, bundled Headscale, gateway, reachable Agents, applications, and application data: removed" "Center、内置 Headscale、网关、可达 Agent、应用及应用数据：已删除"
  say "Applications and application data: deleted" "应用和应用数据：已删除"
else
  say "Center, bundled Headscale, gateway, reachable Agents, and applications: removed" "Center、内置 Headscale、网关、可达 Agent 及应用：已删除"
  say "Applications: removed; application data: preserved" "应用：已删除；应用数据：已保留"
fi
if [ "$force_offline" = yes ]; then
  say "Offline Agents: cleanup incomplete; run the manual commands shown above on those hosts." "离线 Agent：清理未完成；请在这些主机上运行上方显示的手动命令。"
fi
