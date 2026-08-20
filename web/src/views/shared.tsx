import { useEffect, useState, type ReactNode } from "react";
import { CheckIcon, CircleAlertIcon, CircleCheckIcon, Clock3Icon, CopyIcon, ShieldAlertIcon } from "lucide-react";
import type { Language } from "../translations";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

export const copy = (language: Language, zh: string, en: string) => language === "zh-CN" ? zh : en;

export function userError(language: Language, error: unknown) {
  const code = error && typeof error === "object" && "code" in error && typeof error.code === "string" ? error.code : "";
  const detail = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  const normalized = detail.toLowerCase();
  if (code === "authentication_required" || normalized.includes("authentication required") || normalized.includes("unauthorized")) {
    return copy(language, "登录状态已失效，请重新登录后再试。", "Your session has expired. Sign in and try again.");
  }
  if (normalized.includes("invalid credentials") || normalized.includes("incorrect password")) {
    return copy(language, "账号或密码不正确，请重新输入。", "The username or password is incorrect. Try again.");
  }
  if (code === "dns_record_conflict" || normalized.includes("dns record") && normalized.includes("already exists")) {
    return copy(language, "这个地址已有指向其他服务器的 DNS 记录。Vastora 没有覆盖它；请更换地址或先处理现有记录。", "This hostname already points to another server. Vastora did not overwrite it; choose another hostname or update the existing record first.");
  }
  if (code === "already_installed" || code === "conflict" || normalized.includes("already installed") || normalized.includes("already exists") || normalized.includes("conflict")) {
    return copy(language, "已有相同配置，请刷新页面后检查当前状态。", "This is already configured. Refresh and check its current status.");
  }
  if (normalized.includes("timeout") || normalized.includes("timed out") || normalized.includes("deadline exceeded")) {
    return copy(language, "操作等待超时，系统可能仍在后台处理。请稍后刷新后重试。", "The operation timed out and may still be running. Refresh shortly, then retry.");
  }
  if (normalized.includes("failed to fetch") || normalized.includes("networkerror") || normalized.includes("network error")) {
    return copy(language, "无法连接 Center，请检查网络后重试。", "Could not reach Center. Check the network and try again.");
  }
  if (code === "cloudflare_error" || normalized.includes("cloudflare")) {
    return copy(language, "Cloudflare 操作未完成，请检查授权和域名配置后重试。", "The Cloudflare operation did not complete. Check authorization and domain settings, then retry.");
  }
  if (code === "gateway_unavailable" || normalized.includes("gateway") || normalized.includes("no eligible node")) {
    return copy(language, "当前没有可用的入口节点，请先检查节点是否在线并完成网络确认。", "No entry node is available. Check that a node is online and its network is confirmed.");
  }
  if (code === "forbidden") return copy(language, "当前账号没有执行此操作的权限。", "Your account does not have permission to perform this operation.");
  if (code === "not_found") return copy(language, "目标已不存在，请刷新页面后重试。", "This item no longer exists. Refresh and try again.");
  if (code === "invalid_request") return copy(language, "填写内容不完整或格式不正确，请检查后重试。", "Some entries are missing or invalid. Check them and try again.");
  if (code === "internal_error") return copy(language, "Center 暂时无法完成此操作，请稍后重试。", "Center could not complete the operation. Try again shortly.");
  return copy(language, "操作未完成，请检查填写内容后重试。", "The operation did not complete. Check your entries and try again.");
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
  const good = ["ready", "running", "succeeded", "configured", "connected", "active"].includes(value);
  const bad = ["failed", "degraded", "offline", "lease_expired"].includes(value);
  const Icon = good ? CircleCheckIcon : bad ? CircleAlertIcon : Clock3Icon;
  const labels: Record<string, [string, string]> = {
    ready: ["就绪", "Ready"], running: ["运行中", "Running"], succeeded: ["成功", "Succeeded"], configured: ["已配置", "Configured"], connected: ["已连接", "Connected"], active: ["正常", "Active"],
    failed: ["失败", "Failed"], degraded: ["异常", "Degraded"], offline: ["离线", "Offline"], lease_expired: ["已重试", "Retried"], pending: ["等待中", "Pending"], applying: ["配置中", "Applying"], stopped: ["已停止", "Stopped"], disabled: ["未启用", "Disabled"], unconfigured: ["未配置", "Not configured"], queued: ["已排队", "Queued"], claimed: ["执行中", "In progress"]
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
