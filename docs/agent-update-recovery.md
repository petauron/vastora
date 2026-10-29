# Agent 更新中断与后端离线恢复

## 当前 MVP 契约

#411 最初提出有界重试；后续 #412 的执行模型改为**出错停止、人工核对后处置**。更新助手不在失败或开机时自动重跑，也不在结果确认失败后再次激活候选程序。完整 Agent 的长期低内存验收不属于当前 MVP 门槛，见 [交付范围](agent-execution-delivery-audit.md#mvp-发布范围以用户最新决定为准)。

更新分为三件独立的事：

1. 调度进程准备更新、启动助手。调度成功不表示安装成功。
2. 助手向 Center 单次交接，依次获取 stop、backup、preserve、install、start 授权。错误或响应丢失后停止，不重复消费授权。
3. 助手等待目标版本的新心跳，再提交结果。结果确认失败时保留本地结果、候选程序和受保护恢复材料；不重新安装、重启或降级。

### 结果归属

Agent 的 gRPC `CompleteTask` 回执中，只有持久更新助手发送 `hostUpdateHelper: true`。普通执行器和调度失败回报不设置此字段。

Center 在保存结果的同一次条件写入中检查归属：普通结果要求 `running`；更新助手结果要求 `agent.update` 的 `helper_running`。仍须匹配 Agent、任务、attempt、execution、session、未过期授权和未处置状态。助手成功还要求已经获得 start 授权及近期目标版本心跳。

因此，systemd 已启动助手但调度调用随后被中断时，迟到的调度失败不能覆盖助手结果。如果调度失败先被接受，后来的助手交接会被拒绝。该字段选择已有授权的结果通道，不授予新的执行权限。

鉴权失败返回 gRPC `Unauthenticated`；过期、错误归属或已消费的授权返回 `FailedPrecondition`。目标心跳未到时只读观察返回 `ready: false`，最多观察 30 次、总计 30 秒；控制请求出错立即停止。已消费结果不再自动重报，重复或迟到结果被拒绝且不改写既有证据。

同一通信协议版本内，Center 先更新，再通过托管更新下发同版 Agent。持久助手运行下载的目标程序，由新助手声明结果归属。旧助手没有此声明时不得推断其身份或恢复其授权，按人工恢复流程处理。

### 从旧 HTTP Agent 切换到 gRPC 的首次升级

旧 Center 不提供 gRPC，旧 Agent 不能与仅支持 gRPC 的 Center 通信。**不能直接使用现有的“Center 先升级、再自动升级 Agent”流程跨越此边界**；离线节点也不会自行迁移。首次切换由管理员协调，普通的网页更新会拒绝跨协议升级。已切换后的同协议版本继续使用正常更新流程。

1. 核对所有节点、在线状态和未完成任务；为在线节点确认独立 SSH 或控制台可用。离线节点保留原有身份，待其恢复后通过独立入口按下述步骤更新；不能确认恢复入口时不要切换。核对 Center 数据库、密钥及各节点的恢复方式与空间。
2. 核对可信发布版本和不可变 Center 镜像摘要。从该镜像的 `/app/agent-binaries/linux-amd64` 和 `/app/agent-binaries/linux-arm64` 取出对应程序，核对版本和 SHA-256；通过已验证的 SSH 在在线节点预放匹配架构的程序，保存预期摘要。不要在旧 Center 上启动新 Agent。
3. 在 Center 暂停任务领取，等待所有已领取任务、Agent 更新和卸载助手结束或明确处置。再次核对未完成任务，按既有恢复流程准备 Center 一致性备份。
4. 使用验证过的版本安装包运行 `install.sh center --expected-version VERSION --coordinated-agent-migration`。安装器只接受从无协议标记的旧版或 `http-v1` 切换到 `grpc-v1`，并只读检查管理员暂停和在途任务；检查失败时拒绝切换。这些检查不替代逐节点核对独立访问和预放文件。主控升级会停止同机 Agent，核验其数据库、密钥和原程序恢复点后再启动新版；不覆盖旧的 `.previous` 文件。
5. 检查 Center 健康和同机 Agent 后恢复任务领取，等待新版网关配置实际生效。然后通过独立入口逐台更新旧 Agent：确认节点 ID、已安装版本和预放程序的 SHA-256；停止 `vastora-agent.service`，以预放程序运行 `agent prepare-center-upgrade-recovery --data-dir /var/lib/vastora/agent --executable /usr/local/bin/vastora --source-version INSTALLED_VERSION`。该命令只在 Agent 和生命周期助手停止且无未处置的更新操作时保存并校验数据库、密钥与原程序；记下输出的受保护恢复点。
6. 将预放程序以临时文件安装到原可执行文件的同一目录，再原子替换 `/usr/local/bin/vastora`，启动 Agent。不要重建注册或修改 Agent 身份、凭据、TLS/CA 信任、路由或订阅。逐台核对新版进程、Center 新心跳、任务及业务；离线节点恢复时复用同样流程。

备份前如停止或校验失败，检查 Agent 和助手状态后恢复原服务。新版程序一旦可能打开数据库，不自动运行旧程序或降级数据库；保留恢复点并先核对实际状态，再按人工恢复流程处理。Center 启动后同样不能自动回退已迁移数据库。独立入口、自动网关接管、真实发布包和离线节点恢复须在交付前分别验收，不能仅凭上述说明认定完成。

迁移到 schema 126 后，Center 会重新排队现有系统网关配置，使新版 Agent 将 gRPC 请求送到私有上游；停用任务领取时必须等待网关 Agent 接入后再恢复领取并确认路由已生效。若切换前的旧心跳使 Center 提前排队 Agent 更新，新 Agent 的启动心跳仅撤销尚未领取且已达到目标版本的更新，保留事件记录；已经领取的任务仍按原有执行与恢复规则处理。上述保护不能替代受控切换或离线节点恢复流程。

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
