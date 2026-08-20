import { HistoryIcon } from "lucide-react";
import type { Action, AgentView } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, copy, formatDate, userError } from "./shared";
import { Badge } from "@/components/ui/badge";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";

const visibleEventLimit = 100;

export function ActivityView({ actions, agents, language }: { actions: Action[]; agents: AgentView[]; language: Language }) {
  const agentNames = new Map(agents.map((agent) => [agent.id, agent.name]));
  const visibleActions = actions.slice(0, visibleEventLimit);
  const groups = groupActions(visibleActions);
  return (
    <section className="flex flex-col gap-7">
      <PageHeading title={copy(language, "活动", "Activity")} description={copy(language, "每次安装或网络变更只显示为一项操作；展开后可查看执行步骤。", "Each install or network change appears as one operation. Expand it to see execution steps.")} />
      {actions.length > visibleEventLimit ? <p className="text-xs text-muted-foreground">{copy(language, `显示最近 ${visibleEventLimit} 条事件，已按操作合并。`, `Showing the latest ${visibleEventLimit} events, grouped by operation.`)}</p> : null}
      {groups.length === 0 ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><HistoryIcon /></EmptyMedia><EmptyTitle>{copy(language, "还没有活动记录", "No activity yet")}</EmptyTitle><EmptyDescription>{copy(language, "创建安装或访问任务后，进度会显示在这里。", "Progress appears here after an install or access operation is created.")}</EmptyDescription></EmptyHeader></Empty> : (
        <div aria-live="polite" className="flex flex-col gap-3">
          {groups.map((group) => {
            const latest = group.actions[0];
            const message = visibleActionMessage(language, latest);
            return (
              <Card key={group.taskId} size="sm">
                <CardHeader><CardTitle>{message || actionKind(language, latest.kind)}</CardTitle><CardDescription>{agentNames.get(latest.agentId) ?? copy(language, "未知节点", "Unknown node")} · {formatDate(language, latest.createdAt)}</CardDescription><CardAction><StateBadge language={language} value={latest.event} /></CardAction></CardHeader>
                <CardContent>
                  {message && message !== actionKind(language, latest.kind) ? <p className="text-sm text-muted-foreground">{actionKind(language, latest.kind)}</p> : null}
                  <details className="mt-3 rounded-lg border p-3 text-xs text-muted-foreground">
                    <summary className="cursor-pointer font-medium text-foreground">{copy(language, `技术详情与 ${group.actions.length} 个步骤`, `Technical details and ${group.actions.length} step(s)`)}</summary>
                    <dl className="mt-3 grid gap-2 sm:grid-cols-[7rem_1fr]"><dt>{copy(language, "任务 ID", "Task ID")}</dt><dd className="break-all font-mono">{group.taskId}</dd><dt>{copy(language, "最新修订", "Latest revision")}</dt><dd><Badge variant="outline">r{latest.revision}</Badge></dd></dl>
                    <ol className="mt-3 flex flex-col gap-2 border-t pt-3">{group.actions.map((action) => <li className="grid gap-1 sm:grid-cols-[7rem_6rem_1fr]" key={action.id}><span>{formatDate(language, action.createdAt)}</span><span>{action.event}</span><span className="break-all">{action.message || action.kind} · r{action.revision}</span></li>)}</ol>
                  </details>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}
    </section>
  );
}

export function groupActions(actions: Action[]) {
  const grouped = new Map<string, Action[]>();
  for (const action of actions) {
    const group = grouped.get(action.taskId);
    if (group) group.push(action);
    else grouped.set(action.taskId, [action]);
  }
  return [...grouped.entries()].map(([taskId, values]) => ({ taskId, actions: [...values].sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt)) }));
}

function visibleActionMessage(language: Language, action: Action) {
  const localized = actionMessage(language, action.message);
  if (!action.message || localized !== action.message || /^(install|upgrade|uninstall) /.test(action.message)) return localized;
  return action.event === "failed" ? userError(language, action.message) : actionKind(language, action.kind);
}

export function actionKind(language: Language, kind: string) {
  const labels: Record<string, [string, string]> = {
    "application.apply": ["应用变更", "Application change"],
    "gateway.component.apply": ["准备服务入口", "Prepare service access"],
    "gateway.routes.apply": ["更新访问方式", "Update service access"],
    "tunnel.state.apply": ["更新 Cloudflare 连接", "Update Cloudflare connection"]
  };
  return labels[kind] ? copy(language, ...labels[kind]) : copy(language, "系统操作", "System operation");
}

export function actionMessage(language: Language, message?: string) {
  if (!message) return "";
  const operation = /^(install|upgrade|uninstall) (.+)$/.exec(message);
  if (operation) {
    const labels: Record<string, [string, string]> = { install: ["安装", "Install"], upgrade: ["升级", "Upgrade"], uninstall: ["卸载", "Uninstall"] };
    return `${copy(language, ...labels[operation[1]])} ${operation[2]}`;
  }
  if (message === "task lease expired; queued for retry") return copy(language, "节点响应较慢，系统正在自动重试。", "The node is responding slowly. Vastora is retrying automatically.");
  if (message === "gateway health check failed; queued for reconcile") return copy(language, "服务入口暂时不可用，系统正在自动修复。", "Service access is temporarily unavailable. Vastora is repairing it automatically.");
  return message;
}
