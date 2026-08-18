import { HistoryIcon } from "lucide-react";
import type { AgentView, Action } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, copy, formatDate } from "./shared";
import { Card, CardContent } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export function ActivityView({ actions, agents, language }: { actions: Action[]; agents: AgentView[]; language: Language }) {
  const agentNames = new Map(agents.map((agent) => [agent.id, agent.name]));
  return <section className="flex flex-col gap-7"><PageHeading title={copy(language, "活动", "Activity")} description={copy(language, "安装和网络配置会自动更新；失败时这里会保留原因。", "Install and network operations update automatically. Failure details remain here.")} />{actions.length === 0 ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><HistoryIcon /></EmptyMedia><EmptyTitle>{copy(language, "还没有活动记录", "No activity yet")}</EmptyTitle><EmptyDescription>{copy(language, "创建安装或发布任务后，进度会显示在这里。", "Progress appears here after an install or publication task is created.")}</EmptyDescription></EmptyHeader></Empty> : <Card aria-live="polite"><CardContent className="p-0"><Table><TableHeader><TableRow><TableHead>{copy(language, "状态", "Status")}</TableHead><TableHead>{copy(language, "操作", "Action")}</TableHead><TableHead>{copy(language, "节点", "Node")}</TableHead><TableHead>{copy(language, "详情", "Details")}</TableHead><TableHead className="text-right">{copy(language, "时间", "Time")}</TableHead></TableRow></TableHeader><TableBody>{actions.map((action) => <TableRow key={action.id}><TableCell><StateBadge language={language} value={action.event} /></TableCell><TableCell><p className="font-medium">{actionKind(language, action.kind)}</p><p className="mt-0.5 font-mono text-xs text-muted-foreground">r{action.revision}</p></TableCell><TableCell>{agentNames.get(action.agentId) ?? action.agentId}</TableCell><TableCell className="max-w-sm whitespace-normal text-muted-foreground">{actionMessage(language, action.message) || "—"}</TableCell><TableCell className="text-right text-muted-foreground">{formatDate(language, action.createdAt)}</TableCell></TableRow>)}</TableBody></Table></CardContent></Card>}</section>;
}

export function actionKind(language: Language, kind: string) {
  const labels: Record<string, [string, string]> = {
    "application.apply": ["应用变更", "Application change"],
    "gateway.component.apply": ["安装或更新网关", "Install or update gateway"],
    "gateway.routes.apply": ["同步服务入口", "Synchronize service access"],
    "tunnel.state.apply": ["同步 Cloudflare Tunnel", "Synchronize Cloudflare Tunnel"]
  };
  return labels[kind] ? copy(language, ...labels[kind]) : kind;
}

export function actionMessage(language: Language, message?: string) {
  if (!message) return "";
  const operation = /^(install|upgrade|uninstall) (.+)$/.exec(message);
  if (operation) {
    const labels: Record<string, [string, string]> = { install: ["安装", "Install"], upgrade: ["升级", "Upgrade"], uninstall: ["卸载", "Uninstall"] };
    return `${copy(language, ...labels[operation[1]])} ${operation[2]}`;
  }
  if (message === "task lease expired; queued for retry") return copy(language, "节点响应超时，系统已自动重新排队。", "The node timed out and the operation was queued for retry.");
  if (message === "gateway health check failed; queued for reconcile") return copy(language, "网关健康检查失败，系统正在自动修复。", "The gateway health check failed and automatic recovery was queued.");
  return message;
}
