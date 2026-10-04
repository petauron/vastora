import { useEffect, useState, type ReactNode } from "react";
import { CheckIcon, CircleAlertIcon, CircleCheckIcon, Clock3Icon, CopyIcon, ShieldAlertIcon, UnplugIcon } from "lucide-react";
import type { Language } from "../translations";
import type { AppView } from "../types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

export const copy = (language: Language, zh: string, en: string) => language === "zh-CN" ? zh : en;

// The server remains authoritative; also stop stale open forms at their known
// expiry instead of waiting for the next dashboard refresh.
export function catalogInstallBlocked(app?: AppView) {
  if (!app || app.installBlocked) return true;
  if (!app.catalogExpiresAt) return false;
  const expiry = Date.parse(app.catalogExpiresAt);
  return !Number.isFinite(expiry) || expiry <= Date.now();
}

export function userError(language: Language, error: unknown) {
  const code = error && typeof error === "object" && "code" in error && typeof error.code === "string" ? error.code : "";
  const detail = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  const normalized = detail.toLowerCase();
  const reinstallErrors: Record<string, string> = {
    "center: current authenticated client verification is incomplete": "真实客户端验收未通过或已过期，请查看验收结果并明确重新验证。",
    "center: real client verification is required": "请先完成真实客户端验收。",
    "center: an original client identity has no current verification": "部分原有账号或协议尚无有效验收结果，请重新验证全部线路。",
    "center: client verification is still running or requires inspection": "客户端验证仍在执行或结果不明确，请先核对该任务。",
    "center: fresh authenticated runtime observation is required": "运行时上报已过期，请等待新机器上报后刷新。",
    "center: landing transport authorization has expired; wait for a fresh observation": "落地连接授权已过期，请等待新鲜运行时上报。",
    "center: fresh reports from the original monitoring identity are required": "请先验证原监控身份的新鲜上报。",
    "center: current shared entry verification is required": "请先重新验证共享入口的解析和 TLS。",
    "center: current entry DNS verification is required": "请先核对入口 DNS；手动 DNS 需通过入口验证。",
    "center: business data restoration has not been verified": "业务数据尚未恢复并验证，不能完成全部恢复。",
    "center: an application has no verified recovery procedure": "部分应用尚无经过验证的恢复方式，请先处理恢复清单。",
    "center: a separate online verifier is required": "请选择另一台在线且支持自动验收的节点。",
    "center: inspect the outstanding client check before starting another request": "已有客户端验证尚未结束或结果不明，请先核对，不要重复发起。",
    "center: fresh exit evidence is required": "出口上报已过期，请等待相关节点重新上报。",
    "center: selected landing exit is unconfirmed": "当前选择的落地出口尚未确认，请核对出口配置。",
    "center: usage changed restored access requirements; review runtime before completion": "最新用量改变了访问权限，请核对运行配置后再完成恢复。",
    "center: resolve outstanding work before completing recovery": "请先处理未完成或结果不明的历史任务。",
    "center: restored application runtime and reviewed access are required": "请先恢复应用运行配置并启用已确认的访问地址。",
    "center: current replacement network approval is required": "请先确认当前新机器的网络地址。",
    "center: an uninstalled application still has running state": "应保持卸载的应用仍显示运行中，请核对后再完成恢复。",

    "center: authenticated previous private identity evidence is missing": "缺少旧机器已认证的私网身份记录，无法安全撤销旧身份。恢复已暂停。",
    "center: previous private network is externally managed; verify its isolation before recovery": "旧私网由外部管理，请先核对旧机器的访问权限是否已撤销。",
    "center: previous private address is assigned to another node": "原私网地址已属于另一节点，恢复已暂停。",
    "center: private controller is unavailable; recovery remains paused": "私网控制面不可用，恢复已暂停。",
    "center: private controller changed after recovery authorization": "确认后私网控制面发生变化，请核对后再继续。",
    "center: private identity inspection failed; recovery remains paused": "无法核对旧私网身份，恢复已暂停。",
    "center: previous private identity or address ownership changed; recovery remains paused": "旧私网身份或地址归属发生变化，恢复已暂停。",
    "center: private identity withdrawal was not confirmed; inspect the saved identity before continuing": "未收到旧私网身份撤销的确认，请核对已保存的身份记录后再继续。",
    "center: private identity withdrawal could not be verified; recovery remains paused": "无法验证旧私网身份是否已撤销，恢复已暂停。",
    "center: previous private identity or address is still present; recovery remains paused": "旧私网身份或地址仍在使用，恢复已暂停。",
    "center: previous private identity must be isolated before command preparation": "先完成旧私网身份隔离，再生成接入命令。",
    "center: command preparation stopped; inspect recovery before continuing": "接入命令准备已停止，请先核对恢复进度。",
    "center: recovery progress changed or command preparation already started; refresh before continuing": "恢复进度已变化，或已进入接入命令准备阶段。请刷新状态，勿重复操作。",
    "center: explicitly confirm private identity inspection before continuing": "请明确确认核对旧私网身份，再继续隔离。",
    "center: review address migration through the active reinstall recovery before confirming network settings": "节点正在重装恢复，请先在恢复流程中核对地址迁移。",
    "center: explicitly confirm the reviewed old-machine executions": "请核对旧本机执行记录，再明确确认终止。",
    "center: old-machine isolation and authorized replacement enrollment are required before settlement": "先完成旧机器隔离和新机器接入，再处理旧本机执行。",
    "center: replacement identity changed; review recovery before continuing": "新机器身份已变化，请刷新并核对恢复状态。",
    "center: previous execution evidence changed; inspect it before settlement": "旧执行证据已变化，请核对后再处理。",
    "center: previous task attempt changed; inspect it before settlement": "旧任务的执行状态已变化，请到活动记录核对后再处理。",
    "center: no isolated local executions are eligible for settlement": "当前没有可统一处理的旧本机执行，请刷新状态。",
  };
  if (reinstallErrors[normalized]) return copy(language, reinstallErrors[normalized], detail);
  if (normalized === "center: recovery plan changed; review it again before confirming") {
    return copy(language, "恢复清单已变化，请刷新状态后重新确认。", detail);
  }
  if (normalized === "center: recovery already started; inspect its saved progress before continuing") {
    return copy(language, "恢复操作已开始，请刷新并核对已保存的进度，避免重复操作。", detail);
  }
  if (normalized === "center: recovery command is no longer valid; review the node again") {
    return copy(language, "接入命令已失效，请重新核对节点的恢复状态。", detail);
  }
  if (normalized === "center: disconnect the agent before generating a reconnect command") {
    return copy(language, "节点仍在线，请先确认原机器已停止接入。", detail);
  }
  if (normalized === "center: restore the native meridian runtime and wait for a fresh authenticated private identity report before recovering landing access") {
    return copy(language, "请先恢复本机 Meridian，并等待节点上报最新私网身份，再恢复落地线路。", detail);
  }
  if (normalized === "center: this meridian entry has no replacement private identity to recover") {
    return copy(language, "当前私网身份与原授权一致，无需进行重装恢复。", detail);
  }
  if (normalized === "center: meridian replacement identity or configuration changed; inspect it again before confirming") {
    return copy(language, "节点身份或配置已变化，请重新检查并确认。", detail);
  }
  if (normalized === "center: settle active or uncertain executions on the entry and affected landings before identity recovery") {
    return copy(language, "线路机或相关落地机仍有未完成任务，请先在活动页面处理。", detail);
  }
  if (normalized === "center: wait for the affected landing services to finish their current authorization update") {
    return copy(language, "相关落地机仍在更新授权，请等待完成后重试。", detail);
  }
  if (normalized === "center: wait for the landing services to confirm withdrawal of the previous entry identity") {
    return copy(language, "正在撤回原节点的落地权限，请等待落地机确认后重试。", detail);
  }
  if (code === "execution_blocked") return copy(language, "相关节点有待处理任务，请处理后重试。", "A related node has unresolved tasks. Resolve them before retrying.");
  if (code === "landing_pool_revision_changed") return copy(language, "全局落地池已变化，请刷新后重试。", "The global landing pool changed. Refresh before retrying.");
  if (normalized === "center: resolve outstanding execution and runtime recovery before updating") {
    return copy(language, "节点仍有待处理任务或升级恢复状态，请处理后再更新。", "Resolve the node's outstanding task or runtime recovery before updating.");
  }
  if (normalized === "center: confirm the exact failed update is stopped and record recovery verification") {
    return copy(language, "升级状态已变化，请刷新并重新确认。", "The update state changed. Refresh and confirm again.");
  }
  if (normalized === "center: task execution is paused; resume task execution before changing exits") {
    return copy(language, "任务已暂停，恢复后才能切换出口。", "Tasks are paused. Resume them before changing exits.");
  }
  if (normalized === "center: a related node has an unresolved execution; resolve it before changing exits") {
    return copy(language, "相关节点有待处理任务，处理后才能切换出口。", "A related node has a task needing attention before its exit can change.");
  }
  if (normalized === "center: remove this landing server's account routes and wait for revocation before removing the server") {
    return copy(language, "请先移除使用这台落地机的账号路由，等待授权撤销完成后再移除落地机。", "Remove the account routes using this exit and wait for revocation before removing the server.");
  }
  if (normalized === "center: landing selection changed; refresh and retry") {
    return copy(language, "落地配置已变化，请刷新状态后重试。", "Landing settings changed. Refresh and retry.");
  }
  if (normalized === "refresh the app catalog and retry this operation.") {
    return copy(language, "请先在设置中刷新应用目录，再重试。", "Refresh the app catalog in Settings, then retry.");
  }
  if (code === "protocols_need_own_exit") return copy(language, "请先切换为“本机出口”，启用 HY2 后再选择落地机。", "Switch to the node's own exit, enable HY2, then select the landing server again.");
  if (code === "protocols_need_domain") return copy(language, "请先配置节点的公网地址，再修改协议。", "Set up the node's public address before changing protocols.");
  if (code === "protocols_domain_in_use") return copy(language, "HY2 正在使用这个地址，请先关闭 HY2 再移除。", "Disable HY2 before removing its public address.");
  if (code === "node_operation_busy") return copy(language, "请等待当前节点操作完成后再试。", "Wait for the current node operation to finish.");
  if (code === "node_remove_online") return copy(language, "节点已上线。永久移除仅用于不再使用的离线节点。", "The node is online. Permanent removal is only for retired offline nodes.");
  if (code === "node_remove_confirmation") return copy(language, "节点名称不一致，请核对后重试。", "The node name does not match. Check it and try again.");
  if (code === "node_remove_shared") return copy(language, "这个节点仍提供共享服务，请先更换订阅主机、落地机或访问入口，再移除节点。", "This node provides a shared service. Move its subscription controller, landing service or access entry before removing it.");
  if (code === "node_disable_in_use") return copy(language, "节点仍有关联应用或入口。如果已到期且不再使用，请选择“永久移除”。", "This node still has apps or access entries. For an expired node, choose Permanently remove.");
  if (code === "node_delete_requires_disabled") {
    return copy(language, "请先停用节点，再删除。", "Disable the node before deleting it.");
  }
  if (code === "node_delete_in_use") {
    return copy(language, "节点仍有关联服务或未完成的操作，请先解除应用、入口和落地机的关联，并等待操作完成。", "Remove the node from apps, access entries and landing services, and wait for unfinished operations to complete.");
  }
  if (code === "authentication_required" || normalized.includes("authentication required") || normalized.includes("unauthorized") || normalized.includes("session expired")) {
    return copy(language, "登录状态已失效，请重新登录后再试。", "Your session has expired. Sign in and try again.");
  }
  if (normalized.includes("invalid credentials") || normalized.includes("incorrect password")) {
    return copy(language, "账号或密码不正确，请重新输入。", "The username or password is incorrect. Try again.");
  }
  if (code === "dns_record_conflict" || normalized.includes("dns record") && normalized.includes("already exists")) {
    return copy(language, "这个地址已有指向其他服务器的 DNS 记录。Vastora 没有覆盖它；请更换地址或先处理现有记录。", "This hostname already points to another server. Vastora did not overwrite it; choose another hostname or update the existing record first.");
  }
  if (normalized.includes("verify headscale") && (normalized.includes("lookup") || normalized.includes("no such host"))) {
    return copy(language, "安全私网地址的 DNS 尚未生效，系统没有保存未完成的设置。请稍后重试。", "DNS for the secure private-network address is not available yet. The incomplete setup was not saved; try again shortly.");
  }
  if (code === "already_installed" || code === "conflict" || normalized.includes("already installed") || normalized.includes("already exists") || normalized.includes("conflict")) {
    return copy(language, "已有相同配置，请刷新页面后检查当前状态。", "This is already configured. Refresh and check its current status.");
  }
  if (normalized.includes("timeout") || normalized.includes("timed out") || normalized.includes("deadline exceeded") || normalized.includes("did not respond in time")) {
    return copy(language, "操作等待超时，系统可能仍在后台处理。请稍后刷新后重试。", "The operation timed out and may still be running. Refresh shortly, then retry.");
  }
  if (normalized.includes("failed to fetch") || normalized.includes("networkerror") || normalized.includes("network error") || normalized.includes("live connection")) {
    return copy(language, "无法连接 Center，请检查网络后重试。", "Could not reach Center. Check the network and try again.");
  }
  if (code === "cloudflare_error" || normalized.includes("cloudflare")) {
    return copy(language, "Cloudflare 操作未完成，请检查授权和域名配置后重试。", "The Cloudflare operation did not complete. Check authorization and domain settings, then retry.");
  }
  if (code === "gateway_unavailable" || normalized.includes("gateway") || normalized.includes("no eligible node")) {
    return copy(language, "当前没有可用的入口节点，请先检查节点是否在线并完成网络确认。", "No entry node is available. Check that a node is online and its network is confirmed.");
  }
  if (code === "deployment_rejected") return copy(language, "升级请求在创建任务前被 Center 拒绝，未改动节点。具体原因已记录到 Center 日志。", "Center rejected the upgrade before creating a task, so the node was not changed. The exact reason was recorded in the Center journal.");
  if (code === "forbidden") return copy(language, "当前账号没有执行此操作的权限。", "Your account does not have permission to perform this operation.");
  if (code === "not_found") return copy(language, "目标已不存在，请刷新页面后重试。", "This item no longer exists. Refresh and try again.");
  if (code === "invalid_request") return copy(language, "填写内容不完整或格式不正确，请检查后重试。", "Some entries are missing or invalid. Check them and try again.");
  if (code === "internal_error") return copy(language, "Center 暂时无法完成此操作，请稍后重试。", "Center could not complete the operation. Try again shortly.");
  return copy(language, "操作未完成，请检查填写内容后重试。", "The operation did not complete. Check your entries and try again.");
}

// Task failures are not form validation failures. Keep raw diagnostics in details.
export function taskError(language: Language, error?: string) {
  const value = (error ?? "").toLowerCase();
  if (value.includes("private identity changed before handover")) return copy(language, "节点重新接入后，连接身份已变化，这次落地配置未完成。请到应用中核对线路与落地机，再重新配置。", "The node rejoined with a new identity, so this exit configuration did not complete. Review its entry and exit connection in Apps before configuring it again.");
  if (value.includes("deadline exceeded") || value.includes("timeout") || value.includes("timed out")) return copy(language, "节点没有在规定时间内返回结果。请先检查节点是否在线，并核对任务是否已完成。", "The node did not return a result in time. Check whether it is online and whether the task actually completed.");
  if (value.includes("offline") || value.includes("not connected")) return copy(language, "节点暂时离线。请先恢复节点连接，再检查这项任务。", "The node is offline. Restore its connection, then review this task.");
  return copy(language, "任务未能完成。请先查看对应节点或应用的状态；技术详情可用于进一步排查。", "The task did not complete. Check the affected node or app; technical details are available for troubleshooting.");
}

export function TechnicalError({ language, error }: { language: Language; error: unknown }) {
  const detail = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  return <div className="text-sm text-destructive"><p>{userError(language, error)}</p>{detail ? <details className="mt-1"><summary className="cursor-pointer text-xs font-medium">{copy(language, "查看技术详情", "Technical details")}</summary><code className="mt-2 block break-all text-xs">{detail}</code></details> : null}</div>;
}

export function CopyButton({ language, value, label, className, size = "sm", variant = "outline" }: { language: Language; value: string; label?: string; className?: string; size?: "sm" | "icon" | "icon-sm"; variant?: "outline" | "ghost" }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  useEffect(() => {
    if (state === "idle") return;
    const timer = window.setTimeout(() => setState("idle"), 2000);
    return () => window.clearTimeout(timer);
  }, [state]);
  const text = state === "copied" ? copy(language, "已复制", "Copied") : state === "failed" ? copy(language, "复制失败", "Copy failed") : label ?? copy(language, "复制", "Copy");
  const iconOnly = size === "icon" || size === "icon-sm";
  return <Button aria-label={text} className={className} onClick={async () => { try { await navigator.clipboard.writeText(value); setState("copied"); } catch { setState("failed"); } }} size={size} type="button" variant={variant}>{state === "copied" ? <CheckIcon aria-hidden="true" data-icon={iconOnly ? undefined : "inline-start"} /> : <CopyIcon aria-hidden="true" data-icon={iconOnly ? undefined : "inline-start"} />}{iconOnly ? <span className="sr-only" aria-live="polite">{text}</span> : text}</Button>;
}

export function Brand() {
  return <div className="flex items-center gap-2.5 font-semibold tracking-tight"><span className="grid size-8 place-items-center rounded-lg bg-primary text-primary-foreground shadow-xs" aria-hidden="true"><svg className="size-4" viewBox="0 0 24 24"><path fill="currentColor" d="M12 2.5 20 7v10l-8 4.5L4 17V7l8-4.5Zm0 3.1L7.1 8.4v7.2l4.9 2.8 4.9-2.8V8.4L12 5.6Zm-3 4.1 3 1.7 3-1.7v3.5L12 15l-3-1.8V9.7Z" /></svg></span><span>Vastora</span></div>;
}

export function PageHeading({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  return <div className="flex flex-col gap-3 sm:flex-row sm:items-start"><div className="flex min-w-0 flex-1 flex-col gap-1"><h1 className="text-balance text-2xl font-semibold tracking-tight">{title}</h1>{description ? <p className="max-w-2xl text-sm leading-6 text-muted-foreground">{description}</p> : null}</div>{action}</div>;
}

export function StateBadge({ value, language = document.documentElement.lang === "zh-CN" ? "zh-CN" : "en" }: { value: string; language?: Language }) {
  const good = ["ready", "running", "succeeded", "configured", "connected", "active", "healthy"].includes(value);
  const bad = ["failed", "degraded", "offline", "lease_expired", "recovery", "expired", "removal_failed"].includes(value);
  const Icon = value === "access_stopped" ? UnplugIcon : good ? CircleCheckIcon : bad ? CircleAlertIcon : Clock3Icon;
  const labels: Record<string, [string, string]> = {
    removing: ["正在移除", "Removing"],
    deploying: ["正在处理", "In progress"],
    removal_failed: ["移除未完成", "Removal incomplete"],
    access_stopped: ["已停止接入", "Access stopped"],
    expired: ["需刷新", "Refresh required"],
    superseded: ["已被替代", "Superseded"],
    ready: ["就绪", "Ready"], running: ["运行中", "Running"], succeeded: ["成功", "Succeeded"], configured: ["已配置", "Configured"], connected: ["已连接", "Connected"], active: ["正常", "Active"], healthy: ["健康", "Healthy"], stale: ["使用缓存", "Using cache"],
    failed: ["失败", "Failed"], degraded: ["异常", "Degraded"], recovery: ["需恢复", "Recovery needed"], offline: ["离线", "Offline"], lease_expired: ["已重试", "Retried"], pending: ["等待中", "Pending"], applying: ["配置中", "Applying"], blocked: ["落地不可用", "Egress unavailable"], stopped: ["已停止", "Stopped"], disabled: ["未启用", "Disabled"], unconfigured: ["未配置", "Not configured"], queued: ["已排队", "Queued"], claimed: ["执行中", "In progress"]
  };
  const label = labels[value];
  return <Badge variant={bad ? "destructive" : good ? "secondary" : "outline"}><Icon data-icon="inline-start" />{label ? copy(language, ...label) : value}</Badge>;
}

export function HighPrivilegeBadge({ language }: { language: Language }) {
  return <Badge variant="destructive"><ShieldAlertIcon data-icon="inline-start" />{copy(language, "高权限", "Privileged")}</Badge>;
}

export function formatDate(language: Language, value?: string) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(language, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}
