import { useState } from "react";
import { RefreshCwIcon, ShieldCheckIcon } from "lucide-react";
import { api } from "../api";
import type { AgentView } from "../types";
import type { Language } from "../translations";
import { freshTailscaleUpdate } from "../tailscale-update";
import { copy, formatDate, userError } from "./shared";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { FieldError } from "@/components/ui/field";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";

export function TailscaleUpdateCard({ agents, language, onRefresh }: { agents: AgentView[]; language: Language; onRefresh: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [selection, setSelection] = useState<{ agent: AgentView; action: "update" | "review" } | null>(null);
  const [note, setNote] = useState("");
  const hasActive = agents.some((agent) => ["pending", "running", "awaiting_decision"].includes(agent.tailscaleUpdateTask?.state ?? ""));
  const check = async () => {
    setBusy(true); setError(""); setMessage("");
    try { await api.checkTailscaleUpdates(); setMessage(copy(language, "已请求在线节点检查，结果会在节点下次心跳后显示。", "Online nodes will report the check after their next heartbeat.")); }
    catch (cause) { setError(userError(language, cause)); } finally { setBusy(false); }
  };
  const submit = async () => {
    if (!selection) return;
    setBusy(true); setError("");
    try {
      if (selection.action === "update") await api.startTailscaleUpdate(selection.agent.id);
      else if (selection.agent.tailscaleUpdateTask) await api.acknowledgeTailscaleUpdate(selection.agent.id, selection.agent.tailscaleUpdateTask.id, note);
      setMessage(selection.action === "update" ? copy(language, "更新任务已提交，等待节点实际完成并重新连接。", "Update queued; waiting for the node to finish and reconnect.") : copy(language, "已记录核查结果。", "Review recorded."));
      setSelection(null); setNote(""); await onRefresh();
    } catch (cause) { setError(userError(language, cause)); } finally { setBusy(false); }
  };
  return <>
  <Card className="mt-4">
    <CardHeader><CardTitle className="flex items-center gap-2"><ShieldCheckIcon aria-hidden="true" />Tailscale</CardTitle><CardDescription>{copy(language, "节点自动检查官方稳定版；由管理员逐台手动更新。", "Nodes check the official stable release; an administrator updates one node at a time.")}</CardDescription></CardHeader>
    <CardContent className="space-y-3">
      {agents.filter((agent) => agent.status === "active" && agent.tailscaleOwnership).map((agent) => {
        const status = agent.capabilities.tailscaleUpdate;
        const task = agent.tailscaleUpdateTask;
        const available = freshTailscaleUpdate(agent);
        const stale = !status?.checkedAt || !Number.isFinite(Date.parse(status.checkedAt)) || Date.now() - Date.parse(status.checkedAt) > 48 * 60 * 60_000 || Date.parse(status.checkedAt) > Date.now() + 5 * 60_000;
        const label = !agent.connected ? copy(language, "离线", "Offline") : !status ? copy(language, "尚未检查", "Not checked") : status.error ? copy(language, "检查失败", "Check failed") : stale ? copy(language, "检查已过期", "Check is stale") : status.installedVersion !== status.runningVersion ? copy(language, "运行版本与已安装版本不一致", "Running and installed versions differ") : available ? copy(language, "发现新版本", "New version available") : copy(language, "无可用更新", "No newer version");
        const taskLabel = task?.state === "pending" ? copy(language, "更新已排队", "Update queued") : task?.state === "running" ? copy(language, "正在准备更新", "Preparing update") : task?.state === "awaiting_decision" ? copy(language, "等待更新后重新连接和版本核实", "Waiting for reconnect and version verification") : task?.state === "succeeded" ? copy(language, "更新已完成", "Update completed") : task?.state === "failed" ? copy(language, "更新需要检查", "Update needs review") : "";
        const observationAfterUpdate = task?.state !== "succeeded" || Boolean(status?.checkedAt && Date.parse(status.checkedAt) > Date.parse(task.updatedAt));
        const canUpdate = Boolean(available && status?.upgradeReady && status.installedVersion === status.runningVersion && !hasActive && task?.state !== "failed" && observationAfterUpdate);
        const canReview = task?.state === "failed" || (task?.state === "awaiting_decision" || task?.state === "pending") && Date.now() - Date.parse(task.updatedAt) >= 20 * 60_000;
        return <div className="flex flex-wrap items-start justify-between gap-3 rounded-lg border p-3" key={agent.id}>
          <div><div className="font-medium">{agent.name}</div><div className="text-sm text-muted-foreground">{copy(language, "运行", "Running")}: {status?.runningVersion || "—"} · {copy(language, "已安装", "Installed")}: {status?.installedVersion || "—"} · {copy(language, "官方稳定版", "Upstream stable")}: {status?.latestVersion || "—"}</div>{status?.checkedAt && Number.isFinite(Date.parse(status.checkedAt)) ? <div className="text-xs text-muted-foreground">{copy(language, "上次检查", "Last checked")}: {formatDate(language, status.checkedAt)}</div> : null}{status?.error && agent.connected ? <div className="text-xs text-muted-foreground">{status.error === "upstream_unavailable" ? copy(language, "无法获取官方版本。", "The upstream version is unavailable.") : copy(language, "无法读取节点的 Tailscale 运行版本。", "The node's running Tailscale version is unavailable.")}</div> : null}{taskLabel ? <div className="mt-1 text-sm" role="status">{taskLabel}{task?.error ? ` · ${task.error}` : ""}</div> : null}{available && !status?.upgradeReady ? <div className="text-xs text-muted-foreground">{copy(language, "节点未配置受支持的官方软件源，需先核对。", "The node does not have a supported official package source.")}</div> : null}</div>
          <div className="flex items-center gap-2"><span className="text-sm">{label}</span>{canUpdate ? <Button disabled={busy} onClick={() => { setError(""); setSelection({ agent, action: "update" }); }} size="sm">{copy(language, "更新", "Update")}</Button> : null}{canReview ? <Button disabled={busy} onClick={() => { setError(""); setSelection({ agent, action: "review" }); }} size="sm" variant="outline">{copy(language, "核查", "Review")}</Button> : null}</div>
        </div>;
      })}
      <Button disabled={busy} onClick={() => void check()} size="sm" variant="outline">{busy ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "检查更新", "Check for updates")}</Button>
      {message ? <p className="text-sm text-muted-foreground" role="status">{message}</p> : null}
      {error && !selection ? <FieldError role="alert">{error}</FieldError> : null}
    </CardContent>
  </Card>
  <Sheet open={selection !== null} onOpenChange={(open) => { if (!open && !busy) { setSelection(null); setNote(""); } }}><SheetContent><SheetHeader><SheetTitle>{selection?.action === "review" ? copy(language, "核查更新结果", "Review update") : copy(language, `更新 ${selection?.agent.name ?? ""} 的 Tailscale`, `Update Tailscale on ${selection?.agent.name ?? ""}`)}</SheetTitle><SheetDescription>{selection?.action === "review" ? copy(language, "确认节点上的更新进程已停止，且已检查软件包和实际网络状态。此操作只记录核查，不会撤销已经发生的变更。", "Confirm the host update process has stopped and inspect the package and network. This records review and does not undo changes.") : copy(language, `将更新到 ${selection?.agent.capabilities.tailscaleUpdate?.latestVersion ?? ""}。私网可能短暂断开，页面将在节点重连且实际版本核实后显示完成。`, `Updates to ${selection?.agent.capabilities.tailscaleUpdate?.latestVersion ?? ""}. The private network may briefly disconnect; completion requires a reconnect and version verification.`)}</SheetDescription></SheetHeader><div className="flex-1 px-4">{selection?.action === "review" ? <><label className="text-sm font-medium" htmlFor="tailscale-review-note">{copy(language, "核查说明", "Review note")}</label><Textarea id="tailscale-review-note" value={note} onChange={(event) => setNote(event.target.value)} maxLength={500} /></> : null}{error ? <FieldError className="mt-4" role="alert">{error}</FieldError> : null}</div><SheetFooter><Button disabled={busy} variant="outline" onClick={() => setSelection(null)}>{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || selection?.action === "review" && !note.trim()} onClick={() => void submit()}>{busy ? <Spinner data-icon="inline-start" /> : null}{selection?.action === "review" ? copy(language, "确认已核查", "Confirm review") : copy(language, "开始更新", "Start update")}</Button></SheetFooter></SheetContent></Sheet>
  </>;
}
