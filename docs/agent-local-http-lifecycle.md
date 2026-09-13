# Agent 本地 HTTP 连接生命周期（#428）

## 实现

- LinkChecker 的 Unix HTTP Transport 限制为每主机最多 8 条连接、最多 8 条空闲连接，空闲超时 30 秒；拨号和响应头等待都有超时。
- Store 持有 checker，心跳身份观察和周期性落地计划校验复用它。Store 关闭时终止 checker 的请求并清理连接池。
- readiness、每个 Monitor、延迟检测循环分别拥有自己的 checker，退出时关闭。Monitor.Run 入口直接登记关闭，参数验证和初始化失败也会释放已有连接；传给 Monitor 的 checker 不与其他调用者共享。
- Close 禁止新请求，取消并等待在途请求完成 Body 关闭，再关闭连接池；重复 Close 安全。请求注册表仅保存当前在途请求，不保存历史。
- pinned Center 临时 HTTPS Transport 由调用者释放；外部注入的 HTTPClient 或共享默认 Transport 不由借用者关闭。
- 公网地址启动观察和更新命令创建的临时 HTTP 客户端也明确释放。

不改变身份验证、CA pin、落地判定和业务重试规则；不借助重启、增加内存或调 GC 掩盖问题。

## 验证记录与剩余项

已通过隔离 Unix socket 测试：200 次身份读取复用单条连接，Close 后无遗留连接；非 200、无效 JSON、身份无效以及在途取消释放连接；关闭后不能重新执行请求。延迟检测相关定向测试也已通过。

补充的生命周期测试已通过三轮 race 检查：32 个并发请求最多建立 8 条连接，关闭时取消在途与排队请求；50 次 checker 替换（每次 10 次读取）没有连接累积，goroutine 在每批后回到基线容差内；截断响应、响应超时后也能释放连接。测试不依赖强制 GC，但这不是 Linux FD/内存压力环境的验证。

进一步通过两轮 race 检查：实际 observeLandingClientRuntime 连续 500 次身份读取仅使用一条 Unix 连接，goroutine 保持在预热基线容差内，Store.Close 后连接归零；实际 Monitor.Run 的正常取消、参数不完整、nft 初始化失败三种路径，各替换 20 次后连接均释放。nft 和业务断连操作为模拟，未执行宿主机变更。现有延迟目标替换/取消测试也通过。

在用户指定的非生产 Linux amd64 环境完成了隔离容器验证：分别限制 256 MiB 和 512 MiB、禁止 Swap、限制为 1 CPU/128 PID、禁用网络、不挂载宿主机服务 socket。两个限额下 LinkChecker/Monitor 生命周期测试与实际心跳身份读取/执行取消测试均各通过 10 轮。心跳每轮 500 次读取，共 5,000 次；256 MiB 测试记录的各批采样 FD 为 13、goroutine 为 7，单个 Store 始终复用一条 Unix 连接，关闭后归零。

256 MiB 测试进程采样 RSS 从预热约 41.6 MiB 上升至最高约 54.3 MiB，末轮约 53.9 MiB；PSS 接近 RSS，Swap 为零。256/512 MiB 容器的 memory.peak 分别约 38.9/35.9 MiB，memory.events 的 max/oom/oom_kill 均为零，memory.pressure 的 some/full total 均为零。RSS 与 cgroup 记账口径不同（共享文件页不一定记入当前容器），不能直接比较或当成完整 Agent 的内存预算。短测试未模拟真实 Docker、tailscaled、完整 Agent 守护进程负载，不足以证明长期内存平台期；容器均已自动退出并删除，未改动已有服务。

补充异常响应验证：使用真实 Unix HTTP 服务流式发送 8 MiB、身份字段正确的有效 JSON（固定 Content-Length 和 chunked 两种传输），以及带大响应体的非 200 回复。每种连续三次读取均拒绝，并在未调用 checker.Close、未强制 GC 的情况下确认旧连接归零；随后正常响应可读取成功，最终所有者关闭后连接仍归零。LinkChecker 针对性测试通过两轮 race。这验证响应大小与错误路径的连接清理，不代表完整 Agent 的长期资源验证已完成。

补充多 peer 并发验证：每批 8 个不同 peer 的真实 Monitor，各自持有 Unix HTTP checker，连续替换 10 批。等待每个 Monitor 唯一就绪报告后确认 8 条连接；整组取消并等待退出后连接归零，旧 checker 均拒绝新请求。全部 80 个 checker 总共只接受 80 条连接，goroutine 每批回到基线容差，无强制 GC。此测试与心跳/Monitor 退出测试通过两轮 race；nft 命令、Docker 断连和业务探测仍为模拟，不代表 Store.startLandingMonitor 的完整宿主交互。

另外删除了 Agent 中只写不读的 landingClientStatuses 缓存：旧实现按 peer ID 追加状态，切换 peer 后不清理；当前代码无读取方，删除不会影响已有对外状态字段。这是独立的潜在历史状态累积，不能据此断言它曾造成生产卡死。

以下仍需完成，不能以以上单元测试代替：

- Store 级多 peer Monitor 并发替换及真实 Docker 错误路径的完整交互。
- 正常频率下心跳/计划校验和完整 Agent 的长期 Linux FD、内存平台期；已有短期实际身份观察函数的 Unix socket/FD/goroutine 证据，尚不是整机内存归因。
- 完整负载的 RSS/PSS、PSI、socket 内存、文件页 refault 和物理 I/O 曲线；短期隔离测试的零 PSI 不代表原生产故障已复现。
- 最终代码审查、与 #412 新执行循环的整体验证。

本轮未修改生产、未重启服务，也未执行生产压力测试。现场卡死的全部进程归因仍未证明；本修复不宣称该泄漏是唯一原因。
