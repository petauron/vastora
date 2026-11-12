import { useEffect, useState } from "react";
import { CircleArrowUpIcon, RefreshCwIcon, ServerIcon, ShieldCheckIcon } from "lucide-react";
import { api } from "../api";
import type { AgentUpdatesStatus, AgentUpdateTarget } from "../types";
import type { Language } from "../translations";
import { copy, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { FieldError, FieldLegend, FieldSet } from "@/components/ui/field";
import { Progress, ProgressLabel, ProgressValue } from "@/components/ui/progress";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const labels: Record<AgentUpdateTarget["state"], readonly [string, string]> = {
  available: ["可更新", "Available"], current: ["已是当前版本", "Current"], newer: ["版本更新", "Newer version"],
  paused: ["已暂停更新", "Updates paused"], disabled: ["已停用", "Disabled"], offline: ["离线", "Offline"],
  pending: ["等待领取", "Queued"], updating: ["更新中", "Updating"], failed: ["需核查失败记录", "Review failure"],
  manual: ["需手动接入更新", "Manual update required"], blocked: ["待核验或暂不可更新", "Review required or unavailable"],
};

export function AgentUpdateCard({ language, onViewActivity }: { language: Language; onViewActivity?: () => void }) {
  const [status, setStatus] = useState<AgentUpdatesStatus | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [confirmation, setConfirmation] = useState<{ mode: "all" | "selected"; targetVersion: string; targets: AgentUpdateTarget[] } | null>(null);
  const [refresh, setRefresh] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");

  useEffect(() => {
    let stopped = false;
    let timer = 0;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const next = await api.agentUpdates(controller.signal);
        if (stopped) return;
        setStatus(next);
        // Available but unselected nodes are never work in progress. Poll only
        // tasks already pushed by an administrator, not a prospective rollout.
        if (!next.pushPaused && next.targets.some((target) => target.state === "pending" || target.state === "updating")) timer = window.setTimeout(() => void poll(), 2000);
      } catch (failure) {
        if (!stopped) setError(userError(language, failure));
      }
    };
    void poll();
    return () => { stopped = true; controller.abort(); window.clearTimeout(timer); };
  }, [language, refresh]);

  const eligible = status?.targets.filter((target) => target.eligible) ?? [];
  const chosen = eligible.filter((target) => selected.includes(target.id));
  const choose = (mode: "all" | "selected") => {
    if (!status) return;
    setError("");
    setConfirmation({ mode, targetVersion: status.targetVersion, targets: mode === "all" ? eligible : chosen });
  };
  const submit = async () => {
    if (!confirmation) return;
    setBusy(true); setError("");
    try {
      const result = await api.pushAgentUpdates(confirmation.mode === "all"
        ? { mode: "all", targetVersion: confirmation.targetVersion }
        : { mode: "selected", targetVersion: confirmation.targetVersion, agentIds: confirmation.targets.map((target) => target.id) });
      setStatus(result.status); setSelected([]); setConfirmation(null);
      setMessage(copy(language, `已推送 ${result.queuedAgentIds.length} 台，跳过 ${result.skippedAgentIds.length} 台。`, `Pushed ${result.queuedAgentIds.length}; skipped ${result.skippedAgentIds.length}.`));
      if (result.error) setError(userError(language, result.error));
      setRefresh((value) => value + 1);
    } catch (failure) { setError(userError(language, failure)); }
    finally { setBusy(false); }
  };
  const rollout = status?.rollout;
  const current = rollout ? copy(language, `${rollout.updated}/${rollout.total} 个 Agent 已是当前版本`, `${rollout.updated}/${rollout.total} Agents are current`) : "";

  return <>
    <Card>
      <CardHeader><CardTitle className="flex items-center gap-2"><ServerIcon />{copy(language, "Agent 更新", "Agent updates")}</CardTitle><CardDescription>{copy(language, "单独选择全部或指定在线节点推送。主控升级、页面刷新和节点上线不会自动创建更新任务。", "Push separately to all eligible online nodes or selected nodes. Center upgrades, page refreshes and reconnects do not create update tasks.")}</CardDescription></CardHeader>
      <CardContent className="flex flex-col gap-4">
        {status ? <p className="text-sm">{copy(language, "目标版本", "Target version")}：{status.targetVersion} · {copy(language, `${eligible.length} 台可更新`, `${eligible.length} eligible nodes`)}</p> : <Spinner />}
        {rollout && rollout.total > 0 ? <Progress aria-valuetext={current} value={Math.round(100 * rollout.updated / rollout.total)}><ProgressLabel>{copy(language, "Agent 版本状态", "Agent version status")}</ProgressLabel><ProgressValue>{() => current}</ProgressValue></Progress> : null}
        {status && (!status.available || status.pushPaused) ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, status.pushPaused ? "紧急维护已暂停 Agent 更新" : "Agent 推送暂不可用", status.pushPaused ? "Emergency maintenance paused Agent updates" : "Agent pushes are unavailable")}</AlertTitle><AlertDescription>{copy(language, "请先核对主控就绪状态和任务领取设置；已有任务、身份及业务保留。", "Review Center readiness and task claim controls. Existing tasks, identities and services are preserved.")}</AlertDescription></Alert> : null}
        {rollout && (rollout.failed > 0 || rollout.blocked > 0 || rollout.manual > 0) ? <Alert><AlertTitle>{copy(language, "部分 Agent 需要后续处理", "Some Agents need follow-up")}</AlertTitle><AlertDescription>{copy(language, `${rollout.failed ? `${rollout.failed} 个更新失败；` : ""}${rollout.blocked ? `${rollout.blocked} 个存在待处理任务，已暂停更新；` : ""}${rollout.manual ? `${rollout.manual} 个旧版本需要手动更新。` : ""}`, `${rollout.failed} failed; ${rollout.blocked} blocked; ${rollout.manual} require a manual update.`)}</AlertDescription></Alert> : null}
        {status ? <FieldSet><FieldLegend>{copy(language, "选择更新节点", "Select update targets")}</FieldLegend><div className="max-h-80 overflow-auto"><Table><TableHeader><TableRow><TableHead>{copy(language, "选择", "Select")}</TableHead><TableHead>{copy(language, "节点", "Node")}</TableHead><TableHead>{copy(language, "当前版本", "Current version")}</TableHead><TableHead>{copy(language, "状态", "Status")}</TableHead></TableRow></TableHeader><TableBody>{status.targets.map((target) => <TableRow key={target.id}><TableCell><Checkbox aria-label={copy(language, `选择 ${target.name}`, `Select ${target.name}`)} checked={selected.includes(target.id) && target.eligible} disabled={busy || !target.eligible} onCheckedChange={(checked) => setSelected((previous) => checked ? [...previous.filter((id) => id !== target.id), target.id] : previous.filter((id) => id !== target.id))} /></TableCell><TableCell>{target.name}</TableCell><TableCell>{target.version}</TableCell><TableCell><Badge variant="secondary">{copy(language, ...labels[target.state])}</Badge></TableCell></TableRow>)}</TableBody></Table></div></FieldSet> : null}
        <p className="text-xs text-muted-foreground">{copy(language, "明确暂停、离线、凭据撤销及待核验节点不会被强制更新；失败任务仍需核查。应用和订阅不随 Agent 推送更新。", "Paused, offline, revoked and review-blocked nodes are not forced to update. Failed executions still require review. Agent pushes do not update applications or subscriptions.")}</p>
        {onViewActivity ? <Button onClick={onViewActivity} size="sm" variant="outline">{copy(language, "查看活动", "View activity")}</Button> : null}
        {message ? <p role="status" className="text-sm">{message}</p> : null}
        {error && !confirmation ? <FieldError role="alert">{error}</FieldError> : null}
      </CardContent>
      <CardFooter className="flex-wrap justify-end gap-2"><Button disabled={busy} onClick={() => { setError(""); setRefresh((value) => value + 1); }} size="sm" variant="outline"><RefreshCwIcon data-icon="inline-start" />{copy(language, "刷新节点状态", "Refresh nodes")}</Button><Button disabled={busy || chosen.length === 0} onClick={() => choose("selected")} size="sm" variant="outline">{copy(language, `更新所选 (${chosen.length})`, `Update selected (${chosen.length})`)}</Button><Button disabled={busy || eligible.length === 0} onClick={() => choose("all")} size="sm"><CircleArrowUpIcon data-icon="inline-start" />{copy(language, "全部更新", "Update all")}</Button></CardFooter>
    </Card>
    <Sheet open={confirmation !== null} onOpenChange={(open) => { if (!open && !busy) setConfirmation(null); }}><SheetContent><SheetHeader><SheetTitle>{copy(language, "确认推送 Agent 更新", "Confirm Agent update push")}</SheetTitle><SheetDescription>{copy(language, `推送 ${confirmation?.targetVersion ?? ""}。只更新 Agent 程序，保留节点身份、应用、订阅和流量；节点管理通道可能短暂重连。`, `Push ${confirmation?.targetVersion ?? ""}. Updates the Agent program while retaining identity, applications, subscriptions and traffic. Management channels may briefly reconnect.`)}</SheetDescription></SheetHeader><div className="flex-1 overflow-auto px-4"><p className="text-sm">{confirmation?.mode === "all" ? copy(language, "更新当前符合条件的全部在线节点，明确暂停规则仍然生效。", "Update all currently eligible online nodes; explicit pauses remain enforced.") : copy(language, "仅推送以下所选节点，其他节点不会随后自动加入。", "Push only to the following selected nodes; other nodes will not be added later.")}</p><ul className="mt-3 flex flex-col gap-2 text-sm">{confirmation?.targets.map((target) => <li key={target.id}>{target.name} · {target.version}</li>)}</ul>{error ? <FieldError role="alert">{error}</FieldError> : null}</div><SheetFooter><Button disabled={busy} onClick={() => setConfirmation(null)} variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !confirmation?.targets.length} onClick={() => void submit()}>{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "确认推送", "Confirm push")}</Button></SheetFooter></SheetContent></Sheet>
  </>;
}
