import { HistoryIcon } from "lucide-react";
import type { Action, AgentView, Screen } from "../types";
import type { Language } from "../translations";
import { ExecutionSettings } from "./ExecutionSettings";
import { PageHeading, StateBadge, copy, formatDate } from "./shared";
import { Badge } from "@/components/ui/badge";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";

import { actionKind, groupActions, visibleActionMessage } from "./activityPresentation";

const visibleEventLimit = 100;

export function ActivityView({ actions, agents, language, onNavigate }: { actions: Action[]; agents: AgentView[]; language: Language; onNavigate?: (screen: Screen) => void }) {
  const agentNames = new Map(agents.map((agent) => [agent.id, agent.name]));
  const visibleActions = actions.slice(0, visibleEventLimit);
  const groups = groupActions(visibleActions);
  return (
    <section className="mac-activity flex flex-col gap-6">
      <PageHeading title={copy(language, "活动", "Activity")} description={copy(language, "查看需要处理的任务，再按需查看历史操作。", "Review tasks needing attention, then browse historical activity.")} />
      <ExecutionSettings agents={agents} language={language} onNavigate={onNavigate} />
      <details className="rounded-xl border p-4"><summary className="cursor-pointer text-sm font-medium">{copy(language,"操作日志","Operation log")}</summary><div className="mt-4">
      {actions.length > visibleEventLimit ? <p className="text-xs text-muted-foreground">{copy(language, `显示最近 ${visibleEventLimit} 条事件，已按操作合并。`, `Showing the latest ${visibleEventLimit} events, grouped by operation.`)}</p> : null}
      {groups.length === 0 ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><HistoryIcon /></EmptyMedia><EmptyTitle>{copy(language, "还没有活动记录", "No activity yet")}</EmptyTitle><EmptyDescription>{copy(language, "创建安装或访问任务后，进度会显示在这里。", "Progress appears here after an install or access operation is created.")}</EmptyDescription></EmptyHeader></Empty> : (
        <div aria-live="polite" className="mac-activity-list">
          {groups.map((group) => {
            const latest = group.actions[0];
            const message = visibleActionMessage(language, latest);
            return (
              <Card key={group.taskId} size="sm">
                <CardHeader><CardTitle>{message || actionKind(language, latest.kind)}</CardTitle><CardDescription>{agentNames.get(latest.agentId) ?? copy(language, "未知节点", "Unknown node")} · {formatDate(language, latest.createdAt)}</CardDescription><CardAction><StateBadge language={language} value={latest.currentState || latest.event} /></CardAction></CardHeader>
                <CardContent>
                  {latest.currentState === "superseded" ? <p className="text-sm text-muted-foreground">{copy(language, "此配置已被替代，不再排队；下面保留原始事件。", "This configuration was superseded and is no longer queued; original events are retained below.")}</p> : null}
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
      </div></details>
    </section>
  );
}
