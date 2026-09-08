# 集群灾备（#340）

状态：代码开发中；以下是恢复合同和操作步骤，不是已完成的生产恢复演练。
不得将 `ready` 解读成已替管理员验证异地存储、所有应用语义或整套集群恢复。

## 哪些数据需要独立保护

| 组件 | 分类 | 保护范围 / 恢复来源 |
| --- | --- | --- |
| Center | authoritative | 现有密码加密备份：一致性数据库快照与原始根密钥；精确版本和 schema |
| 内置 Headscale | authoritative | SQLite、Noise/DERP 身份、配置、ACL、DERP 配置；独立加密导出 |
| 每台 Agent | authoritative | SQLite、原始本地密钥、加密连接身份、任务回执、最后应用状态、主机安装所有权记录 |
| 已加入私网的 Agent | authoritative | 还必须包含原始 `tailscaled.state`；缺失时走替换接入，不能冒充原身份恢复 |
| Caddy / HAProxy / cloudflared | reconstructible | 从 Center 期望状态、Agent 应用状态与加密证书/凭据重建；不能以容器 running 代替健康检查 |
| 应用数据卷 | application_owned | 外部备份系统保管完整卷集；不是 Center 备份的一部分 |
| 镜像层、缓存、日志派生内容 | reconstructible | 按固定摘要重新取得镜像；不将缓存当作配置真相 |

官方应用的备份合同在 `internal/catalog/recovery_policy.go`，独立于执行包版本，
避免仅添加灾备元数据就强制升级运行中的应用。当前合同要求停止应用后备份完整卷集：
3x-ui 的 db/cert/acme；CPA 的 auths/logs/plugins；Keeper 的 data。
Komari Agent 从受管配置重建，其远端 Komari 面板的数据不在 Vastora 的保护范围。
未知应用或未知版本显示 `unsupported`，不能猜测快照一致性。
保留数据卸载后的应用仍列入清单；仅明确完成“删除数据卸载”的记录才不再要求卷备份。

## 备份与登记

使用与组件相同的 Vastora 发布版。密码只从 0600 文件读取，至少 12 个字符；
不要把密码放在参数、日志或 shell 历史里。先创建只有管理员可读写的备份目录。
下面全部为示例路径，不会由页面自动执行：

```sh
vastora center backup --data-dir /var/lib/vastora/center \
  --output /backup/center.vastora --password-file /secure/backup-password

vastora recovery export-agent --data-dir /var/lib/vastora/agent \
  --tailscale-state /var/lib/tailscale/tailscaled.state \
  --output /backup/agent.vastora --password-file /secure/backup-password

vastora recovery export-headscale --data-dir /mounted/headscale-data \
  --config-dir /mounted/headscale-config --output /backup/headscale.vastora \
  --password-file /secure/backup-password
```

Headscale 源目录必须是已确认的内置卷挂载点，不能按名称猜测任意目录。
导出保留 SQLite 已提交 WAL 状态；配置和身份在快照前后必须一致。
Headscale 导出只支持当前固定镜像的内置布局；命令会只读核对 Docker 中的容器
所有权、固定镜像和实际卷挂载点，并确认导出前后容器身份没有变化。需在能访问
这些挂载目录的原 Docker 主机运行；不能用相似目录冒充受管数据。
其他版本或外部 Headscale 先使用其自身恢复流程，不登记成已验证的内置备份。

导出回执仅含版本、身份指纹、成员摘要与时间，没有明文凭据。把回执、
加密文件和密码分别保存；**必须由管理员复制到独立故障域**。主机上的 `/backup`
不是异地备份。默认证据有效期 24 小时；建议至少每日备份，并在重要配置、
密钥、升级变更前后各保存一个版本。保留数个历史恢复点，不覆盖唯一副本。

在持有 Center 数据目录的管理环境中，校验复制回来的加密文件并登记：

```sh
vastora recovery register --data-dir /var/lib/vastora/center --kind agent \
  --input /verified-copy/agent.vastora --password-file /secure/backup-password \
  --identity-sha256 sha256:INDEPENDENTLY_CONFIRMED_FINGERPRINT
vastora recovery status --data-dir /var/lib/vastora/center
```

`--kind` 可为 center、agent、headscale。Agent 指纹必须匹配当前受管身份，
Center 指纹必须匹配其根密钥；Headscale 首次登记需要从原主机独立确认的
Noise 公钥指纹。登记失败不会写入成功证据。HTTP 接口为需登录的
`GET /api/v1/recovery`，只读，不接受 URL、文件路径、密码或远程下载请求。

`recovery inspect` 用于检查 Agent / Headscale 加密导出，Center 仍使用原有
Center 备份格式与恢复校验，不存在第二套 Center 备份实现。

## 应用数据卷证据

先按上述 Catalog 合同停止对应应用、由外部工具备份完整卷集，恢复到隔离环境
验证后再启动原应用。只操作该应用，不停止无关服务。记录独立存储中的不含
凭据的引用（例如 `restic:repository/snapshot-id`）、加密文件 SHA-256、创建时间、
恢复验证时间和准确的 application/node/site ID、应用版本及全部卷名。

`vastora recovery register-application --data-dir CENTER_DIR --input COPIED_ARTIFACT
--evidence EVIDENCE_JSON` 对复制的完整文件计算摘要，校验
`ExternalRecoveryEvidence` 的身份、Catalog 合同及时间，再记录**管理员确认的恢复结果**。
不下载该引用、不把应用数据上传到 Center，也不宣称能解密任意第三方备份格式。
遗漏任一必需卷、版本/身份不一致、缺少加密或恢复声明，均不能登记成功。

## 确定的恢复顺序

1. **隔离旧控制面和丢失主机**。确认它们不会重新联网持旧身份执行任务；保存诊断，
   停止重试。不能让新旧 Center 同时控制同一批 Agent。
2. 准备与备份完全一致的 Vastora 版本及固定摘要的运行镜像，先不启动 Center/Agent。
   在隔离环境执行 `vastora center restore --input FILE --data-dir NEW_CENTER_DIR
   --password-file FILE`；目标必须为空，原数据库与根密钥必须成对恢复。
3. 使用 `vastora recovery restore --input FILE --data-dir NEW_HEADSCALE_DIR
   --password-file FILE --component-id ORIGINAL_HTTPS_ENDPOINT --identity-sha256 HASH`
   验证并展开 Headscale。目标必须不存在。将 config.yaml/policy.hujson/derp.yaml
   放回其已确认的配置卷，db.sqlite/两把身份密钥放回数据卷；权限 0600，目录 0700。
   使用原域名和原身份，确认正确镜像摘要，先启动内置私网控制服务。
4. 恢复 Center 的原服务地址/信任关系和必要管理入口，再启动唯一的 Center。
   私网 DNS 的受管额外记录由 Center 重建；不能复制缓存当成新配置。
5. 对仍可恢复原身份的 Agent，使用相同 `recovery restore` 命令展开到新目录，
   `--component-id` 为原 Agent ID。先恢复其原始应用数据卷及所有权标签，
   将导出的 Tailscale 状态安装回已停止的 tailscaled 的原状态路径，然后启动
   tailscaled 并核对私网身份/地址。**不要先启动 Agent，让空卷覆盖原应用。**
6. 恢复该 Agent 的原 systemd 参数、roles/capabilities、数据目录和原受管网络策略，
   再启动 Agent。中断回执先对账，应用网络先恢复，随后恢复入口；没有新健康证据
   时不能宣布入口正常。先恢复一台 Worker，再恢复一台 Gateway，逐台核对。
7. 核对各应用、订阅、客户端身份和认证业务；确认完成后才开放新环境公网服务。
   恢复成功后再进行常规升级。数据库只向前迁移，不能用旧二进制打开新 schema。

组件恢复命令不会启动服务、重写 systemd、挂载外部卷或调整 DNS。它原子发布新目录，
拒绝覆盖已有目标；错误密码、摘要/身份/版本不匹配不会发布可启动目录。
目录发布后如果磁盘同步失败，要检查该完整目录，不要删除它或自动重建空状态。

## 替换接入不是身份恢复

丢失 Agent 私钥或 Tailscale 原始状态时，使用“离线节点 → 重新接入”。Center 先撤销/
隔离旧凭据，新命令只更新所选原节点身份；不能复制其他节点数据库或复用旧命令。
这能保留 Center 的节点归属，但不是恢复应用数据：卷与应用一致性仍需独立备份。

## 演练记录（必须真实执行后填写）

在独立主机/卷/域名环境演练，记录版本、artifact digest、恢复顺序、耗时、失败和修复：

- Center 与 Headscale 身份一致，原节点列表、ACL、应用归属和私网地址一致。
- 至少恢复或替换一台 Worker 和一台 Gateway；没有重复执行未知任务。
- 删除的是隔离环境里的派生配置后，Caddy/HAProxy/cloudflared 能从受管状态重建。
- 公网 TLS 与经认证的实际 TCP/UDP 业务通过；订阅内容、客户端身份和数据未丢失。
- 缺少文件、错误密码、篡改、过期、错误版本、错身份、非空目标均拒绝恢复。
- 中断导出/恢复后的重试不覆盖现存有效备份或已恢复的数据。

当前仓库只新增了相应回归用例，**未运行本地测试或真实恢复演练**。
界面/命令显示的备份证据不代替以上演练，也不证明异地存储可用。
