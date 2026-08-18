import type { ReactNode } from "react";
import { CircleAlertIcon, CircleCheckIcon, Clock3Icon, ShieldAlertIcon } from "lucide-react";
import type { Language } from "../translations";
import { Badge } from "@/components/ui/badge";

export const copy = (language: Language, zh: string, en: string) => language === "zh-CN" ? zh : en;

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
