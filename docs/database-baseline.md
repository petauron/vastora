# 数据库基线压缩与兼容层退役（MVP）

关联：petauron/vastora#1074、petauron/vastora#1371、petauron/vastora#977。

## 支持的最早升级与恢复基线

- **Center**：压缩基线为 schema **123**（业务结构与 122 相同；`Vastora v0.1.0` 发布于 schema **134**）。历史迁移 4–122 已移除；小于 122 的旧库须先在备份副本上通过最后支持 122 的已发布版本完成升级。
- **Agent**：压缩基线为 schema **26**，要求最低输入基线为 **`Vastora v0.1.0`（schema 25）**。schema 26 的表结构与 schema 25 完全一致，已移除：
  - 1–24 阶段的历史前向迁移分支；
  - `meridian_runtime_state` 表与 `agent-meridian-runtime` 密文上下文读取器、旧版 `applied_installations.sealed_secrets` 读取分支；
  - `<dataDir>/meridian` 旧挂载目录回退（若磁盘仍存在 `<dataDir>/meridian`，`agent.Open` 直接失败关闭，始终仅使用 `<dataDir>/glivela`）。

## 现有库转换

### Center（122 → 123）

1. 运维先确认运行版本、数据库 `PRAGMA user_version` 为 122，迁移记录最大版本为 122 且全部已应用。
2. 保留完整 Center 数据目录备份，尤其 `center.key` 与数据库必须配对；不把凭据或完整配置写到公开日志。
3. 在维护窗口停止旧 Center，再由新版本独占打开原数据目录，不能让两个版本同时写入。
4. 程序先核对外键和 SQLite 完整性，然后通过 `VACUUM INTO` 保存一致的 122 快照到私有 `migration-backups/`，文件权限 `0600`。
5. 单个事务仅替换 `goose_db_version` 记录并将 `user_version` 推进到 123。账号、节点、部署、订阅、流量、密文、业务触发器和关联全部原样保留。
6. 再次核对完整性及外键。重新打开时不重复压缩、不重复备份。

### Agent（`v0.1.0` schema 25 → schema 26）

1. 仍停留在 pre-`v0.1.0`（如 schema 24）的节点，必须先升级并启动 **`Vastora v0.1.0`**，完成 `meridian_runtime_state` → `glivela_runtime_state`（schema 25）迁移并清理遗留 `<dataDir>/meridian` 目录，再升级到 `v0.2.0+`。
2. 打开 schema 25 数据库时，Agent 先校验 `storage_key_binding` 与全部密文表（仅接受 `glivela_runtime_state` 当前上下文），通过 `VACUUM INTO` 在 `<dataDir>/schema-25-backup-baseline/agent.db`（目录 `0700`、文件 `0600`）生成经密钥与逻辑哈希核验的一致快照，再于单事务内将 `PRAGMA user_version` 推进到 **26**。
3. 重复重启不会改写密文或重复生成快照；任何 `user_version < 25`、`> 26`、缺少密钥绑定或残留旧上下文密文的数据库均在修改前失败关闭。
4. **备份恢复路径**：`vastora` 加密备份归档强制要求恢复版本与归档版本严格一致（`restore requires the exact artifact release; upgrade only after recovery`）。对于 `v0.1.0`（schema 25）生成的 Agent 归档，先使用 `v0.1.0` 恢复并核验身份，再升级二进制推进到 schema 26；同机 `schema-25-backup-baseline/agent.db` 快照与原 `agent.key` 配对后可直接由当前版本打开并完成 `25 → 26` 推进。

## 刻意保留的签名与审计标识

以下历史标识属于已签名制品或冻结审计证据，不作重命名或重写：

- 官方签名应用目录 v4 中的历史应用键 `vastora-official/meridian`、执行能力 `meridian-runtime` 及对应 `official_app_ui_assets` / `/api/v1/official-app-ui/meridian/{version}/*` 资源路径；
- Center 已完成业务移交并由触发器永久只读冻结的历史 `meridian_*` 表及对应密文 AAD；
- SQLite `CHECK` 约束与历史任务回执中的持久化枚举及 JSON 标签（`meridian.runtime.apply`、`meridian.legacy.export`、`meridian.legacy.retire`、`meridian.subscription.publish`、`meridian.recovery.acceptance`、`meridian.link-bandwidth*`、`services.app_protocol` 中的 `meridian/entry` 与 `meridian/subscription`）。

## 失败与恢复

备份或检查失败则停止启动；事务提交前失败会回滚迁移元数据。提交后的检查失败仍拒绝启动，保留原快照供恢复，不自动降级数据库。恢复必须先停止对应服务，配对恢复快照与原密钥文件，不要在运行中覆盖数据库，也不要把旧 WAL/SHM 与恢复快照混用。
