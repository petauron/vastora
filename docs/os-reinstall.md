# KVM Debian 13 网页重装（MVP）

新购、无业务的 KVM VPS 普通接入后，在节点管理的“系统重装”中检查并确认系统盘，再安装 Debian 13。安装完成后恢复原节点身份与受支持的网络配置，通过认证回执确认完成。

## 支持范围与前置条件

- 当前执行器限 Linux amd64、BIOS、单个可明确识别的系统盘，安装过程需要主 IPv4；不支持真正 IPv6-only、UEFI、ARM 或业务自动迁移。
- Center 与 Agent 需要同一支持本功能的版本。节点须在线、无受管应用、共享网关或未解决的执行任务；不能用此功能给运行中业务重装系统。
- 预检列出缺少的系统工具，包括 `ca-certificates`、`curl`、Bash、GRUB 和磁盘工具。缺少工具时不执行，也不自动安装软件或更换源。
- 保留受支持的既有 SSH 密钥登录配置及运维账号。重装前必须通过公钥专用的 SSH 配置、实际监听端口、配置加载时间及当前 Agent 受管单元检查；条件 `Match`、用户／组访问限制、未知 SSH 启动选项或自定义 Agent 单元需要人工核对，不能自动覆盖。表单填写服务器内部 SSH 端口；商家 NAT 的外部端口映射由商家管理，必须另行确认不会改变。
- 操作前必须确认商家控制台和人工恢复入口可用。重装会清空所选系统盘全部内容；恢复材料只保护必要 Agent 身份、SSH 与网络配置，不是业务或整盘备份，也不提供自动回滚旧系统。

## 操作流程

1. 在节点管理中点击“检查”，核对新鲜的目标磁盘、网卡 MAC、IPv4/IPv6、网关和 DNS 信息。
2. 确认现有 SSH 用户及内部端口，输入显示的系统盘路径，并勾选清盘确认。
3. 点击“重装 Debian 13”。Center 先持久保存并核验加密恢复资料，再对该任务发出单次启动授权。
4. 安装后，原身份的 Agent 使用新的执行会话提交启动、系统、网络、SSH 和 Agent 单元证据。SSH 公钥与单元摘要须与受保护的原恢复资料一致；检查失败不会解除执行门槛或显示完成。重启、失联、已提交命令或普通心跳都不代表重装成功。

对人工重装的机器，恢复服务后从管理机执行 `scripts/verify-vps-onboarding.sh [-F SSH_CONFIG] SSH_ALIAS`。脚本使用严格主机密钥检查和全新 SSH 密钥连接，以实际登录账号及内部监听端口调用节点上的 `vastora agent verify-onboarding`。命令只读取 SSH 配置及受支持的 systemd 单元，不修改配置或重启服务。人工流程须主动执行此命令；它不会自动触发网页重装回执，也不代替订阅等业务验收。

有问题时保留任务和恢复资料，通过控制台排查。仅明确未获得启动授权的失败可在网页重新检查后创建新任务；启动已授权或结果不明时不会自动重放擦盘命令。网络或镜像等待超过执行窗口后，同一未撤销任务仍可提交严格核验的完成证明，这不会恢复执行权限或发出第二次启动授权。

升级 Center 前须先检查是否有正在重装或回执未决的操作。旧版 Agent 的回执不包含新增的 SSH 和单元证据；新版 Center 不会将它直接视为通过，应保留现场及恢复资料并人工核验。

## 重装安全收尾：移除 qemu-guest-agent

Debian 13 安装器（`debian-installer`）在 `finish-install` 阶段的 `08hw-detect` 检测到 KVM 虚拟化时，会自动安装 `qemu-guest-agent`；只要宿主机向来宾暴露了 virtio-serial 通道（`/dev/virtio-ports/org.qemu.guest_agent.0`），udev 就会在开机后自动拉起该服务。Debian 默认发行版配置未屏蔽 `guest-exec`、`guest-file-open`、`guest-file-write` 与 `guest-set-user-password` 等高危 RPC，意味着宿主机侧可绕过 SSH 与防火墙，直接以 `root` 身份在来宾系统内执行命令或读写文件。

网页重装脚本已在安装与首次启动阶段加入自动防护（本说明不自动授权批量修改既有节点；该自动清理逻辑的隔离 KVM 实机端到端验收尚未完成，上线前须单独核验）：

- 在 `preseed/late_command`（`07preseed`）预先将 `/target/etc/systemd/system/qemu-guest-agent.service` 屏蔽至 `/dev/null`，并写入 `/usr/lib/finish-install.d/09remove-qemu-guest-agent` 钩子，待 `08hw-detect` 运行后如检测到已安装或残余配置即通过 `dpkg --purge qemu-guest-agent` 彻底卸载，并校验 `/target/usr/sbin/qemu-ga` 不存在。
- 在首次启动脚本（`fix-eth-name.hardened.sh`，运行于 `network.target` 之前）再次校验屏蔽链接并兜底卸载残留包。

对于尚未通过上述流程重装的既有 KVM 节点，或重装完成后的接入前复核，仍须执行以下清理与回读核验：

1. 停止、禁用并屏蔽服务，防止 udev 或后续包操作重新拉起：

```sh
systemctl disable --now qemu-guest-agent.service
systemctl mask qemu-guest-agent.service
```

2. 先模拟卸载，确认只移除 `qemu-guest-agent` 且不牵连其他包，再执行彻底卸载：

```sh
apt-get --simulate purge qemu-guest-agent
apt-get purge -y qemu-guest-agent
```

3. 回读核验：确认 `dpkg-query -W qemu-guest-agent` 返回未安装、`/usr/sbin/qemu-ga` 不存在、无 `qemu-ga` 进程，且 `/etc/systemd/system/qemu-guest-agent.service` 仍指向 `/dev/null`。在后续已授权重启后复核一次，确认未被重新安装或拉起。非 KVM 环境或本身未安装该包的节点核实未安装即可。

## 网络恢复边界

自动读取受支持的 Netplan/ifupdown 声明并与运行状态交叉核对，按 MAC 恢复网卡名称，保存 IPv4、静态 IPv6、默认网关、onlink、声明的直连路由、DNS/搜索域和 RA/autoconf 策略。DHCP/RA 租约不会被固化为静态地址。

未知网络脚本、策略路由、未声明路由、不受支持的网络管理器或无法恢复的本机 stub/Tailscale DNS 依赖会阻止操作；不会静默改 DNS、停用既有服务或猜测商家网络。当前动态网络路径只有解析与生成回归，尚未完成实机端到端验收。

## 已完成的隔离验证

- Debian 11/12 与 Ubuntu 22.04/24.04/26.04 到 Debian 13 的网页任务、身份接回和再次启动；该组使用受限静态 IPv4。Ubuntu 源镜像中的空 UFW 服务须先人工确认未启用并停止，未宣称默认状态可直接操作。
- 双网卡、IPv4 /32、链路外网关、静态 IPv6、直连路由、DNS 和 RA/autoconf 的恢复；认证完成后额外重启，原身份及网络仍一致。
- 安装过程曾遇官方镜像超时和控制台辅助，记录为实际恢复验证，不宣称全程无人干预或公网 IPv6 已连通。

这些结果不是生产验收，不代表已有业务、任意网络拓扑或所有 VPS 厂商可直接重装。生产重装与后续业务恢复仍须单独授权。

已接入 Headscale/Tailscale 的主机不属于本次重装执行范围；重装不会迁移该类 VPN 身份。
