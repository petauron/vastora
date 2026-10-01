import { useEffect, useRef, useState } from "react";
import { CheckCircle2Icon, RotateCcwIcon, ShieldCheckIcon } from "lucide-react";
import { api } from "../api";
import type { AgentEnrollment, AgentReinstallInput, AgentReinstallPlan, AgentView, NetworkProfile } from "../types";
import type { Language } from "../translations";
import { agentInstallCommand } from "../lib/agent-install";
import { CopyButton, copy, formatDate, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ReinstallNetworkReview } from "./ReinstallNetworkReview";
import { Spinner } from "@/components/ui/spinner";

export function ReinstallNodeSheet({ agent, installerAvailable, language, onClose }: { agent: AgentView; installerAvailable: boolean; language: Language; onClose: () => void }) {
  const [plan, setPlan] = useState<AgentReinstallPlan | null>(null);
  const [enrollment, setEnrollment] = useState<AgentEnrollment | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const request = useRef<AgentReinstallInput | null>(null);
  const generation = useRef(0);
  // A dashboard update is evidence to refresh the saved operation, not evidence
  // that business recovery completed. Closing invalidates every pending response.
  useEffect(() => {
    const current = ++generation.current;
    setBusy(true);
    api.agentReinstallPlan(agent.id).then((value) => {
      if (generation.current === current) { setPlan(value); setError(""); }
    }).catch((cause: unknown) => {
      if (generation.current === current) { setPlan(null); setError(userError(language, cause)); }
    }).finally(() => { if (generation.current === current) setBusy(false); });
    return () => { generation.current += 1; };
  }, [agent.id, agent.reinstall?.updatedAt, language, revision]);
  const recovery = plan?.recovery;
  const joined = recovery?.state === "review_required";
  const localTasks = plan ? plan.executions.filter((execution) => execution.agentId === agent.id && execution.resolution === "local_after_isolation").length + plan.unclaimedLocalWork.length : 0;
  const settledTasks = plan?.localWorkDisposition ? plan.localWorkDisposition.executionIds.length + plan.localWorkDisposition.unclaimedWork.length : 0;
  const command = enrollment && !joined ? agentInstallCommand({ centerURL: enrollment.centerUrl ?? "", enrollment, installerAvailable }) : "";
  const confirm = async (resumeIsolation = false) => {
    if (!plan || busy) return;
    const current = generation.current;
    request.current ??= {
      operationId: recovery?.state === "awaiting_enrollment" ? recovery.id : crypto.randomUUID(),
      planRevision: recovery?.state === "awaiting_enrollment" ? recovery.planRevision : plan.revision,
      confirmReplacement: true,
    };
    setBusy(true); setError("");
    try {
      const result = resumeIsolation && recovery
        ? await api.continueAgentReinstallIsolation(agent.id, { operationId: recovery.id, expectedAttempt: recovery.attempt, confirmIsolation: true })
        : await api.createAgentReconnectEnrollment(agent.id, request.current);
      if (generation.current !== current) return;
      setEnrollment(result);
      const latest = await api.agentReinstallPlan(agent.id);
      if (generation.current === current) setPlan(latest);
    } catch (cause) {
      if (generation.current === current) setError(userError(language, cause));
    } finally {
      if (generation.current === current) setBusy(false);
    }
  };
  const approveNetwork = async (profile: NetworkProfile) => {
    if (!plan || !recovery || busy) return;
    const current = generation.current;
    setBusy(true); setError("");
    try {
      await api.approveAgentReinstallNetwork(agent.id, { operationId: recovery.id, planRevision: plan.revision, confirmMigration: true, profile });
      if (generation.current !== current) return;
      const latest = await api.agentReinstallPlan(agent.id);
      if (generation.current === current) setPlan(latest);
    } catch (cause) {
      if (generation.current === current) setError(userError(language, cause));
    } finally {
      if (generation.current === current) setBusy(false);
    }
  };
  const refresh = () => { request.current = null; setRevision((value) => value + 1); };
  const settleLocalWork = async () => {
    if (!plan || !recovery || busy) return;
    const current = generation.current;
    setBusy(true); setError("");
    try {
      await api.settleAgentReinstallLocalWork(agent.id, { operationId: recovery.id, planRevision: plan.revision, confirmLocal: true });
      if (generation.current !== current) return;
      const latest = await api.agentReinstallPlan(agent.id);
      if (generation.current === current) setPlan(latest);
    } catch (cause) {
      if (generation.current === current) setError(userError(language, cause));
    } finally {
      if (generation.current === current) setBusy(false);
    }
  };
  return <Sheet open onOpenChange={(open) => { if (!open) onClose(); }}>
    <SheetContent className="sm:max-w-xl">
      <SheetHeader>
        <SheetTitle>{copy(language, `重装恢复 ${agent.name}`, `Recover ${agent.name}`)}</SheetTitle>
        <SheetDescription>{copy(language, "保留节点和业务归属，核对恢复要求后接替机器身份。", "Keep the node and business ownership. Review recovery requirements before replacing the machine identity.")}</SheetDescription>
      </SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4">
        {busy ? <p role="status" className="flex items-center gap-2 text-sm"><Spinner />{copy(language, "正在读取恢复状态…", "Reading recovery progress…")}</p> : null}
        {error ? <Alert variant="destructive"><AlertTitle>{copy(language, "操作未完成", "Action incomplete")}</AlertTitle><AlertDescription>{error}</AlertDescription></Alert> : null}
        {plan ? <>
          {recovery ? <Alert><ShieldCheckIcon /><AlertTitle>{joined ? copy(language, "新身份已接入，业务待恢复", "New identity connected; business recovery pending") : copy(language, "恢复已暂停任务执行", "Recovery has paused task execution")}</AlertTitle><AlertDescription>
            {recovery.state === "failed" || recovery.state === "preparing" ? copy(language, "命令准备尚未完成。核对现场后再继续，系统不会自动重复操作。", "Command preparation is incomplete. Inspect the operation before continuing; it will not run again automatically.") : copy(language, "需要继续核对网络地址和应用恢复；节点在线不代表业务已恢复。", "Network addresses and application recovery still need verification. Being online does not mean business is restored.")}
          </AlertDescription></Alert> : <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "确认后替换管理身份", "Confirm management identity replacement")}</AlertTitle><AlertDescription>{copy(language, "旧 Agent 凭据、接入命令和受管私网身份将撤销，任务领取暂停。应用、订阅和历史执行记录保留。", "The previous Agent credential, enrollment command and managed private identity will be revoked, and task claims paused. Apps, subscriptions and execution history are retained.")}</AlertDescription></Alert>}
          {recovery?.lastError ? <Alert variant="destructive"><AlertTitle>{copy(language, "恢复已暂停", "Recovery paused")}</AlertTitle><AlertDescription>{userError(language, recovery.lastError)}</AlertDescription></Alert> : null}
          <section aria-label={copy(language, "恢复清单", "Recovery checklist")} className="flex flex-col gap-3 rounded-xl border p-4">
            <div className="flex items-start justify-between gap-3 text-sm"><span>{copy(language, "私网与入口", "Network and access")}</span><span className="text-right text-muted-foreground">{plan.privateNetwork.privateAddress || plan.privateNetwork.serviceAddress || copy(language, "待确认", "Needs review")}</span></div>
            {recovery?.privateIsolation === "withdrawn" ? <p className="text-xs text-muted-foreground">{copy(language, "已确认旧私网身份撤销。", "Previous private identity withdrawal verified.")}</p> : null}
            <p className="text-xs text-muted-foreground">{plan.privateNetwork.addressRecovery === "explicit_migration_required" ? copy(language, "当前私网控制面不支持指定原 IP，需明确确认地址迁移。", "The current private controller cannot pin the old IP; address migration requires explicit review.") : copy(language, "原地址作为恢复依据保留，需核对新机器的实际网络。", "The previous address is retained as recovery evidence; verify the new machine's actual network.")} {copy(language, `${plan.privateNetwork.landingRoutes} 条落地授权 · ${plan.privateNetwork.publications} 个入口`, `${plan.privateNetwork.landingRoutes} landing grants · ${plan.privateNetwork.publications} access entries`)}</p>
            <div className="flex items-center justify-between gap-3 text-sm"><span>{copy(language, "应用", "Applications")}</span><Badge variant="secondary">{plan.applications.length}</Badge></div>
            {plan.applications.length ? <ul className="flex flex-col gap-2">{plan.applications.map((app) => <li className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 text-sm" key={app.applicationId}><span className="min-w-0 break-words">{app.name} <span className="text-xs text-muted-foreground">{app.version}</span></span><span className="text-xs text-muted-foreground">{recoveryLabel(app.recovery, language)}</span></li>)}</ul> : <p className="text-xs text-muted-foreground">{copy(language, "无应用需要恢复", "No applications to restore")}</p>}
            <p className="text-xs text-muted-foreground">{copy(language, `${plan.executions.length} 条执行待核对 · ${plan.pendingWork.reduce((total, item) => total + item.count, 0)} 项历史工作保留`, `${plan.executions.length} executions to inspect · ${plan.pendingWork.reduce((total, item) => total + item.count, 0)} historical work items retained`)}</p>
            {plan.requirements.includes("inspect_remote_effects_before_restore") ? <p className="text-xs text-muted-foreground">{copy(language, "包含其他主机上关联此节点的任务，需单独核对执行结果。", "Includes related tasks on other hosts; inspect their outcomes separately.")}</p> : null}
          </section>
          {joined && (localTasks > 0 || plan.localWorkDisposition) ? <section className="flex flex-col gap-2 rounded-xl border p-4" aria-label={copy(language, "旧本机任务", "Previous machine tasks")}>
            {plan.localWorkDisposition ? <p className="text-sm">{copy(language, `本次已终止 ${settledTasks} 条旧本机任务，历史记录已保留。`, `${settledTasks} previous machine tasks abandoned; history retained.`)}</p> : null}
            {localTasks > 0 ? <><p className="text-xs text-muted-foreground">{copy(language, "终止已隔离的旧本机执行及从未下发的排队任务，保留配置和记录。应用恢复将创建新任务。", "Abandon isolated local executions and cancel unissued queued tasks, retaining configuration and records. Application restoration will create new tasks.")}</p><Button className="self-start" variant="outline" disabled={busy || recovery.privateIsolation === "pending"} onClick={() => void settleLocalWork()}>{copy(language, `终止 ${localTasks} 条旧本机任务`, `Abandon ${localTasks} previous machine tasks`)}</Button></> : null}
          </section> : null}
          {joined && plan.networkReview ? <ReinstallNetworkReview key={plan.revision} review={plan.networkReview} busy={busy} language={language} onApprove={approveNetwork} /> : null}
          {command ? <><p className="text-sm">{copy(language, "在重装后的原服务器执行一次", "Run once on the reinstalled original server")}</p><div className="relative"><code className="block max-h-48 overflow-auto break-all rounded-xl bg-muted p-4 pr-14 text-xs leading-6">{command}</code><CopyButton className="absolute right-2 top-2" label={copy(language, "复制命令", "Copy command")} language={language} size="icon" value={command} /></div><p className="text-xs text-muted-foreground">{copy(language, `命令有效期至 ${formatDate(language, enrollment!.expiresAt)}`, `Command valid until ${formatDate(language, enrollment!.expiresAt)}`)}</p></> : null}
          {joined ? <p className="flex items-center gap-2 text-sm"><CheckCircle2Icon aria-hidden="true" className="size-4" />{copy(language, "身份接替已记录；网络、应用和业务验证尚未完成。", "Identity replacement recorded. Network, application and business verification are not complete.")}</p> : null}
        </> : null}
      </div>
      <SheetFooter className="flex-row flex-wrap justify-end gap-2">
        <Button disabled={busy} onClick={refresh} variant="outline"><RotateCcwIcon data-icon="inline-start" />{copy(language, "刷新状态", "Refresh status")}</Button>
        <Button onClick={onClose} variant="outline">{copy(language, "关闭", "Close")}</Button>
        {plan && !command && (!recovery || recovery.state === "awaiting_enrollment") ? <Button disabled={busy || (agent.connected && !recovery)} onClick={() => void confirm()}>{recovery ? copy(language, "取回接入命令", "Retrieve command") : copy(language, "确认接替并生成命令", "Confirm and create command")}</Button> : null}
        {recovery?.privateIsolation === "pending" && (recovery.state === "failed" || recovery.state === "preparing") ? <Button disabled={busy} onClick={() => void confirm(true)}>{copy(language, "核对并继续隔离", "Inspect and continue isolation")}</Button> : null}
      </SheetFooter>
    </SheetContent>
  </Sheet>;
}

function recoveryLabel(value: string, language: Language) {
  switch (value) {
    case "rebuild_configuration": return copy(language, "重建配置", "Rebuild configuration");
    case "reenroll_monitor": return copy(language, "重新关联监控", "Re-enroll monitoring");
    case "restore_data": return copy(language, "需要数据备份", "Data backup required");
    case "keep_stopped": return copy(language, "保持卸载", "Keep uninstalled");
    default: return copy(language, "需要核对", "Needs review");
  }
}
