import { useEffect, useState } from "react";
import { api } from "../api";
import type { AppData, Screen } from "../types";
import type { ExecutionPage } from "../execution-types";
import type { Language } from "../translations";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { actionKind } from "./activityPresentation";
import { copy, taskError } from "./shared";

export function HomeTaskSummary({ data, language, onNavigate }: { data: AppData; language: Language; onNavigate: (screen: Screen) => void }) {
  const [page, setPage] = useState<ExecutionPage | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const request = new AbortController();
    void api.executions(0, request.signal, "attention").then((value) => {
      if (!request.signal.aborted) { setPage(value); setFailed(false); }
    }).catch(() => { if (!request.signal.aborted) setFailed(true); });
    return () => request.abort();
  }, [data.actions]);
  if (failed) return <Alert><AlertTitle>{copy(language, "待处理任务暂时无法读取", "Task status is unavailable")}</AlertTitle><AlertDescription><Button onClick={() => onNavigate("activity")} variant="outline" size="sm">{copy(language, "前往活动重试", "Retry in Activity")}</Button></AlertDescription></Alert>;
  if (!page) return <p role="status" className="text-sm text-muted-foreground">{copy(language, "正在检查待处理任务…", "Checking tasks needing attention…")}</p>;
  if (!page.executions.length) return <p role="status" className="text-sm text-muted-foreground">{copy(language, "没有待处理任务", "No tasks need attention")}</p>;
  return <Alert><AlertTitle>{copy(language, `${page.executions.length}${page.nextCursor ? "+" : ""} 项任务需要处理`, `${page.executions.length}${page.nextCursor ? "+" : ""} tasks need attention`)}</AlertTitle><AlertDescription>
    {page.executions.slice(0, 2).map((task) => <div key={task.id}><p className="font-medium text-foreground">{data.agents.find((agent) => agent.id === task.agentId)?.name ?? copy(language, "未知节点", "Unknown node")} · {actionKind(language, task.kind)}</p><p>{taskError(language, task.lastError)}</p></div>)}
    <Button onClick={() => onNavigate("activity")} size="sm" variant="outline">{copy(language, "查看待处理任务", "Review tasks")}</Button>
  </AlertDescription></Alert>;
}
