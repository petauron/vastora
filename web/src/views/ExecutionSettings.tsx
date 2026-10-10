import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../api";
import { executionActions, type ExecutionAction, type ExecutionClaimControl, type ExecutionPage, type ExecutionView, type LegacyReceiptView } from "../execution-types";
import type { Screen } from "../types";
import { SelectControl } from "@/components/SelectControl";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Input } from "@/components/ui/input";
import type { Language } from "../translations";
import { copy, formatDate, taskError, userError } from "./shared";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardHeader, CardTitle, CardDescription, CardContent, CardFooter } from "@/components/ui/card";
import { Field, FieldGroup, FieldLabel, FieldError } from "@/components/ui/field";
import { Checkbox } from "@/components/ui/checkbox";
import { Textarea } from "@/components/ui/textarea";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription, SheetFooter } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Empty, EmptyHeader, EmptyTitle } from "@/components/ui/empty";

function actionLabel(language: Language, action: ExecutionAction) {
  if (action === "confirm-completed") return copy(language, "确认已完成", "Confirm completed");
  return action === "abandon" ? copy(language, "放弃任务", "Abandon task") : copy(language, "重新执行", "Execute again");
}

function kindLabel(language: Language, kind: string) {
  const labels: Record<string, [string, string]> = {
    "application.apply": ["应用配置", "App configuration"], "application.command": ["应用操作", "App operation"],
    "agent.update": ["节点更新", "Node update"], "agent.decommission": ["节点卸载", "Node removal"],
    "landing.proxy.apply": ["落地连接", "Landing connection"], "landing.server.apply": ["落地服务", "Landing service"],
    "gateway.routes.apply": ["网关路由", "Gateway routes"], "gateway.component.apply": ["网关配置", "Gateway configuration"],
    "node.listener.apply": ["节点入口", "Node entry"], "tunnel.state.apply": ["隧道配置", "Tunnel configuration"],
    "xray.configuration.inspect": ["Xray 配置检查", "Xray configuration inspection"],
    "xray.configuration.apply": ["Xray 配置恢复", "Xray configuration recovery"],
    "legacy.receipt": ["旧执行记录", "Legacy execution"], "node.ip-quality": ["IP 质量检测", "IP quality check"],
  };
  return labels[kind] ? copy(language, ...labels[kind]) : copy(language, "节点任务", "Node task");
}

function stateLabel(language: Language, value: ExecutionView) {
  if (value.disposition === "reexecute") return copy(language, "已重新排队", "Requeued");
  if (value.disposition === "abandon") return copy(language, "已放弃", "Abandoned");
  if (value.disposition === "confirm-completed") return copy(language, "已确认完成", "Confirmed completed");
  const labels: Record<string, [string, string]> = {
    offered: ["等待执行", "Waiting"], running: ["执行中", "Running"], helper_running: ["执行中", "Running"],
    succeeded: ["已完成", "Completed"], failed: ["执行失败", "Failed"], unknown: ["结果待核对", "Outcome unconfirmed"],
  };
  const label = labels[value.state];
  return label ? copy(language, ...label) : copy(language, "待核对", "Needs review");
}

export function ExecutionSettings({ language, agents, onNavigate }: { language: Language; agents: { id: string; name: string }[]; onNavigate?: (screen: Screen) => void }) {
  const [filter, setFilter] = useState<"all" | "attention" | "running">("attention");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState<ExecutionPage | null>(null);
  const [control, setControl] = useState<ExecutionClaimControl | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [selected, setSelected] = useState<ExecutionView | null>(null);
  const [confirmControl, setConfirmControl] = useState(false);
  const pending = useRef<AbortController | null>(null);
  const load = useCallback(async (before = 0) => {
    pending.current?.abort();
    const request = new AbortController();
    pending.current = request;
    setBusy(true); setPage(null); setError("");
    try {
      const [nextPage, nextControl] = await Promise.all([api.executions(before, request.signal, filter), api.executionClaimControl(request.signal)]);
      if (request.signal.aborted) return;
      setPage(nextPage); setControl(nextControl);
    } catch (cause) {
      if (!request.signal.aborted) setError(userError(language, cause));
    } finally {
      if (!request.signal.aborted) setBusy(false);
    }
  }, [language, filter]);
  useEffect(() => { void load(); return () => pending.current?.abort(); }, [load]);
  const names = new Map(agents.map((agent) => [agent.id, agent.name]));
  const visible = page?.executions.filter((execution) => !query.trim() || (names.get(execution.agentId) ?? "").toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()));
  return <Card>
    <CardHeader><CardTitle>{copy(language, "任务处理", "Tasks")}</CardTitle><CardDescription>{copy(language, "先查看需要处理的任务；失败任务会保留，核对前不会自动重试。", "Review tasks needing attention first. Failed tasks are retained and never retried automatically.")}</CardDescription></CardHeader>
    <CardContent className="flex flex-col gap-4">
      {control?.paused ? <Alert><AlertTitle>{copy(language, "紧急维护：已暂停所有新任务", "Emergency maintenance: all new task claims paused")}</AlertTitle><AlertDescription><Button disabled={busy} variant="outline" onClick={() => setConfirmControl(true)}>{copy(language, "结束紧急维护", "End emergency maintenance")}</Button></AlertDescription></Alert> : null}
      <div className="flex flex-wrap items-center gap-2">
        <SelectControl aria-label={copy(language, "任务状态", "Task status")} value={filter} onValueChange={(value) => setFilter(value as typeof filter)} options={[{value:"attention", label:copy(language,"需要处理","Needs attention")},{value:"running",label:copy(language,"进行中","In progress")},{value:"all",label:copy(language,"全部记录","All records")}]} />
        <Input className="w-full sm:w-60" aria-label={copy(language,"搜索本页节点","Search nodes on this page")} placeholder={copy(language,"搜索本页节点…","Search nodes on this page…")} value={query} onChange={(event)=>setQuery(event.target.value)} />
        {page ? <span className="text-xs text-muted-foreground">{copy(language, `本页 ${visible?.length ?? 0} 项`, `${visible?.length ?? 0} on this page`)}</span> : null}
      </div>
      {error ? <FieldError role="alert">{error}</FieldError> : null}
      {notice ? <p role="status" className="text-sm text-muted-foreground">{notice}</p> : null}
      {busy ? <p role="status" className="flex items-center gap-2"><Spinner />{copy(language, "正在读取", "Loading")}</p> : null}
      {page && !visible?.length ? <Empty><EmptyHeader><EmptyTitle>{query ? copy(language,"本页没有匹配的节点","No matching nodes on this page") : filter === "attention" ? copy(language,"没有待处理任务","No tasks need attention") : copy(language,"暂无任务记录","No tasks found")}</EmptyTitle></EmptyHeader></Empty> : null}
      <ul className="flex flex-col gap-2" aria-label={copy(language, "执行记录", "Execution history")}>
        {visible?.map((execution) => <li key={execution.id}>
          <Button className="h-auto min-h-11 w-full flex-col items-start justify-between gap-3 py-3 sm:flex-row sm:items-center" variant="outline" disabled={busy} onClick={() => setSelected(execution)}>
            <span className="flex min-w-0 flex-col items-start gap-1 text-left"><span className="max-w-full truncate">{names.get(execution.agentId) ?? copy(language, "未知节点", "Unknown node")}</span><span className="max-w-full whitespace-normal break-words text-xs text-muted-foreground">{kindLabel(language, execution.kind)} · {formatDate(language, execution.updatedAt)}</span></span>
            <Badge variant={!execution.disposition && ["failed", "unknown"].includes(execution.state) ? "destructive" : "secondary"}>{stateLabel(language, execution)}</Badge>
          </Button>
        </li>)}
      </ul>
      {control && !control.paused ? <details className="border-t pt-3 text-xs text-muted-foreground"><summary className="cursor-pointer">{copy(language,"高级：紧急维护","Advanced: emergency maintenance")}</summary><p className="my-3">{copy(language,"暂停所有节点接收新任务，仅用于系统维护。正在运行的应用不会因此停止。","Pause new tasks on every node for maintenance. This does not stop running applications.")}</p><Button disabled={busy} variant="outline" onClick={() => setConfirmControl(true)}>{copy(language,"进入紧急维护","Start emergency maintenance")}</Button></details> : null}
    </CardContent>
    <CardFooter className="flex-wrap justify-end gap-2">
      <Button className="min-h-11" disabled={busy} variant="outline" onClick={() => void load()}>{copy(language, "刷新最新记录", "Refresh latest")}</Button>
      {page?.nextCursor ? <Button className="min-h-11" disabled={busy} variant="outline" onClick={() => void load(page.nextCursor)}>{copy(language, "更早记录", "Older records")}</Button> : null}
    </CardFooter>
    {selected ? <ExecutionDetail key={selected.id} execution={selected} language={language} onNavigate={onNavigate} name={names.get(selected.agentId) ?? selected.agentId} onClose={() => setSelected(null)} onSaved={() => { setSelected(null); setNotice(copy(language, "处置已记录。", "Disposition recorded.")); void load(); }} /> : null}
    {confirmControl && control ? <ClaimControlConfirmation paused={control.paused} language={language} onClose={() => setConfirmControl(false)} onSaved={() => { setConfirmControl(false); setNotice(copy(language, "领取设置已保存。", "Claim settings saved.")); void load(); }} /> : null}
  </Card>;
}

function ClaimControlConfirmation({ paused, language, onClose, onSaved }: { paused: boolean; language: Language; onClose: () => void; onSaved: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async () => {
    setBusy(true); setError("");
    try { await api.setExecutionClaimControl(!paused); onSaved(); }
    catch (cause) { setError(userError(language, cause)); }
    finally { setBusy(false); }
  };
  return <Sheet open onOpenChange={(open) => { if (!open && !busy) onClose(); }}><SheetContent showCloseButton={!busy}>
    <SheetHeader><SheetTitle>{paused ? copy(language, "结束紧急维护", "End emergency maintenance") : copy(language, "进入紧急维护", "Start emergency maintenance")}</SheetTitle><SheetDescription>{paused ? copy(language, "恢复所有节点领取新任务。失败或结果不明的任务仍保持原状态，不会自动重试。", "Resume new task claims for all nodes. Failed or uncertain tasks keep their current state and are not retried automatically.") : copy(language, "这是紧急总闸：将暂停包括 Agent 更新在内的所有新任务。正在执行的任务不会被取消，节点仍会上报状态。", "This is an emergency stop: all new tasks, including Agent updates, are paused. Running tasks are not cancelled and nodes continue reporting.")}</SheetDescription></SheetHeader>
    {error ? <FieldError className="px-4" role="alert">{error}</FieldError> : null}
    <SheetFooter><Button className="min-h-11" disabled={busy} variant="outline" onClick={onClose}>{copy(language, "取消", "Cancel")}</Button><Button className="min-h-11" disabled={busy} onClick={() => void submit()}>{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "确认", "Confirm")}</Button></SheetFooter>
  </SheetContent></Sheet>;
}

function ExecutionDetail({ execution, name, language, onClose, onSaved, onNavigate }: { execution: ExecutionView; name: string; language: Language; onClose: () => void; onSaved: () => void; onNavigate?: (screen: Screen) => void }) {
  const [action, setAction] = useState<ExecutionAction | null>(null);
  const [stopped, setStopped] = useState(false);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [legacy, setLegacy] = useState<LegacyReceiptView | null>(null);
  useEffect(() => {
    if (execution.kind !== "legacy.receipt") return;
    const request = new AbortController();
    void api.inspectLegacyReceipt(execution.id, request.signal).then((value) => { if (!request.signal.aborted) setLegacy(value); }).catch((cause) => { if (!request.signal.aborted) setError(userError(language, cause)); });
    return () => request.abort();
  }, [execution.id, execution.kind, language]);
  const submit = async () => {
    if (!action || !stopped || !note.trim() || busy) return;
    setBusy(true); setError("");
    try { await api.disposeExecution(execution.id, execution.kind, { action, executionStopped: true, note: note.trim() }); onSaved(); }
    catch (cause) { setError(userError(language, cause)); }
    finally { setBusy(false); }
  };
  return <Sheet open onOpenChange={(open) => { if (!open && !busy) onClose(); }}><SheetContent showCloseButton={!busy} className="sm:max-w-lg">
    <SheetHeader><SheetTitle>{name}</SheetTitle><SheetDescription>{stateLabel(language, execution)} · {kindLabel(language, execution.kind)}</SheetDescription></SheetHeader>
    <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4">
      {!execution.disposition && ["failed", "unknown"].includes(execution.state) ? <Alert><AlertTitle>{copy(language,"先核对实际状态","Check the actual state first")}</AlertTitle><AlertDescription><p>{taskError(language, execution.lastError)}</p><p>{copy(language,"如果不确定是否已完成，请保留任务，交由维护人员核对。放弃任务不会撤销服务器上已经发生的修改。","If the outcome is unclear, leave the task for an administrator to review. Abandoning it does not undo changes on the server.")}</p>{onNavigate ? <Button variant="outline" size="sm" onClick={() => onNavigate(execution.kind.startsWith("agent.") ? "nodes" : "apps")}>{execution.kind.startsWith("agent.") ? copy(language,"查看节点","View nodes") : copy(language,"查看应用","View apps")}</Button> : null}</AlertDescription></Alert> : null}
      <details className="rounded-lg border p-3 text-sm"><summary className="cursor-pointer">{copy(language,"技术详情","Technical details")}</summary><dl className="mt-3 grid gap-3"><div><dt>{copy(language,"任务编号","Task ID")}</dt><dd className="break-all text-muted-foreground">{legacy?.taskId ?? execution.taskId}</dd></div><div><dt>{copy(language,"执行阶段","Phase")}</dt><dd>{execution.phase}</dd></div><div><dt>{copy(language,"执行次数","Attempt")}</dt><dd>{execution.attempt}</dd></div></dl>{execution.lastError ? <code className="mt-3 block break-all text-xs">{execution.lastError}</code> : null}</details>
      {execution.kind === "legacy.receipt" ? <p className="text-sm text-muted-foreground">{legacy ? legacy.hasCompletion ? copy(language, "旧执行结果已安全保存，需要核对后处置。这里不会显示其中的凭据。", "Old result securely retained for review. Credentials are not displayed here.") : copy(language, "旧执行没有完成结果，需要核对实际资源。", "No completion result was retained; inspect actual resources.") : copy(language, "正在读取旧执行记录", "Loading legacy evidence")}</p> : null}
      {!action ? <div className="flex flex-wrap gap-2">{executionActions(execution).map((option) => <Button className="min-h-11" key={option} variant="outline" onClick={() => setAction(option)}>{actionLabel(language, option)}</Button>)}</div> : <FieldGroup>
        <p className="text-sm">{action === "reexecute" ? copy(language, "将创建一次新的执行。请确认旧操作已停止，并核对现有资源，避免重复变更。", "A new attempt will be created. Verify the old operation has stopped and inspect resources to avoid duplicate changes.") : action === "abandon" ? copy(language, "只放弃此任务，不撤销已经发生的变更，也不删除历史证据。", "Abandons this task without undoing changes or deleting evidence.") : copy(language, "请核对实际完成结果。节点在线或重连本身不代表任务已完成。", "Verify the actual result. Being online or reconnecting alone does not prove completion.")}</p>
        <Field orientation="horizontal" data-disabled={busy}><Checkbox id="execution-stopped" checked={stopped} disabled={busy} onCheckedChange={setStopped} /><FieldLabel htmlFor="execution-stopped">{copy(language, "已确认旧执行停止，并核对实际资源", "I verified the old execution stopped and inspected resources")}</FieldLabel></Field>
        <Field data-disabled={busy}><FieldLabel htmlFor="execution-note">{copy(language, "核对说明", "Verification notes")}</FieldLabel><Textarea id="execution-note" disabled={busy} maxLength={1024} value={note} onChange={(event) => setNote(event.target.value)} required /></Field>
      </FieldGroup>}
      {error ? <FieldError role="alert">{error}</FieldError> : null}
    </div>
    <SheetFooter><Button className="min-h-11" disabled={busy} variant="outline" onClick={() => { if (action) { setAction(null); setStopped(false); setNote(""); setError(""); } else onClose(); }}>{copy(language, "返回", "Back")}</Button>{action ? <Button className="min-h-11" disabled={busy || !stopped || !note.trim()} onClick={() => void submit()}>{busy ? <Spinner data-icon="inline-start" /> : null}{actionLabel(language, action)}</Button> : null}</SheetFooter>
  </SheetContent></Sheet>;
}
