# 节点协议：VLESS 与 HY2

在应用管理中打开节点的“编辑节点”，可以保存协议勾选。默认只启用
VLESS，可添加 HY2，也可只保留 HY2；至少保留一种协议。节点仍是一行，
客户端刷新原订阅即可获取已启用的协议，不生成第二个订阅地址。

实现使用 3x-ui 3.7.0 的原生 Hysteria2 入站，不安装独立 Hysteria 或
sing-box 服务，也不加入 AnyTLS。上游协议定义：
[Hysteria 入站](https://github.com/MHSanaei/3x-ui/blob/v3.7.0/frontend/src/schemas/protocols/inbound/hysteria.ts)、
[Hysteria 传输](https://github.com/MHSanaei/3x-ui/blob/v3.7.0/frontend/src/schemas/protocols/stream/hysteria.ts)。

## 使用条件

- Center 和目标 Agent 都需要包含本功能的版本，3x-ui 需要支持原生 HY2。
- 节点已有正常的公网域名，Center 已连接对应的 Cloudflare 区域用于签发证书。
- 防火墙、安全组允许 UDP 443。VLESS 仍使用 HAProxy 的 TCP 443，二者不冲突。
- 若 UDP 443 已被独立 HY2 测试服务或其他应用占用，先自行处理冲突；
  Vastora 不会停止、删除或接管其他服务。
- 添加 HY2 前，已使用落地机的节点先切到“本机出口”。协议保存后再选择
  落地机，路由同时包含两个入站；协议调整期间禁止并行修改出口。

## 状态与边界

- 每个节点签发独立的域名证书，不向节点分发 Center 或 Site 的通配符私钥。
  证书加密保存，由现有证书维护任务续期；续期提交与节点确认分开记录。
- 新增 HY2 只关联已经分配给该节点的客户端，凭据由 3x-ui 生成。
  关闭 HY2 保留凭据，重新开启不会创建重复入站。
- HY2 使用 3x-ui 的全局客户端套餐。原“VLESS 节点套餐”只统计 VLESS，
  不复制成另一份 HY2 额度，也不表示两种协议共享该节点额度。
- 更新分为端口准备、订阅主机配置、目标节点读回三个阶段，各阶段使用
  独立任务 ID。失败保留具体阶段，重试不会重放已经确认完成的前序阶段。
- 启停 UDP 映射使用现有容器替换与回滚机制，会短暂重启该节点的 3x-ui。
  应用升级保留显式启用的 UDP 映射。
- HY2-only 不执行 VLESS 的 TCP/TLS 健康检查。“HY2 已配置”表示目标节点
  配置读回一致，不代表已经从用户网络完成 QUIC 连通或性能测试。

数据库升级使用前向迁移 70，并复用升级前备份流程；不支持降级数据库。

本次新增了 Go 与界面测试用例，但按仓库要求未在本地执行测试、构建或浏览器验证。
