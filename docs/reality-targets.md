# REALITY 目标选择（#352）

Vastora 不再预填一个未经当前节点检查的固定目标。创建时由**实际 VLESS 节点**
检查下面的有限候选集合，显示可用结果，由管理员选择；高级设置仍可检查其他
`.com` 网站。自动检查不会自动创建入站或更换既有入站的目标。

## 候选来源

2026-09-08 根据运营方的官方产品页面整理：

| 域名 | 官方来源 |
| --- | --- |
| `www.bing.com` | [Microsoft Bing](https://support.microsoft.com/en-us/bing/microsoft-bing-help) |
| `www.google.com` | [Google 产品](https://about.google/intl/en-GB/products/) |
| `www.yahoo.com` | [Yahoo Search](https://search.yahooinc.com/) |
| `www.amazon.com` | [About Amazon](https://www.aboutamazon.com/about-us) |

这些来源只说明网站与运营方的关系，**不是安全背书或可用性保证**。网站的解析、
CDN/WAF、协议和访问策略会变化；候选可能全部不通过。增加候选应同时更新
`internal/realitytarget/candidate.go`、本表和来源日期，不接受远程任意候选列表。

## 检查与选择

- 只连接 DNS 返回的公网 IPv4；拒绝私网、回环、链路本地及共享地址空间。
- 沿用 CDN/WAF 边界检查和 exact-SNI TLS 1.3 / X25519 / h2 / 证书检查。
- 对通过的固定 IP 再采样两次，记录三次平均时延。ASN 只作排序提示，缺失不会
  被伪装为同 ASN；已知同 ASN 优先，其次样本数、时延和确定性顺序。
- 整个推荐操作有 75 秒总限时、每个候选有 60 秒限时，最多四个候选并行。
  这不是端口扫描，也不是持续后台探测。
- 创建引用 15 分钟内同一应用、节点身份及网络资料的成功检查，必须使用管理员
  选中的域名、SNI 和固定 IP。创建过程不在 Center 或订阅主机重新解析替换它。
- 无可用结果时可重试或手动检查其他目标；不会静默恢复默认域名。

## 既有服务

不再在 Center 启动或每六小时把已就绪入口隔离。只有未完成的安全修复继续恢复；
管理员仍可主动检查或修改目标。相同 HAProxy 配置在运行时保留原容器，恢复网络
或进程不等于重建共享入口。

固定 IP 和一次检查不能证明永久安全。HAProxy 的外层 SNI 过滤不能验证加密后的
HTTP Host；REALITY 未认证回落边界和已有安全拒绝规则仍须保留。这里不承诺
“不会被偷流量”、零中转或抗 DDoS。参考 [REALITY 上游](https://github.com/XTLS/REALITY)
及 [Xray 传输配置](https://xtls.github.io/config/transport.html)。

代码配有回归用例；本轮按仓库约定未运行本地测试、构建或线上探测。
