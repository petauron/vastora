# Agent 更新中断与后端离线恢复

## 当前 MVP 契约

#411 最初提出有界重试；后续 #412 的执行模型改为**出错停止、人工核对后处置**。更新助手不在失败或开机时自动重跑，也不在结果确认失败后再次激活候选程序。完整 Agent 的长期低内存验收不属于当前 MVP 门槛，见 [交付范围](agent-execution-delivery-audit.md#mvp-发布范围以用户最新决定为准)。

更新分为三件独立的事：

1. 调度进程准备更新、启动助手。调度成功不表示安装成功。
2. 助手向 Center 单次交接，依次获取 stop、backup、preserve、install、start 授权。错误或响应丢失后停止，不重复消费授权。
3. 助手等待目标版本的新心跳，再提交结果。结果确认失败时保留本地结果、候选程序和受保护恢复材料；不重新安装、重启或降级。

### 结果归属

`POST /api/v1/agents/{id}/tasks/{taskID}/result` 中，只有持久更新助手发送 `hostUpdateHelper: true`。普通执行器和调度失败回报不设置此字段。

Center 在保存结果的同一次条件写入中检查归属：普通结果要求 `running`；更新助手结果要求 `agent.update` 的 `helper_running`。仍须匹配 Agent、任务、attempt、execution、session、未过期授权和未处置状态。助手成功还要求已经获得 start 授权及近期目标版本心跳。

因此，systemd 已启动助手但调度调用随后被中断时，迟到的调度失败不能覆盖助手结果。如果调度失败先被接受，后来的助手交接会被拒绝。该字段选择已有授权的结果通道，不授予新的执行权限。

鉴权失败返回 401；过期、错误归属或已消费的授权返回 409。目标心跳未到时只读观察返回 `ready: false`，最多观察 30 次、总计 30 秒；HTTP 错误立即停止。已消费结果不再自动重报，重复或迟到结果返回 409 且不改写既有证据。

发布顺序为 Center 先更新，再通过托管更新下发同版 Agent。持久助手运行下载的目标程序，由新助手声明结果归属。旧助手没有此声明时不得推断其身份或恢复其授权，按人工恢复流程处理。

## 人工恢复

1. 在设置页的执行记录中核对节点、目标版本、任务 attempt、阶段及错误。不要依据“调度成功”或本地 `result.json` 独立判断已完成。
2. 确认旧更新助手确实已停止。保留 operation、result、候选可执行文件、`.previous` 及 pre-migration 恢复点；不要通过删除文件、修改数据库或重新启用旧 unit 来清除提示。
3. 检查实际运行的可执行版本、主 Agent 状态、Center 的近期心跳和业务健康。若可能已迁移数据库，不启动旧二进制、不降级数据库。
4. 执行过期后会显示“结果未知”并阻止后续任务。管理员勾选旧执行已停止、填写核对说明：
   - 目标版本已运行并有新心跳：使用“确认已完成”。Center 保留原始失败/未知证据及处置记录。
   - 尚未完成：先在维护窗口核对并处理受保护的中断状态，再明确放弃；需要再更新时创建新的授权尝试。不要直接重放旧助手。
5. 若成功结果已被 Center 接收，只是响应未到达助手，先核对 Center 已完成状态和本地材料，再按受保护清理流程处理残留；不要重新执行更新。

缺少可靠证据时保持待处理，不伪造成功。数据库恢复遵循既有前向迁移和备份流程。

## 共享 HAProxy 与离线后端

容器名称后端使用 Docker DNS `127.0.0.11:53`、运行时 resolvers 和 `init-addr libc,none`。后端不存在时保持 DOWN；恢复或以同一网络别名替换后由 DNS 与健康检查重新发现。无关路由继续服务，未匹配 SNI 继续拒绝，Proxy Protocol 只用于指定路由。

相同期望配置保留现有 HAProxy 容器；后端不可用不是替换共享网关的依据。运维应检查应用期望状态、网络别名及实际容器状态。明确停止的应用不要自动启动，也不要替换成其他上游。

## 回归入口

- `TestExecutionUpdateHandoffIsAtomicAndSingleUse`：交接前失败、交接后迟到调度失败、助手成功/失败/未知、进程更替、过期及人工处置。
- `TestExecutionFailureRemainsFencedAndSuccessRequiresResultCommit`：助手通道不能领取普通任务结果。
- `TestExecutionHostHandoffDoesNotRearmOnRestart`、`TestHostUpdateHelperUsesAuthenticatedLifecycleCallbacks`：调度与助手的真实客户端回报格式。
- `TestHostUpdateObservationIsBoundedAndReadOnly`：心跳等待、401/409/503、取消、结果保留，不重复服务变更。
- `TestHostUpdateServiceDoesNotReplayOnFailureOrBoot`、`TestHostUpdateRecoveryPointBlocksAutomaticCandidateResume`：停止策略及恢复点保护。
- `TestShared443KeepsCaddyOnItsPrivateContainerSocket`、`TestHAProxyMatchingRuntimeCanBeRetained`：DNS、路由边界和容器保留条件。

这些测试不宣称复现了原事故的全部内存压力，也不替代生产节点的独立部署与健康核对。
