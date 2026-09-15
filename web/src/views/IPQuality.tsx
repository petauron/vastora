import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { ActivityIcon, ChevronRightIcon, RefreshCwIcon } from "lucide-react";
import { api, APIError } from "../api";
import type { AgentView } from "../types";
import type { Language } from "../translations";
import type { IPQualityCheck, IPQualityClassification } from "../ip-quality-types";
import type { Carrier, NodeDiagnosticCheck, NetworkMeasurement } from "../node-diagnostics-types";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Spinner } from "@/components/ui/spinner";
import { cn } from "@/lib/utils";
import { copy } from "./shared";
import { RegionFlag } from "./RegionFlag";
import { checkPending, cleanIPQualityValue, ipQualityError, ipQualitySummary, unlockLabel, unlockTypeLabel } from "./ipQualityModel";

type QualityState = {
  checks: IPQualityCheck[]; diagnostics: NodeDiagnosticCheck[]; agents: AgentView[]; loading: boolean; error: boolean;
  refresh: () => Promise<void>;
};
const QualityContext = createContext<QualityState | null>(null);

function carrierLabel(language: Language, carrier: Carrier) {
  return carrier === "telecom" ? copy(language, "中国电信", "China Telecom") : carrier === "unicom" ? copy(language, "中国联通", "China Unicom") : copy(language, "中国移动", "China Mobile");
}

function ClassificationTable({ language, title, values }: { language: Language; title: string; values: IPQualityClassification[] }) {
  return <section className="space-y-2"><h3 className="font-medium">{title}</h3><Table><TableHeader><TableRow><TableHead>{copy(language, "来源", "Provider")}</TableHead><TableHead>{copy(language, "分类", "Classification")}</TableHead></TableRow></TableHeader><TableBody>{values.map((value) => <TableRow key={value.source}><TableCell>{value.source}</TableCell><TableCell>{value.value}</TableCell></TableRow>)}{!values.length ? <TableRow><TableCell colSpan={2}>{copy(language, "暂无分类数据", "No classification data")}</TableCell></TableRow> : null}</TableBody></Table></section>;
}

function UnlockIndicators({ check, language }: { check: IPQualityCheck; language: Language }) {
  const summary = ipQualitySummary(language, check);
  return <span aria-label={summary} className="inline-flex shrink-0 items-center gap-1.5">{["Netflix", "ChatGPT"].map((name) => {
    const service = check.report?.services.find((item) => item.name === name);
    const status = cleanIPQualityValue(service?.status).toLowerCase();
    const label = unlockLabel(language, service?.status);
    return <span aria-hidden="true" className={cn("size-2.5 shrink-0 rounded-[2px] border", status === "yes" ? "border-transparent bg-latency-fast" : status === "no" ? "border-destructive bg-transparent" : "border-muted-foreground/40 bg-muted")} key={name} title={`${name} · ${label}`} />;
  })}</span>;
}

function latencyTone(value: number) {
  return value < 80 ? "text-latency-fast" : value < 150 ? "text-latency-medium" : "text-destructive";
}

function networkSummary(network?: NetworkMeasurement[]) {
  return ["telecom", "unicom", "mobile"].map((carrier) => network?.find((value) => value.carrier === carrier));
}

function humanBytes(bytes: number) {
  return bytes >= 1024 ** 3 ? `${(bytes / 1024 ** 3).toFixed(1)} GiB` : `${Math.round(bytes / 1024 ** 2)} MiB`;
}

function recommendationReason(language: Language, reason?: string) {
  if (reason === "available_low_latency_control") return copy(language, "内核支持", "Kernel supported");
  if (reason === "available_pacing_queue") return copy(language, "已加载队列模块", "Queue module loaded");
  if (reason === "avoid_idle_restart") return copy(language, "避免空闲后重新慢启动", "Avoid slow start after idle");
  if (reason === "requires_path_measurement") return copy(language, "需结合线路实测，暂不更改", "Keep until path is measured");
  return copy(language, "保持当前值", "Keep current value");
}

// The fleet table reads saved snapshots only. Page refreshes never start probes.
export function NodeHealthCells({ agent, language }: { agent: AgentView; language: Language }) {
  const state = useContext(QualityContext);
  const network = state?.diagnostics.find((value) => value.agentId === agent.id && value.kind === "node.network-quality");
  const host = state?.diagnostics.find((value) => value.agentId === agent.id && value.kind === "node.host-profile");
  const latest = [network?.checkedAt, host?.checkedAt].filter((value): value is string => Boolean(value)).sort().at(-1);
  return <>
    <TableCell className="max-md:hidden"><div className="flex min-w-0 flex-col gap-0.5 text-xs tabular-nums">{networkSummary(network?.network).map((value, index) => <span className="flex gap-1.5" key={index}><span className="w-5 text-muted-foreground">{["电", "联", "移"][index]}</span><span className={value ? latencyTone(value.latencyMs) : "text-muted-foreground"}>{value ? `${Math.round(value.latencyMs)} ms` : "—"}</span></span>)}</div></TableCell>
    <TableCell className="max-md:hidden"><span className="block text-xs tabular-nums">{host?.host ? `${host.host.cpuCount} vCPU` : "—"}</span><span className="block text-xs tabular-nums text-muted-foreground">{host?.host ? humanBytes(host.host.memoryBytes) : copy(language, "未采集", "Not collected")}</span></TableCell>
    <TableCell className="max-md:hidden"><span className="block text-xs">{host?.host ? `${host.host.congestionControl || "—"} / ${host.host.defaultQdisc || "—"}` : "—"}</span><span className="block text-xs text-muted-foreground">{host?.host ? host.host.persistentConfig ? copy(language, "发现配置文件", "Config file found") : copy(language, "无 tcpfit 配置", "No tcpfit config") : copy(language, "未采集", "Not collected")}</span></TableCell>
    <TableCell className="text-xs tabular-nums text-muted-foreground max-md:hidden">{latest ? new Date(latest).toLocaleString(language, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" }) : "—"}</TableCell>
  </>;
}

export function NodeHealthInline({ agent }: { agent: AgentView }) {
  const state = useContext(QualityContext);
  const network = state?.diagnostics.find((value) => value.agentId === agent.id && value.kind === "node.network-quality");
  return <span className="mt-1 flex gap-2 text-[11px] tabular-nums md:hidden">{networkSummary(network?.network).map((value, index) => <span className={value ? latencyTone(value.latencyMs) : "text-muted-foreground"} key={index}>{["电", "联", "移"][index]} {value ? `${Math.round(value.latencyMs)} ms` : "—"}</span>)}</span>;
}

// One read per page, then poll only while an explicitly requested check exists.
// A list refresh never starts a diagnostic on any node.
function DiagnosticsProvider({ agents, enabled, includeIPQuality, children }: { agents: AgentView[]; enabled: boolean; includeIPQuality: boolean; children: ReactNode }) {
  const [checks, setChecks] = useState<IPQualityCheck[]>([]);
  const [diagnostics, setDiagnostics] = useState<NodeDiagnosticCheck[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const pending = useRef<AbortController | null>(null);
  const refresh = useCallback(async () => {
    pending.current?.abort();
    const request = new AbortController();
    pending.current = request;
    const timeout = setTimeout(() => request.abort(), 15000);
    setLoading(true);
    try {
      const [quality, nodeDiagnostics] = await Promise.all([includeIPQuality ? api.ipQuality(request.signal) : Promise.resolve({ checks: [] }), api.nodeDiagnostics(request.signal)]);
      if (!request.signal.aborted) { setChecks(quality.checks); setDiagnostics(nodeDiagnostics.checks); setError(false); }
    } catch {
      if (pending.current === request) setError(true);
    } finally {
      clearTimeout(timeout);
      if (pending.current === request) { setLoading(false); pending.current = null; }
    }
  }, [includeIPQuality]);
  useEffect(() => {
    if (enabled) void refresh();
    return () => { const request = pending.current; pending.current = null; request?.abort(); };
  }, [enabled, refresh]);
  const active = checks.some(checkPending) || diagnostics.some((value) => value.state === "pending" || value.state === "running");
  useEffect(() => {
    if (!enabled || !active || error || loading) return;
    const timer = setTimeout(() => { if (!document.hidden) void refresh(); }, 4000);
    const visible = () => { if (!document.hidden) void refresh(); };
    document.addEventListener("visibilitychange", visible);
    return () => { clearTimeout(timer); document.removeEventListener("visibilitychange", visible); };
  }, [enabled, active, error, loading, refresh]);
  return <QualityContext.Provider value={{ checks, diagnostics, agents, loading, error, refresh }}>{children}</QualityContext.Provider>;
}

export function IPQualityProvider({ agents, enabled, children }: { agents: AgentView[]; enabled: boolean; children: ReactNode }) {
  return <DiagnosticsProvider agents={agents} enabled={enabled} includeIPQuality>{children}</DiagnosticsProvider>;
}

export function NodeDiagnosticsProvider({ agents, enabled, children }: { agents: AgentView[]; enabled: boolean; children: ReactNode }) {
  return <DiagnosticsProvider agents={agents} enabled={enabled} includeIPQuality={false}>{children}</DiagnosticsProvider>;
}

type DiagnosticsButtonProps = { nodeId: string; name: string; language: Language; compact?: boolean };

export function IPQualityButton(props: DiagnosticsButtonProps) {
  return <DiagnosticsButton {...props} includeIPQuality />;
}

export function NodeDiagnosticsButton(props: DiagnosticsButtonProps) {
  return <DiagnosticsButton {...props} includeIPQuality={false} />;
}

function DiagnosticsButton({ nodeId, name, language, compact = false, includeIPQuality }: DiagnosticsButtonProps & { includeIPQuality: boolean }) {
  const state = useContext(QualityContext);
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState(includeIPQuality ? "overview" : "network");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const pending = useRef<AbortController | null>(null);
  useEffect(() => () => { const request = pending.current; pending.current = null; request?.abort(); }, []);
  if (!state) return null;
  const agent = state.agents.find((value) => value.id === nodeId);
  const check = state.checks.find((value) => value.agentId === nodeId);
  const report = check?.report;
  const networkCheck = state.diagnostics.find((value) => value.agentId === nodeId && value.kind === "node.network-quality");
  const routeCheck = state.diagnostics.find((value) => value.agentId === nodeId && value.kind === "node.return-route");
  const bandwidthCheck = state.diagnostics.find((value) => value.agentId === nodeId && value.kind === "node.international-bandwidth");
  const hostCheck = state.diagnostics.find((value) => value.agentId === nodeId && value.kind === "node.host-profile");
  const active = checkPending(check);
  const unavailable = !agent || agent.status !== "active" || agent.credentialRevoked ? "ip_quality_node_unavailable"
    : !agent.connected ? "ip_quality_node_offline"
    : !agent.capabilities.ipQuality || !agent.capabilities.docker ? "ip_quality_agent_upgrade_required"
    : !agent.publicEgress?.address ? "ip_quality_address_unavailable" : "";
  const diagnosticsUnavailable = !agent || agent.status !== "active" || agent.credentialRevoked || !agent.connected || !agent.publicEgress?.address;
  const start = async () => {
    if (pending.current || active || unavailable || state.error || state.loading) return;
    const request = new AbortController();
    pending.current = request;
    const timeout = setTimeout(() => request.abort(), 15000);
    setSubmitting(true); setError("");
    try {
      await api.checkIPQuality(nodeId, request.signal);
      if (pending.current !== request) return;
      await state.refresh();
    } catch (cause) {
      if (pending.current === request) {
        setError(cause instanceof APIError ? cause.code : "request_unconfirmed");
        // A lost POST response is not permission to submit again. Read first.
        await state.refresh();
      }
    } finally {
      clearTimeout(timeout);
      if (pending.current === request) { pending.current = null; setSubmitting(false); }
    }
  };
  const startDiagnostic = async (kind: NodeDiagnosticCheck["kind"]) => {
    if (pending.current || state.error || state.loading) return;
    const request = new AbortController();
    pending.current = request;
    const timeout = setTimeout(() => request.abort(), 15000);
    setSubmitting(true); setError("");
    try {
      await api.checkNodeDiagnostic(nodeId, kind, request.signal);
      if (pending.current === request) await state.refresh();
    } catch (cause) {
      if (pending.current === request) { setError(cause instanceof APIError ? cause.code : "request_unconfirmed"); await state.refresh(); }
    } finally {
      clearTimeout(timeout);
      if (pending.current === request) { pending.current = null; setSubmitting(false); }
    }
  };
  const summary = state.error ? copy(language, "节点诊断 · 读取失败", "Node diagnostics · Unavailable") : includeIPQuality ? ipQualitySummary(language, check) : copy(language, "主机与网络诊断", "Host and network diagnostics");
  const score = !check?.stale && !check?.error && !active ? report?.scores.find((value) => value.source === "IPQS") : undefined;
  const showUnlockIndicators = Boolean(!state.error && check?.report && !check.stale && !check.error && !active);
  return <Sheet open={open} onOpenChange={(value) => { setOpen(value); if (value) { setTab(includeIPQuality ? "overview" : "network"); setError(""); void state.refresh(); } }}>
    <SheetTrigger render={<Button type="button" variant="ghost" size={compact ? "icon-sm" : "sm"} className={compact ? "shrink-0" : "h-auto min-h-6 max-w-full justify-start px-1 py-0 text-left text-xs text-muted-foreground max-md:min-h-11"} />} aria-label={compact ? copy(language, `查看 ${name} 的节点诊断`, `View node diagnostics for ${name}`) : copy(language, `查看 ${name} 的节点诊断：${summary}`, `View node diagnostics for ${name}: ${summary}`)} title={compact ? copy(language, "查看节点诊断", "View node diagnostics") : `${summary}${score ? ` · IPQS ${score.value}` : ""}`}>
      {compact ? <ActivityIcon aria-hidden="true" /> : <><span className="inline-flex min-w-0 items-center gap-2 overflow-hidden">{showUnlockIndicators && check ? <UnlockIndicators check={check} language={language} /> : <span className="truncate whitespace-nowrap">{summary}</span>}{score ? <span className="shrink-0 whitespace-nowrap">· IPQS {score.value}</span> : null}</span><ChevronRightIcon className="shrink-0" aria-hidden="true" /></>}
    </SheetTrigger>
    {open ? <SheetContent className="data-[side=right]:w-full data-[side=right]:sm:max-w-3xl">
      <SheetHeader className="pr-12">
        <SheetTitle>{name} · {copy(language, "节点诊断", "Node diagnostics")}</SheetTitle>
        <SheetDescription>{includeIPQuality ? copy(language, "检测此代理入口或落地机自身的公网出口，不代表入口 → 落地组合的链路实测。", "Checks this proxy entry or landing host's own public exit, not an entry-to-landing route.") : copy(language, "查看这台基础设施节点的主机参数与按需网络诊断；不包含代理出口的 IP 质量或解锁结果。", "Shows host values and on-demand network diagnostics for this infrastructure node; proxy-exit IP quality and unlock results are excluded.")}</SheetDescription>
      </SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto px-4 pb-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 text-xs text-muted-foreground">
            {includeIPQuality ? <p className="break-words">{copy(language, "当前出口", "Current exit")}: {agent?.publicEgress?.address ?? "—"}</p> : <p>{copy(language, "节点诊断按需执行，不会在后台持续探测。", "Node diagnostics run on demand and do not probe continuously in the background.")}</p>}
            {includeIPQuality && check?.checkedAt ? <p className="mt-1">{copy(language, "最近结果", "Last result")}: {new Date(check.checkedAt).toLocaleString(language)}</p> : null}
          </div>
          <Button size="icon-sm" variant="ghost" disabled={state.loading} onClick={() => void state.refresh()} aria-label={copy(language, "刷新检测状态", "Refresh check status")}><RefreshCwIcon aria-hidden="true" /></Button>
        </div>
        {(includeIPQuality && active) || submitting ? <p role="status" className="flex items-center gap-2 text-sm"><Spinner aria-hidden="true" />{submitting ? copy(language, "正在提交检测…", "Submitting check…") : check?.state === "pending" ? copy(language, "等待节点执行，可关闭此面板。", "Waiting for the node. You can close this panel.") : copy(language, "正在检测，可关闭此面板。", "Checking. You can close this panel.")}</p> : null}
        {state.error || error || includeIPQuality && check?.error ? <p role="alert" className="text-sm text-destructive">{ipQualityError(language, state.error ? "read_failed" : error || check!.error!)}</p> : null}
        {includeIPQuality && check?.stale ? <p role="status" className="text-sm text-destructive">{copy(language, "出口 IP 已变化。以下为旧 IP 的结果，请重新检测。", "The exit IP changed. The results below belong to the previous IP; run a new check.")}</p> : null}
        <Tabs value={tab} onValueChange={setTab} className="min-w-0">
          <TabsList className={cn("grid h-auto w-full grid-cols-2", includeIPQuality ? "sm:grid-cols-6" : "sm:grid-cols-4")}>
            {includeIPQuality ? <TabsTrigger value="overview">{copy(language, "概览", "Overview")}</TabsTrigger> : null}
            {includeIPQuality ? <TabsTrigger value="quality">{copy(language, "IP 质量", "IP quality")}</TabsTrigger> : null}
            <TabsTrigger value="network">{copy(language, "网络质量", "Network")}</TabsTrigger>
            <TabsTrigger value="route">{copy(language, "回程路由", "Return route")}</TabsTrigger>
            <TabsTrigger value="bandwidth">{copy(language, "国际带宽", "Bandwidth")}</TabsTrigger>
            <TabsTrigger value="host">{copy(language, "主机与 TCP", "Host & TCP")}</TabsTrigger>
          </TabsList>
          {includeIPQuality ? <TabsContent value="overview" className="space-y-4 pt-3">{report ? <>
            <p className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground"><RegionFlag code={report.regionCode} language={language} /><span className="break-words">{copy(language, "检测出口", "Checked exit")}: {report.address}</span><span>IPQuality {report.version}</span></p>
            <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2">
              {[[copy(language, "ASN", "ASN"), report.asn], [copy(language, "组织 / 运营商", "Organization / ISP"), report.organization], [copy(language, "国家或地区", "Country or region"), report.regionName || report.regionCode], [copy(language, "注册地区", "Registered region"), report.registeredRegion || report.registeredCode], [copy(language, "城市", "City"), report.city], [copy(language, "时区", "Time zone"), report.timeZone]].map(([label, value]) => <div key={label}><dt className="text-xs text-muted-foreground">{label}</dt><dd className="mt-1 break-words">{value || "—"}</dd></div>)}
            </dl>
            <ClassificationTable language={language} title={copy(language, "IP 使用类型", "IP usage type")} values={report.usageTypes ?? []} />
            <ClassificationTable language={language} title={copy(language, "注册 / 公司类型", "Registration / company type")} values={report.companyTypes ?? []} />
          </> : <p className="py-6 text-center text-sm text-muted-foreground">{copy(language, "尚无 IP 检测结果", "No IP report yet")}</p>}</TabsContent> : null}
          {includeIPQuality ? <TabsContent value="quality" className="space-y-5 pt-3">{report ? <>
            <section className="space-y-2" aria-label={copy(language, "风险因子", "Risk factors")}><h3 className="font-medium">{copy(language, "风险因子", "Risk factors")}</h3><Table><TableHeader><TableRow><TableHead>{copy(language, "来源", "Provider")}</TableHead><TableHead>{copy(language, "项目", "Factor")}</TableHead><TableHead>{copy(language, "结果", "Result")}</TableHead></TableRow></TableHeader><TableBody>{(report.riskFactors ?? []).map((factor) => <TableRow key={`${factor.source}-${factor.kind}`}><TableCell>{factor.source}</TableCell><TableCell>{factor.kind}</TableCell><TableCell className={factor.value ? "text-destructive" : "text-latency-fast"}>{factor.value ? copy(language, "是", "Yes") : copy(language, "否", "No")}</TableCell></TableRow>)}{!(report.riskFactors ?? []).length ? <TableRow><TableCell colSpan={3}>{copy(language, "暂无风险因子数据", "No risk-factor data")}</TableCell></TableRow> : null}</TableBody></Table></section>
            <section className="space-y-2" aria-label={copy(language, "风险评分", "Risk scores")}><h3 className="font-medium">{copy(language, "风险评分", "Risk scores")}</h3><p className="text-xs text-muted-foreground">{copy(language, "各来源口径不同，保留原始分值，不合成为总分；缺失不代表零风险。", "Providers use different scales. Values are not averaged; missing data does not mean zero risk.")}</p><Table><TableHeader><TableRow><TableHead>{copy(language, "来源", "Provider")}</TableHead><TableHead className="text-right">{copy(language, "原始分值", "Reported value")}</TableHead></TableRow></TableHeader><TableBody>{report.scores.map((value) => <TableRow key={value.source}><TableCell>{value.source}</TableCell><TableCell className="text-right tabular-nums">{value.value}</TableCell></TableRow>)}{!report.scores.length ? <TableRow><TableCell colSpan={2}>{copy(language, "暂无评分数据", "No score data")}</TableCell></TableRow> : null}</TableBody></Table></section>
            <section className="space-y-2" aria-label={copy(language, "流媒体与 AI 解锁", "Streaming and AI availability")}><h3 className="font-medium">{copy(language, "流媒体与 AI 解锁", "Streaming and AI availability")}</h3><Table><TableHeader><TableRow><TableHead>{copy(language, "服务", "Service")}</TableHead><TableHead>{copy(language, "结果", "Result")}</TableHead><TableHead>{copy(language, "地区", "Region")}</TableHead></TableRow></TableHeader><TableBody>{report.services.map((service) => {
              const status = cleanIPQualityValue(service.status).toLowerCase();
              const type = unlockTypeLabel(language, service.type);
              return <TableRow key={service.name}><TableCell className="whitespace-normal">{service.name === "AmazonPrimeVideo" ? "Prime Video" : service.name === "DisneyPlus" ? "Disney+" : service.name}</TableCell><TableCell className={cn("whitespace-normal", status === "yes" ? "text-latency-fast" : status === "no" ? "text-destructive" : "text-muted-foreground")}>{unlockLabel(language, service.status)}{type ? <span className="block text-xs text-muted-foreground">{type}</span> : null}</TableCell><TableCell><span className="inline-flex gap-1"><RegionFlag code={service.regionCode} language={language} />{service.regionCode ?? "—"}</span></TableCell></TableRow>;
            })}{!report.services.length ? <TableRow><TableCell colSpan={3}>{copy(language, "暂无解锁结果", "No availability results")}</TableCell></TableRow> : null}</TableBody></Table></section>
          </> : <p className="py-6 text-center text-sm text-muted-foreground">{copy(language, "尚无 IP 质量结果", "No IP-quality report yet")}</p>}</TabsContent> : null}
          <TabsContent value="network" className="space-y-4 pt-3">
            <p className="text-xs text-muted-foreground">{copy(language, "按需对三网固定目标执行 4 次 TCP 连接采样；不持续后台探测。", "Runs four on-demand TCP connection samples against fixed carrier targets; no background probing.")}</p>
            <Table><TableHeader><TableRow><TableHead>{copy(language, "目标", "Target")}</TableHead><TableHead className="text-right">{copy(language, "延迟", "Latency")}</TableHead><TableHead className="text-right">{copy(language, "抖动", "Jitter")}</TableHead><TableHead className="text-right">{copy(language, "丢失", "Loss")}</TableHead></TableRow></TableHeader><TableBody>{(networkCheck?.network ?? []).map((value) => <TableRow key={value.carrier}><TableCell>{carrierLabel(language, value.carrier)}</TableCell><TableCell className="text-right tabular-nums">{value.latencyMs.toFixed(1)} ms</TableCell><TableCell className="text-right tabular-nums">{value.jitterMs.toFixed(1)} ms</TableCell><TableCell className="text-right tabular-nums">{value.lossPercent.toFixed(0)}%</TableCell></TableRow>)}{!(networkCheck?.network ?? []).length ? <TableRow><TableCell colSpan={4}>{copy(language, "尚无三网检测结果", "No carrier measurements yet")}</TableCell></TableRow> : null}</TableBody></Table>
            {networkCheck?.error ? <p role="alert" className="text-sm text-destructive">{networkCheck.error}</p> : null}
            {!agent?.capabilities.networkDiagnostics ? <p className="text-xs text-muted-foreground">{copy(language, "需要将 Agent 升级到支持三网检测的版本。", "Upgrade the Agent to a version that supports carrier diagnostics.")}</p> : null}
            <Button variant="outline" disabled={diagnosticsUnavailable || !agent?.capabilities.networkDiagnostics || submitting || networkCheck?.state === "pending" || networkCheck?.state === "running"} onClick={() => void startDiagnostic("node.network-quality")}>{networkCheck?.state === "pending" || networkCheck?.state === "running" ? copy(language, "检测中…", "Checking…") : copy(language, "检测三网质量", "Check carrier quality")}</Button>
          </TabsContent>
          <TabsContent value="route" className="space-y-4 pt-3">
            <p className="text-xs text-muted-foreground">{copy(language, "仅展示节点 → 三网目标的回程方向，不推断或伪造去程。", "Shows only the node-to-carrier return direction; it never infers or fabricates the forward path.")}</p>
            {(routeCheck?.routes ?? []).map((route) => <section key={route.carrier} className="space-y-2"><h3 className="font-medium">{carrierLabel(language, route.carrier)}</h3><Table><TableHeader><TableRow><TableHead className="w-16">TTL</TableHead><TableHead>{copy(language, "地址", "Address")}</TableHead><TableHead className="text-right">{copy(language, "延迟", "Latency")}</TableHead></TableRow></TableHeader><TableBody>{route.hops.map((hop) => <TableRow key={hop.ttl}><TableCell>{hop.ttl}</TableCell><TableCell className="font-mono text-xs">{hop.address || "*"}</TableCell><TableCell className="text-right tabular-nums">{hop.latencyMs === undefined ? "—" : `${hop.latencyMs.toFixed(1)} ms`}</TableCell></TableRow>)}</TableBody></Table></section>)}
            {routeCheck?.error ? <p role="alert" className="text-sm text-destructive">{routeCheck.error}</p> : null}
            {!agent?.capabilities.returnRoute ? <p className="text-xs text-muted-foreground">{copy(language, "回程检测需要升级后的 Linux root Agent。", "Return-route diagnostics require an upgraded Linux root Agent.")}</p> : null}
            <Button variant="outline" disabled={diagnosticsUnavailable || !agent?.capabilities.returnRoute || submitting || routeCheck?.state === "pending" || routeCheck?.state === "running"} onClick={() => void startDiagnostic("node.return-route")}>{routeCheck?.state === "pending" || routeCheck?.state === "running" ? copy(language, "检测中…", "Checking…") : copy(language, "检测三网回程", "Check return routes")}</Button>
          </TabsContent>
          <TabsContent value="bandwidth" className="flex flex-col gap-4 pt-3">
            <p className="text-xs text-muted-foreground">{copy(language, "手动依次测试 Leaseweb 新加坡、洛杉矶和法兰克福公共 iPerf3 节点的下载与上传。每个方向最多 8 MiB，整次最多 48 MiB；公共节点繁忙时结果可能不可用。", "Manually tests download and upload against Leaseweb public iPerf3 endpoints in Singapore, Los Angeles, and Frankfurt. Each direction is capped at 8 MiB (48 MiB total); shared endpoints may be busy.")}</p>
            <Table><TableHeader><TableRow><TableHead>{copy(language, "区域", "Region")}</TableHead><TableHead className="text-right">{copy(language, "下载", "Download")}</TableHead><TableHead className="text-right">{copy(language, "上传", "Upload")}</TableHead></TableRow></TableHeader><TableBody>{["apac", "north-america", "europe"].map((region) => {
              const download = bandwidthCheck?.bandwidth?.find((value) => value.region === region && value.direction === "download");
              const upload = bandwidthCheck?.bandwidth?.find((value) => value.region === region && value.direction === "upload");
              const location = download?.location ?? upload?.location ?? (region === "apac" ? copy(language, "新加坡", "Singapore") : region === "north-america" ? copy(language, "洛杉矶", "Los Angeles") : copy(language, "法兰克福", "Frankfurt"));
              const format = (value: typeof download) => value?.state === "completed" ? `${value.megabitsPerSecond.toFixed(1)} Mbps` : value?.state === "unavailable" ? copy(language, "繁忙", "Busy") : value?.state === "failed" ? copy(language, "失败", "Failed") : "—";
              return <TableRow key={region}><TableCell>{location}</TableCell><TableCell className="text-right tabular-nums">{format(download)}</TableCell><TableCell className="text-right tabular-nums">{format(upload)}</TableCell></TableRow>;
            })}</TableBody></Table>
            {bandwidthCheck?.error ? <p role="alert" className="text-sm text-destructive">{bandwidthCheck.error === "tool_unavailable" ? copy(language, "此 Agent 未安装 iPerf3，安装后重启 Agent 才会开放测速。", "iPerf3 is not installed on this Agent. Install it and restart the Agent to enable testing.") : bandwidthCheck.error}</p> : null}
            {!agent?.capabilities.bandwidthDiagnostics ? <p className="text-xs text-muted-foreground">{copy(language, "需要在 Linux 节点安装 iPerf3 并重启 Agent；Vastora 不会自动修改系统软件包。", "Install iPerf3 on the Linux host and restart the Agent. Vastora does not modify system packages automatically.")}</p> : null}
            <Button variant="outline" disabled={diagnosticsUnavailable || !agent?.capabilities.bandwidthDiagnostics || submitting || bandwidthCheck?.state === "pending" || bandwidthCheck?.state === "running"} onClick={() => void startDiagnostic("node.international-bandwidth")}>{bandwidthCheck?.state === "pending" || bandwidthCheck?.state === "running" ? copy(language, "测速中…", "Testing…") : copy(language, "测试国际带宽", "Test international bandwidth")}</Button>
          </TabsContent>
          <TabsContent value="host" className="flex flex-col gap-6 pt-3">
            <p className="text-xs text-muted-foreground">{copy(language, "由 Vastora Agent 只读采集本机实际生效值；不运行 tcpfit、不应用调优。配置文件存在不代表参数已生效。", "Vastora Agent reads effective host values only. It does not run tcpfit or apply tuning. A config file does not prove values are active.")}</p>
            {hostCheck?.host ? <>
              <section className="space-y-2"><h3 className="font-medium">{copy(language, "硬件与系统", "Hardware & system")}</h3><dl className="grid grid-cols-2 gap-x-6 gap-y-3 border-y py-3 text-sm">{[["vCPU", hostCheck.host.cpuCount.toString()], [copy(language, "内存总量", "Total memory"), humanBytes(hostCheck.host.memoryBytes)], [copy(language, "根文件系统容量", "Root filesystem size"), humanBytes(hostCheck.host.diskBytes)], [copy(language, "内核", "Kernel"), hostCheck.host.kernel], [copy(language, "架构", "Architecture"), hostCheck.host.architecture]].map(([label, value]) => <div key={label}><dt className="text-xs text-muted-foreground">{label}</dt><dd className="mt-1 break-words">{value}</dd></div>)}</dl></section>
              <section className="space-y-2"><h3 className="font-medium">{copy(language, "TCP 实际参数与建议", "Effective TCP values and recommendations")}</h3><Table><TableHeader><TableRow><TableHead>{copy(language, "参数", "Parameter")}</TableHead><TableHead>{copy(language, "当前值", "Current value")}</TableHead><TableHead>{copy(language, "建议值", "Recommended")}</TableHead><TableHead>{copy(language, "依据", "Basis")}</TableHead></TableRow></TableHeader><TableBody>{hostCheck.host.recommendations.map((value) => <TableRow key={value.parameter}><TableCell className="font-mono text-xs">{value.parameter}</TableCell><TableCell className="break-all font-mono text-xs">{value.current || "—"}</TableCell><TableCell className={cn("break-all font-mono text-xs", value.current !== value.value ? "text-amber-400" : "text-muted-foreground")}>{value.value || "—"}</TableCell><TableCell className="text-xs text-muted-foreground">{recommendationReason(language, value.reason)}</TableCell></TableRow>)}</TableBody></Table></section>
              <p className="text-xs text-muted-foreground">{hostCheck.host.persistentConfig ? copy(language, "发现 tcpfit 配置文件；需与当前值对照，不能据此断言已优化。", "Found tcpfit config; compare against effective values before claiming tuning is applied.") : copy(language, "未发现 tcpfit 持久化配置文件。", "No tcpfit persistent config file found.")}</p>
            </> : <p className="py-6 text-center text-sm text-muted-foreground">{copy(language, "尚未采集主机参数", "Host values not collected yet")}</p>}
            {hostCheck?.error ? <p role="alert" className="text-sm text-destructive">{hostCheck.error}</p> : null}
            <Button variant="outline" disabled={!agent?.connected || !agent.capabilities.hostProfile || submitting || hostCheck?.state === "pending" || hostCheck?.state === "running"} onClick={() => void startDiagnostic("node.host-profile")}>{hostCheck?.state === "pending" || hostCheck?.state === "running" ? copy(language, "采集中…", "Collecting…") : copy(language, "采集主机数据", "Collect host values")}</Button>
            {!agent?.capabilities.hostProfile ? <p className="text-xs text-muted-foreground">{copy(language, "需要升级 Linux Agent 才能采集。", "Upgrade the Linux Agent to collect host values.")}</p> : null}
          </TabsContent>
        </Tabs>
      </div>
      {includeIPQuality && (tab === "overview" || tab === "quality") ? <SheetFooter className="border-t">
        {unavailable ? <p className="text-xs text-muted-foreground">{ipQualityError(language, unavailable)}</p> : null}
        <p className="text-xs text-muted-foreground">{copy(language, "使用 IPQuality 访问第三方检测服务，会暴露该节点的出口 IP；不上传在线报告，不测速。首次需下载镜像。", "IPQuality contacts third-party services, revealing this node's exit IP. No online report upload or speed test. The first run downloads an image.")}</p>
        <Button disabled={!!unavailable || active || submitting || state.loading || state.error} onClick={() => void start()}>{report ? copy(language, "重新检测", "Run again") : copy(language, "开始检测", "Run check")}</Button>
      </SheetFooter> : null}
    </SheetContent> : null}
  </Sheet>;
}
