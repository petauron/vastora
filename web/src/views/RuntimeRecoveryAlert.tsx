import { useEffect, useState } from "react";
import { GitCompareArrowsIcon, NetworkIcon, RotateCcwIcon } from "lucide-react";
import { api } from "../api";
import type { AgentView, XrayConfigurationRecovery } from "../types";
import type { Language } from "../translations";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { copy } from "./shared";

export function RuntimeRecoveryAlert({ agent, language, onApplications }: { agent: AgentView; language: Language; onApplications: () => void }) {
  const [recoveryOpen, setRecoveryOpen] = useState(false);
  if (!agent.connected || !agent.runtimeRecovery) return null;
  const applications = agent.runtimeRecoveryApplications ?? [];
  const applicationBlocked = agent.runtimeRecovery === "application" && applications.length > 0;
  const xrayBlocked = applications.some((application) => application.appKey === "vastora-official/3x-ui");
  return <Alert variant={applicationBlocked ? "destructive" : "default"}>
    <NetworkIcon aria-hidden="true" />
    <AlertTitle>{applicationBlocked ? copy(language, "应用恢复受阻", "Application recovery blocked") : copy(language, "服务正在恢复", "Restoring services")}</AlertTitle>
    <AlertDescription>
      <p>{recoveryDescription(language, agent.runtimeRecovery)}</p>
      {applicationBlocked ? <><ul className="list-disc pl-4">{applications.map((application) => <li className="break-words" key={application.appKey}><span className="font-mono">{application.appKey}</span>{" · "}{recoveryReason(language, application.reason)}</li>)}</ul><p>{copy(language, "若 Xray 当前运行配置与 Agent 状态不一致，可先检查差异，再显式选择权威来源。所有权或本地状态无法确认时仍需人工处理。", "If the running Xray configuration differs from Agent state, inspect the difference and explicitly choose the authority. Unproven ownership or local state still requires operator intervention.")}</p><div className="flex flex-wrap gap-2"><Button onClick={onApplications} size="sm" type="button" variant="outline">{copy(language, "查看应用", "View apps")}</Button>{xrayBlocked ? <Button onClick={() => setRecoveryOpen(true)} size="sm" type="button" variant="outline"><GitCompareArrowsIcon aria-hidden="true" data-icon="inline-start" />{copy(language, "显式配置恢复", "Explicit config recovery")}</Button> : null}</div></> : null}
    </AlertDescription>
    <XrayConfigurationRecoverySheet agent={agent} language={language} onOpenChange={setRecoveryOpen} open={recoveryOpen} />
  </Alert>;
}

export function XrayConfigurationRecoverySheet({ agent, language, onOpenChange, open }: { agent: AgentView; language: Language; onOpenChange: (open: boolean) => void; open: boolean }) {
  const [recovery, setRecovery] = useState<XrayConfigurationRecovery | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [source, setSource] = useState<"runtime" | "agent_state" | "">("");
  const [confirmed, setConfirmed] = useState(false);
  const load = async (signal?: AbortSignal) => {
    setLoading(true);
    try { setRecovery((await api.xrayConfigurationRecovery(agent.id, signal)).recovery); setError(""); }
    catch (requestError) { if (!signal?.aborted) setError(recoveryError(language, requestError)); }
    finally { if (!signal?.aborted) setLoading(false); }
  };
  useEffect(() => { if (!open) return; const request = new AbortController(); void load(request.signal); return () => request.abort(); }, [open, agent.id]);
  useEffect(() => { if (!open || recovery?.state !== "pending" && recovery?.state !== "running") return; const request = new AbortController(); const timer = window.setTimeout(() => void load(request.signal), 1500); return () => { request.abort(); window.clearTimeout(timer); }; }, [open, recovery?.id, recovery?.state, recovery?.updatedAt]);
  const inspect = async () => { setBusy(true); setError(""); setSource(""); setConfirmed(false); try { await api.inspectXrayConfiguration(agent.id); await load(); } catch (requestError) { setError(recoveryError(language, requestError)); } finally { setBusy(false); } };
  const apply = async () => { if (!source || !confirmed) return; setBusy(true); setError(""); try { await api.applyXrayConfigurationRecovery(agent.id, source); await load(); } catch (requestError) { setError(recoveryError(language, requestError)); } finally { setBusy(false); } };
  const result = recovery?.result;
  const processing = recovery?.state === "pending" || recovery?.state === "running";
  return <Sheet onOpenChange={onOpenChange} open={open}><SheetContent className="sm:max-w-2xl"><SheetHeader><SheetTitle>{copy(language, `${agent.name} · 显式配置恢复`, `${agent.name} · Explicit configuration recovery`)}</SheetTitle><SheetDescription>{copy(language, "先只读检查当前运行配置与 Agent 状态。确认差异后，只能选择其中一方作为权威来源。", "First compare the running configuration and Agent state without changing either. Then choose exactly one authority.")}</SheetDescription></SheetHeader><div className="flex-1 overflow-y-auto px-4"><FieldGroup>
    {loading && !recovery ? <Alert><Spinner /><AlertTitle>{copy(language, "正在读取恢复状态", "Loading recovery state")}</AlertTitle></Alert> : null}
    {processing ? <Alert><Spinner /><AlertTitle>{recovery?.action === "inspect" ? copy(language, "正在检查配置", "Inspecting configurations") : copy(language, "正在重建状态和回执", "Rebuilding state and receipt")}</AlertTitle><AlertDescription>{copy(language, "请保持节点在线；页面会自动刷新。", "Keep the node online; this view refreshes automatically.")}</AlertDescription></Alert> : null}
    {recovery?.state === "failed" ? <Alert variant="destructive"><AlertTitle>{copy(language, "恢复任务失败", "Recovery task failed")}</AlertTitle><AlertDescription>{recovery.error || copy(language, "请重新检查后再决定。", "Inspect again before making another decision.")}</AlertDescription></Alert> : null}
    {recovery?.state === "succeeded" ? <Alert><NetworkIcon aria-hidden="true" /><AlertTitle>{copy(language, "配置恢复完成", "Configuration recovery completed")}</AlertTitle><AlertDescription>{copy(language, `已采用${recovery.action === "runtime" ? "当前运行配置" : "Agent 状态"}，状态与应用回执已重建。`, `The ${recovery.action === "runtime" ? "running configuration" : "Agent state"} is authoritative; state and the receipt were rebuilt.`)}</AlertDescription></Alert> : null}
    {result ? <><div className="grid gap-3 sm:grid-cols-2"><SourceCard title={copy(language, "当前运行配置", "Running configuration")} countLabel={copy(language, "个入站", "inbounds")} digest={result.runtimeSha256} inbounds={result.runtimeInbounds.length} selected={source === "runtime"} disabled={!result.runtimeImportable || processing} onSelect={() => { setSource("runtime"); setConfirmed(false); }} note={result.runtimeImportable ? copy(language, "不重启 Xray；将运行态写回 Agent 状态。", "No Xray restart; write the runtime back into Agent state.") : copy(language, "无法无损转换，不能作为权威来源。", "Cannot be converted without loss.")} /><SourceCard title={copy(language, "Agent 状态", "Agent state")} countLabel={copy(language, "个入站", "inbounds")} digest={result.agentSha256} inbounds={result.agentInbounds.length} selected={source === "agent_state"} disabled={processing} onSelect={() => { setSource("agent_state"); setConfirmed(false); }} note={copy(language, "校验后替换运行文件并重启 Xray。", "Validate, replace the active file, and restart Xray.")} /></div><DifferenceTable language={language} differences={result.differences} /></> : null}
    {source ? <Field orientation="horizontal"><FieldLabel htmlFor="xray-recovery-confirm"><span className="flex flex-col gap-1"><span>{copy(language, source === "runtime" ? "确认以当前运行配置为权威" : "确认以 Agent 状态为权威并重启 Xray", source === "runtime" ? "Use the running configuration as authority" : "Use Agent state as authority and restart Xray")}</span><span className="text-sm font-normal text-muted-foreground">{copy(language, "执行前会再次核对两侧指纹；任何变化都会停止操作。", "Both fingerprints are checked again before execution; any change stops the operation.")}</span></span></FieldLabel><Switch checked={confirmed} id="xray-recovery-confirm" onCheckedChange={setConfirmed} /></Field> : null}
    {error ? <FieldError role="alert">{error}</FieldError> : null}
  </FieldGroup></div><SheetFooter><Button onClick={() => onOpenChange(false)} type="button" variant="outline">{copy(language, "关闭", "Close")}</Button><Button disabled={busy || processing} onClick={() => void inspect()} type="button" variant="outline"><RotateCcwIcon aria-hidden="true" data-icon="inline-start" />{copy(language, result ? "重新检查" : "检查差异", result ? "Inspect again" : "Inspect differences")}</Button>{result && recovery?.state === "awaiting_decision" ? <Button disabled={busy || !source || !confirmed || source === "runtime" && !result.runtimeImportable} onClick={() => void apply()} type="button">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "开始恢复", "Start recovery")}</Button> : null}</SheetFooter></SheetContent></Sheet>;
}

function SourceCard({ countLabel, digest, disabled, inbounds, note, onSelect, selected, title }: { countLabel: string; digest: string; disabled: boolean; inbounds: number; note: string; onSelect: () => void; selected: boolean; title: string }) {
  return <button aria-pressed={selected} className={`min-h-28 cursor-pointer rounded-lg border p-3 text-left transition-colors focus-visible:ring-2 focus-visible:ring-ring ${selected ? "border-primary bg-primary/5" : "hover:bg-muted/50"} disabled:cursor-not-allowed disabled:opacity-50`} disabled={disabled} onClick={onSelect} type="button"><span className="block font-medium">{title}</span><span className="mt-1 block font-mono text-xs text-muted-foreground">{inbounds} {countLabel} · {digest.slice(0, 12)}</span><span className="mt-3 block text-xs leading-5 text-muted-foreground">{note}</span></button>;
}

function DifferenceTable({ differences, language }: { differences: NonNullable<XrayConfigurationRecovery["result"]>["differences"]; language: Language }) {
  return <div className="overflow-x-auto rounded-lg border"><Table><TableHeader><TableRow><TableHead>{copy(language, "入站", "Inbound")}</TableHead><TableHead>{copy(language, "差异", "Difference")}</TableHead><TableHead>{copy(language, "运行配置", "Runtime")}</TableHead><TableHead>{copy(language, "Agent", "Agent")}</TableHead></TableRow></TableHeader><TableBody>{differences.length === 0 ? <TableRow><TableCell colSpan={4}>{copy(language, "两侧配置一致，只需重建状态与回执。", "Both sides match; only state and the receipt need rebuilding.")}</TableCell></TableRow> : differences.map((difference, index) => <TableRow key={`${difference.kind}-${difference.inbound}-${difference.field}-${index}`}><TableCell className="font-mono text-xs">{difference.inbound || "—"}</TableCell><TableCell>{differenceLabel(language, difference.kind, difference.field)}</TableCell><TableCell>{difference.runtime || "—"}</TableCell><TableCell>{difference.agent || "—"}</TableCell></TableRow>)}</TableBody></Table></div>;
}

function differenceLabel(language: Language, kind: string, field?: string) {
  if (kind === "missing_runtime") return copy(language, "仅 Agent 状态存在", "Agent state only");
  if (kind === "missing_agent") return copy(language, "仅运行配置存在", "Runtime only");
  if (kind === "settings") return copy(language, "路由或策略不同", "Routing or policy differs");
  const label = field === "protocol" ? copy(language, "协议", "Protocol") : field === "port" ? copy(language, "端口", "Port") : field === "clients" ? copy(language, "客户端数量", "Client count") : copy(language, "字段", "Field");
  return copy(language, `${label}不同`, `${label} differs`);
}

function recoveryError(language: Language, error: unknown) { return error instanceof Error && error.message ? error.message : copy(language, "无法处理配置恢复，请检查节点状态。", "Configuration recovery could not be processed. Check the node state."); }

function recoveryDescription(language: Language, stage: NonNullable<AgentView["runtimeRecovery"]>) {
  switch (stage) {
    case "reconciliation": return copy(language, "正在恢复上次未完成的操作，完成后会继续启动服务。", "Recovering the interrupted operation before starting services.");
    case "landing": return copy(language, "落地配置尚未恢复。请检查落地状态或提交停用操作；无法确认安全状态时需要人工处理。", "The landing configuration has not recovered. Inspect it or submit a disable operation; an unproven safe state requires operator intervention.");
    case "application": return copy(language, "节点管理连接正常，但应用恢复尚未完成。无法确认运行配置与 Agent 状态时，必须由操作员显式恢复。", "The management connection is available, but application recovery is incomplete. An operator must explicitly recover an unproven runtime and Agent state.");
    case "gateway": return copy(language, "访问入口尚未恢复，系统会自动重试。持续失败时需人工核对网关状态。", "Service access has not recovered yet. Persistent failures require operator reconciliation of gateway state.");
    case "listener": return copy(language, "代理入口尚未恢复，系统会自动重试。恢复成功前不接受新路由。", "Proxy access has not recovered yet. New routes wait until recovery succeeds.");
    default: return copy(language, "节点已连接，正在恢复服务。", "The node is connected and its services are being restored.");
  }
}

function recoveryReason(language: Language, reason: NonNullable<AgentView["runtimeRecoveryApplications"]>[number]["reason"]) {
  switch (reason) {
    case "state_incomplete": return copy(language, "本地安装状态不完整", "Local installation state is incomplete");
    case "image_unavailable": return copy(language, "离线恢复所需镜像不可用", "The image required for offline recovery is unavailable");
    case "health_check_failed": return copy(language, "应用健康检查未通过", "Application health check failed");
    default: return copy(language, "应用恢复失败", "Application restore failed");
  }
}
