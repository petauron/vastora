# Cloudflare Access 会话时长（#365）

## 使用入口

网络 → Center 远程备用入口 → 修改 → 入口保护选择 Cloudflare Access。
“Access 会话时长”默认 24 小时，可选择 15/30 分钟、1/6/12/24 小时、2/3/7/30 天。
这是 Cloudflare 支持范围内的固定选项，不接受任意字符串、零、负数或超范围值。
直达 Center 登录（native）模式不显示该选项，也不修改已保存的 Access 设置。

保存后显示上次同步的入口数量和未完成的域名。发生拒绝、超时或部分失败时，
保持原来的入口保护，再次“保存并同步”即可重试，不需要先关闭入口。
页面关闭或 Center 重启后仍保留所选时长和同步结果；中途退出的 pending 状态明确提示重试，
不会把它描述成已经成功。接口返回的 `accessSessionSync.status` 是同步结果，
不能只用 HTTP 200 或入口自身的 `configured` 状态判断全部同步成功。

## 配置与同步边界

- 既有 `PUT /api/v1/network/center-remote-access` 接收 `accessSessionDuration`；GET/PUT 返回该值及 `accessSessionSync`。
- `cloudflare_access_settings` 单例保存系统期望值和上次同步结果，独立于可删除的 Center 入口记录。
- 迁移 66 为旧安装补默认值，不对 Cloudflare 发请求、不改变既有资源 ID；沿用升级前备份和失败关闭流程，不支持降级。
- 仅修改时长时，Center 与未停止的托管应用入口均按数据库保存的 Access application ID 原地更新。
  不搜索账户中的同名应用、不删除/重建应用、不修改 Tunnel 或 DNS。
- GET 读取并检查应用身份，保留应用其他设置和原有策略关联，PUT 只改变应用会话时长。
  API 返回确认的 ID 和时长才计为成功；失败保留期望值和未完成入口。重试时读取已达到目标的应用但不重复 PUT。
- 分页检查应用策略；发现显式策略会话时长，保留并提示管理员在 Cloudflare 中选择“与应用相同”。
  不改独立策略、可复用策略、邮箱允许列表、IdP 或服务令牌。
- 保存操作与新建托管 Access 应用串行，避免新入口在同步快照之后沿用旧时长。
  新建 Center/应用入口直接使用保存的期望值。
- 同步总预算 45 秒，每个应用预算 8 秒；失败域名保留在返回值与数据库，具体原因仅写服务端日志。

## 会话语义

此设置只管理 **应用会话**。不改变 Cloudflare 账户的全局 SSO 会话、Cloudflare One Client
会话，也不改变 Vastora 自身的登录有效期。显式策略及 Client 会话可能优先。
应用令牌到期时，如果全局登录仍有效且仍满足策略，Cloudflare 可以续发应用令牌；
所以不能承诺到期后必定重新输入邮箱验证码。

参考：[Cloudflare 会话管理](https://developers.cloudflare.com/cloudflare-one/access-controls/access-settings/session-management/)、
[更新 Access 应用 API](https://developers.cloudflare.com/api/resources/zero_trust/subresources/access/subresources/applications/methods/update/)。

## 验证记录与上线验收

已编写但本轮未运行：默认值/持久化/校验、65→66 迁移和备份、创建时长、
已有入口原地同步、无关设置保留、策略覆盖提示、部分失败重试、超时/拒绝/取消、
错误身份、未确认更新，以及前端默认值、模式隐藏、错误和重试交互测试。
仓库要求显式授权后才能运行本地测试、构建、类型检查和浏览器 QA。

尚未执行真实 Cloudflare 账户验收。获准后应在受控入口上：

1. 记录全局 SSO、应用、策略的原值；保存较短应用时长，核对 Center 和托管入口。
2. 确认未触及无关应用、Tunnel、IdP 和策略内容；新建入口继承该时长。
3. 验证应用会话过期而全局会话仍有效时的续发行为。
4. 在不影响正常用户的测试会话中验证全局会话过期后的重新认证。
5. 对独立策略和 Cloudflare One Client 场景核对提示，不将 API 同步成功当作登录行为验收。
