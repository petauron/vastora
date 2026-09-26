import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { ActivityIcon, ChevronDownIcon, ChevronRightIcon, RefreshCwIcon } from "lucide-react";
import { api, APIError } from "../api";
import type { AgentView } from "../types";
import type { Language } from "../translations";
import type { IPQualityCheck, IPQualityClassification, IPQualityRiskFactor, IPQualityService } from "../ip-quality-types";
import type { Carrier, NodeDiagnosticCheck, NetworkMeasurement } from "../node-diagnostics-types";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Spinner } from "@/components/ui/spinner";
import { cn } from "@/lib/utils";
import { copy } from "./shared";
import { RegionFlag } from "./RegionFlag";
import { routeLine, type RouteTier } from "./returnRouteModel";
import { checkPending, cleanIPQualityValue, ipClassification, ipQualityError, ipQualitySummary, unlockLabel, unlockServiceLabel, unlockServices, unlockTypeLabel } from "./ipQualityModel";
import { AssessmentBadge, AssessmentSummary } from "./IPAssessment";
import { IPQualityComparison } from "./IPQualityComparison";
import { MeridianLinkBandwidth } from "@/app-modules/meridian/LinkBandwidth";

type QualityState = {
  checks: IPQualityCheck[]; diagnostics: NodeDiagnosticCheck[]; agents: AgentView[]; loading: boolean; error: boolean;
  refresh: () => Promise<void>;
};
const QualityContext = createContext<QualityState | null>(null);

export function useIPQuality() { return useContext(QualityContext); }

function carrierLabel(language: Language, carrier: Carrier) {
  return carrier === "telecom" ? copy(language, "中国电信", "China Telecom") : carrier === "unicom" ? copy(language, "中国联通", "China Unicom") : copy(language, "中国移动", "China Mobile");
}

function routeTierLabel(language: Language, tier: RouteTier) {
  return tier === "premium" ? copy(language, "精品", "Premium")
    : tier === "optimized" ? copy(language, "优质", "Optimized")
      : tier === "standard" ? copy(language, "一般", "Standard") : copy(language, "待识别", "Unidentified");
}

function routeTierColor(tier: RouteTier) {
  return tier === "premium" ? "bg-amber-500/15 text-amber-800 ring-amber-500/35 dark:text-amber-300"
    : tier === "optimized" ? "bg-sky-500/15 text-sky-800 ring-sky-500/35 dark:text-sky-300"
      : tier === "standard" ? "bg-slate-500/15 text-slate-700 ring-slate-500/30 dark:text-slate-300" : "bg-muted text-muted-foreground ring-border";
}

function ClassificationBadge({ language, value }: { language: Language; value?: string }) {
  if (!value) return <span className="text-muted-foreground">—</span>;
  const classification = ipClassification(language, value);
  return <span className={cn("inline-flex rounded px-1.5 py-0.5 text-xs font-medium", classification.tone === "good" ? "bg-green-100 text-green-800 dark:bg-green-500/20 dark:text-green-300" : classification.tone === "bad" ? "bg-red-100 text-red-800 dark:bg-red-500/20 dark:text-red-300" : classification.tone === "warning" ? "bg-amber-100 text-amber-800 dark:bg-amber-500/20 dark:text-amber-300" : "bg-muted text-foreground")}>{classification.label}</span>;
}

function IPTypeMatrix({ language, usageTypes, companyTypes }: { language: Language; usageTypes: IPQualityClassification[]; companyTypes: IPQualityClassification[] }) {
  const sources = [...new Set([...usageTypes.map((value) => value.source), ...companyTypes.map((value) => value.source)])];
  if (!sources.length) return <p className="text-xs text-muted-foreground">{copy(language, "暂无分类数据", "No classification data")}</p>;
  const rows = [{ label: copy(language, "使用类型", "Usage type"), values: usageTypes }, { label: copy(language, "公司类型", "Company type"), values: companyTypes }];
  return <Table className="table-fixed text-xs"><TableHeader><TableRow><TableHead className="h-7 w-24 px-1">{copy(language, "数据库", "Source")}</TableHead>{sources.map((source) => <TableHead key={source} className="h-7 px-1 text-center text-[11px]" title={source}>{source}</TableHead>)}</TableRow></TableHeader><TableBody>{rows.map(({ label, values }) => <TableRow key={label}><TableCell className="px-1 py-1.5 text-muted-foreground">{label}</TableCell>{sources.map((source) => <TableCell key={source} className="px-1 py-1.5 text-center"><ClassificationBadge language={language} value={values.find((value) => value.source === source)?.value} /></TableCell>)}</TableRow>)}</TableBody></Table>;
}

function RiskFactorMatrix({ language, factors }: { language: Language; factors: IPQualityRiskFactor[] }) {
  if (!factors.length) return <p className="text-xs text-muted-foreground">{copy(language, "暂无风险因子数据", "No risk-factor data")}</p>;
  const sources = [...new Set(factors.map((factor) => factor.source))];
  const kinds = ["Proxy", "VPN", "Tor", "Server", "Abuser", "Robot"] as const;
  return <Table className="table-fixed text-xs"><TableHeader><TableRow><TableHead className="h-7 w-20 px-1">{copy(language, "项目", "Factor")}</TableHead>{sources.map((source) => <TableHead key={source} className="h-7 px-1 text-center text-[11px]" title={source}>{source}</TableHead>)}</TableRow></TableHeader><TableBody>{kinds.filter((kind) => factors.some((factor) => factor.kind === kind)).map((kind) => <TableRow key={kind}><TableCell className="px-1 py-1 text-muted-foreground">{kind}</TableCell>{sources.map((source) => {
    const factor = factors.find((value) => value.source === source && value.kind === kind);
    return <TableCell key={source} className={cn("px-1 py-1 text-center font-medium", factor?.value ? "text-destructive" : factor ? "text-latency-fast" : "text-muted-foreground")}>{factor ? factor.value ? copy(language, "是", "Yes") : copy(language, "否", "No") : "—"}</TableCell>;
  })}</TableRow>)}</TableBody></Table>;
}

function unlockPresentation(language: Language, service?: IPQualityService, exitRegion?: string) {
  const status = cleanIPQualityValue(service?.status).toLowerCase();
  const region = cleanIPQualityValue(service?.regionCode).toUpperCase();
  const exit = cleanIPQualityValue(exitRegion).toUpperCase();
  const knownRegion = (value: string) => /^[A-Z]{2}$/.test(value) && !["XX", "ZZ", "UN"].includes(value);
  const label = unlockLabel(language, service?.status);
  if (status === "yes") {
    if (!knownRegion(region) || !knownRegion(exit)) return { label, description: copy(language, "已解锁，地区待确认", "Unlocked; region unconfirmed"), mark: "✓?", tone: "text-muted-foreground" };
    if (region !== exit) return { label, description: copy(language, `已解锁 ${region}，与出口 IP 地区 ${exit} 不一致`, `Unlocked in ${region}; differs from exit IP region ${exit}`), mark: region, tone: "text-latency-medium" };
    return { label, description: copy(language, `已解锁 ${region}，与出口 IP 地区一致`, `Unlocked in ${region}; matches exit IP region`), mark: "✓", tone: "text-latency-fast" };
  }
  const blocked = status === "no" || status === "block";
  const mark = blocked ? "×" : ["org", "originals only", "nf.only", "apponly", "webonly", "china", "noprem.", "pending", "idc"].includes(status) ? label : ["failed", "fail", "error"].includes(status) ? "!" : "?";
  return { label, description: label, mark, tone: blocked ? "text-destructive" : "text-muted-foreground" };
}

function UnlockMatrix({ language, services, exitRegion }: { language: Language; services: IPQualityService[]; exitRegion?: string }) {
  if (!services.length) return <p className="text-xs text-muted-foreground">{copy(language, "暂无解锁结果", "No availability results")}</p>;
  return <Table className="table-fixed text-xs"><TableHeader><TableRow><TableHead className="h-7 w-20 px-1">{copy(language, "服务", "Service")}</TableHead>{services.map((service) => <TableHead key={service.name} className="h-7 px-1 text-center text-[11px]" title={service.name}>{unlockServiceLabel(service.name)}</TableHead>)}</TableRow></TableHeader><TableBody>
    <TableRow><TableCell className="px-1 py-1 text-muted-foreground">{copy(language, "结果", "Status")}</TableCell>{services.map((service) => {
      const result = unlockPresentation(language, service, exitRegion);
      return <TableCell key={service.name} title={result.description} aria-label={result.description} className={cn("px-1 py-1 text-center font-medium", result.tone)}>{result.label}</TableCell>;
    })}</TableRow>
    <TableRow><TableCell className="px-1 py-1 text-muted-foreground">{copy(language, "地区", "Region")}</TableCell>{services.map((service) => <TableCell key={service.name} className="px-1 py-1 text-center"><span className="inline-flex items-center gap-1"><RegionFlag code={service.regionCode} language={language} />{service.regionCode ?? "—"}</span></TableCell>)}</TableRow>
    <TableRow><TableCell className="px-1 py-1 text-muted-foreground">{copy(language, "方式", "Type")}</TableCell>{services.map((service) => <TableCell key={service.name} className="px-1 py-1 text-center">{unlockTypeLabel(language, service.type) || "—"}</TableCell>)}</TableRow>
  </TableBody></Table>;
}

function UnlockIndicators({ check, language, current }: { check?: IPQualityCheck; language: Language; current: boolean }) {
  return <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">{unlockServices.map((name) => {
    const service = current ? check?.report?.services.find((item) => item.name === name) : undefined;
    const result = unlockPresentation(language, service, check?.report?.regionCode);
    const description = current ? result.description : copy(language, "待检测", "Check needed");
    return <span aria-label={`${unlockServiceLabel(name)} ${description}`} className={cn("inline-flex shrink-0 items-center gap-0.5 whitespace-nowrap text-[11px] leading-5", result.tone)} key={name} title={`${unlockServiceLabel(name)} · ${description}`}><span>{unlockServiceLabel(name, true)}</span><span aria-hidden="true" className="font-semibold">{current ? result.mark : "?"}</span></span>;
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
  useEffect(() => {
    if (!enabled || !includeIPQuality) return;
    const timer = setInterval(() => { if (!document.hidden && !pending.current) void refresh(); }, 60000);
    const visible = () => { if (!document.hidden && !pending.current) void refresh(); };
    document.addEventListener("visibilitychange", visible);
    return () => { clearInterval(timer); document.removeEventListener("visibilitychange", visible); };
  }, [enabled, includeIPQuality, refresh]);
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

type DiagnosticsButtonProps = { nodeId: string; name: string; language: Language; compact?: boolean; linkBandwidth?: boolean };

export function IPQualityButton(props: DiagnosticsButtonProps) {
  return <DiagnosticsButton {...props} includeIPQuality />;
}

export function NodeDiagnosticsButton(props: DiagnosticsButtonProps) {
  return <DiagnosticsButton {...props} includeIPQuality={false} />;
}

function DiagnosticsButton({ nodeId, name, language, compact = false, linkBandwidth = false, includeIPQuality }: DiagnosticsButtonProps & { includeIPQuality: boolean }) {
  const state = useContext(QualityContext);
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState(includeIPQuality ? "quality" : "network");
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
  const currentUnlocks = Boolean(!state.error && check?.state === "succeeded" && check.report && !check.stale && !check.error && check.assessment?.status !== "expired" && check.assessment?.status !== "ip_changed");
  const broadcast = currentUnlocks && report?.ippure?.status === "ok" ? report.ippure.broadcast : undefined;
  const ipOrigin = broadcast === true ? copy(language, "广播 IP", "Broadcast IP") : broadcast === false ? copy(language, "原生 IP", "Native IP") : copy(language, "未知", "Unknown");
  return <Sheet open={open} onOpenChange={(value) => { setOpen(value); if (value) { setTab(includeIPQuality ? "quality" : "network"); setError(""); void state.refresh(); } }}>
    <SheetTrigger render={<Button type="button" variant="ghost" size={compact ? "icon-sm" : "sm"} className={compact ? "shrink-0" : "quality-list-trigger h-auto min-h-11 w-full max-w-full justify-start gap-2 px-0 py-1 text-left text-xs text-muted-foreground"} />} aria-label={compact ? copy(language, `查看 ${name} 的节点诊断`, `View node diagnostics for ${name}`) : copy(language, `查看 ${name} 的节点诊断：${summary}`, `View node diagnostics for ${name}: ${summary}`)} title={compact ? copy(language, "查看节点诊断", "View node diagnostics") : summary}>
      {compact ? <ActivityIcon aria-hidden="true" /> : <><span className="shrink-0"><AssessmentBadge language={language} assessment={!state.error ? check?.assessment : undefined} /></span><UnlockIndicators check={check} language={language} current={currentUnlocks} /><ChevronRightIcon className="ml-auto shrink-0" aria-hidden="true" /></>}
    </SheetTrigger>
    {open ? <SheetContent className="data-[side=right]:w-full data-[side=right]:sm:max-w-6xl">
      <SheetHeader className="pr-12">
        <SheetTitle>{name} · {copy(language, "节点诊断", "Node diagnostics")}</SheetTitle>
        <SheetDescription>{includeIPQuality ? copy(language, "检测此代理入口或落地机自身的公网出口，不代表入口 → 落地组合的链路实测。", "Checks this proxy entry or landing host's own public exit, not an entry-to-landing route.") : copy(language, "查看这台基础设施节点的主机参数与按需网络诊断；不包含代理出口的 IP 质量或解锁结果。", "Shows host values and on-demand network diagnostics for this infrastructure node; proxy-exit IP quality and unlock results are excluded.")}</SheetDescription>
      </SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto px-4 pb-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 text-xs text-muted-foreground">
            {includeIPQuality ? <p className="break-words">{copy(language, "当前出口", "Current exit")}: {agent?.publicEgress?.address ?? "—"}{check?.checkedAt ? <> · {copy(language, "最近结果", "Last result")}: {new Date(check.checkedAt).toLocaleString(language)}</> : null}</p> : <p>{copy(language, "节点诊断按需执行，不会在后台持续探测。", "Node diagnostics run on demand and do not probe continuously in the background.")}</p>}
          </div>
          <Button size="icon-sm" variant="ghost" disabled={state.loading} onClick={() => void state.refresh()} aria-label={copy(language, "刷新检测状态", "Refresh check status")}><RefreshCwIcon aria-hidden="true" /></Button>
        </div>
        {(includeIPQuality && active) || submitting ? <p role="status" className="flex items-center gap-2 text-sm"><Spinner aria-hidden="true" />{submitting ? copy(language, "正在提交检测…", "Submitting check…") : check?.state === "pending" ? copy(language, "等待节点执行，可关闭此面板。", "Waiting for the node. You can close this panel.") : copy(language, "正在检测，可关闭此面板。", "Checking. You can close this panel.")}</p> : null}
        {state.error || error || includeIPQuality && check?.error ? <p role="alert" className="text-sm text-destructive">{ipQualityError(language, state.error ? "read_failed" : error || check!.error!)}</p> : null}
        {includeIPQuality && check?.stale ? <p role="status" className="text-sm text-destructive">{copy(language, "出口 IP 已变化。以下为旧 IP 的结果，请重新检测。", "The exit IP changed. The results below belong to the previous IP; run a new check.")}</p> : null}
        <Tabs value={tab} onValueChange={setTab} className="min-w-0">
          <TabsList className={cn("grid h-auto w-full", includeIPQuality ? "grid-cols-2" : "grid-cols-1")}>
            {includeIPQuality ? <TabsTrigger value="quality">IP</TabsTrigger> : null}
            <TabsTrigger value="network">{copy(language, "网络", "Network")}</TabsTrigger>
          </TabsList>
          {includeIPQuality ? <TabsContent value="quality" className="pt-3">{report ? <div className="space-y-3">
            <AssessmentSummary language={language} assessment={check?.assessment} report={report} checkedAt={check?.checkedAt} />
            <IPQualityComparison language={language} nodeId={nodeId} />
            <section className="rounded-lg border px-4 py-3" aria-label={copy(language, "基础信息", "Basic information")}>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs"><RegionFlag code={report.regionCode} language={language} /><span className="font-mono font-medium">{report.address}</span><span className="text-muted-foreground">IPQuality {report.version}</span></div>
              <dl className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs sm:grid-cols-3">
                {[[copy(language, "ASN", "ASN"), report.asn], [copy(language, "组织 / ISP", "Organization / ISP"), report.organization], [copy(language, "地区", "Region"), report.regionName || report.regionCode], [copy(language, "注册地区", "Registered region"), report.registeredRegion || report.registeredCode], [copy(language, "IP 归属（IPPure）", "IP origin (IPPure)"), ipOrigin], [copy(language, "城市", "City"), report.city], [copy(language, "时区", "Time zone"), report.timeZone]].map(([label, value]) => <div key={label} className="min-w-0"><dt className="text-muted-foreground">{label}</dt><dd className="truncate font-medium" title={value || "—"}>{value || "—"}</dd></div>)}
              </dl>
            </section>
            <section className="border-t pt-2" aria-label={copy(language, "IP 类型属性", "IP type classifications")}>
              <h3 className="mb-1 text-xs font-semibold text-latency-fast">{copy(language, "一 · IP 类型", "1 · IP type")}</h3>
              <IPTypeMatrix language={language} usageTypes={report.usageTypes ?? []} companyTypes={report.companyTypes ?? []} />
            </section>
            <section className="border-t pt-2" aria-label={copy(language, "来源评分", "Provider scores")}>
              <h3 className="mb-1 text-xs font-semibold text-latency-fast">{copy(language, "二 · 来源评分", "2 · Provider scores")}</h3>
              <div className="grid gap-x-4 gap-y-1 sm:grid-cols-2">{report.scores.map((value) => {
                const risk = ["SCAMALYTICS", "IPQS", "AbuseIPDB"].includes(value.source) && /^\d{1,3}(?:\.\d+)?$/.test(value.value) ? Number(value.value) : NaN;
                const hasKnownScale = Number.isFinite(risk) && risk >= 0 && risk <= 100;
                return <div key={value.source} className="grid grid-cols-[100px_minmax(0,1fr)_56px] items-center gap-2 text-xs"><span className="truncate text-muted-foreground" title={value.source}>{value.source}</span><span className="h-1.5 rounded-full bg-muted">{hasKnownScale ? <span className="block h-full rounded-full bg-primary" style={{ width: `${risk}%` }} /> : null}</span><span className="text-right font-medium tabular-nums">{value.value}</span></div>;
              })}{!report.scores.length ? <p className="text-xs text-muted-foreground">{copy(language, "暂无评分数据", "No score data")}</p> : null}</div>
            </section>
            <section className="border-t pt-2" aria-label={copy(language, "风险因子", "Risk factors")}>
              <h3 className="mb-1 text-xs font-semibold text-latency-fast">{copy(language, "三 · 风险因子", "3 · Risk factors")}</h3>
              <RiskFactorMatrix language={language} factors={report.riskFactors ?? []} />
            </section>
            <section className="border-t pt-2" aria-label={copy(language, "流媒体与 AI 解锁", "Streaming and AI availability")}>
              <h3 className="mb-1 text-xs font-semibold text-latency-fast">{copy(language, "四 · 流媒体与 AI 解锁", "4 · Streaming and AI availability")}</h3>
              <UnlockMatrix language={language} services={report.services} exitRegion={report.regionCode} />
            </section>
          </div> : <p className="py-6 text-center text-sm text-muted-foreground">{copy(language, "尚无 IP 质量结果", "No IP-quality report yet")}</p>}</TabsContent> : null}
          <TabsContent value="network" className="pt-3">
            <div className="grid items-start gap-3 lg:grid-cols-2">
              <section className="space-y-3 rounded-lg border p-3 sm:p-4" aria-label={copy(language, "三网质量", "Carrier quality")}>
                <div><h3 className="text-sm font-semibold">{copy(language, "三网质量", "Carrier quality")}</h3><p className="text-xs text-muted-foreground">{copy(language, "4 次 TCP 连接采样", "Four TCP connection samples")}</p></div>
            <Table><TableHeader><TableRow><TableHead>{copy(language, "目标", "Target")}</TableHead><TableHead className="text-right">{copy(language, "延迟", "Latency")}</TableHead><TableHead className="text-right">{copy(language, "抖动", "Jitter")}</TableHead><TableHead className="text-right">{copy(language, "丢失", "Loss")}</TableHead></TableRow></TableHeader><TableBody>{(networkCheck?.network ?? []).map((value) => <TableRow key={value.carrier}><TableCell>{carrierLabel(language, value.carrier)}</TableCell><TableCell className="text-right tabular-nums">{value.latencyMs.toFixed(1)} ms</TableCell><TableCell className="text-right tabular-nums">{value.jitterMs.toFixed(1)} ms</TableCell><TableCell className="text-right tabular-nums">{value.lossPercent.toFixed(0)}%</TableCell></TableRow>)}{!(networkCheck?.network ?? []).length ? <TableRow><TableCell colSpan={4}>{copy(language, "尚无三网检测结果", "No carrier measurements yet")}</TableCell></TableRow> : null}</TableBody></Table>
            {networkCheck?.error ? <p role="alert" className="text-sm text-destructive">{networkCheck.error}</p> : null}
            {!agent?.capabilities.networkDiagnostics ? <p className="text-xs text-muted-foreground">{copy(language, "需要将 Agent 升级到支持三网检测的版本。", "Upgrade the Agent to a version that supports carrier diagnostics.")}</p> : null}
            <Button variant="outline" disabled={diagnosticsUnavailable || !agent?.capabilities.networkDiagnostics || submitting || networkCheck?.state === "pending" || networkCheck?.state === "running"} onClick={() => void startDiagnostic("node.network-quality")}>{networkCheck?.state === "pending" || networkCheck?.state === "running" ? copy(language, "检测中…", "Checking…") : copy(language, "检测三网质量", "Check carrier quality")}</Button>
              </section>
              <section className="space-y-3 rounded-lg border p-3 sm:p-4" aria-label={copy(language, "三网回程", "Return routes")}>
                <h3 className="text-sm font-semibold">{copy(language, "三网回程", "Return routes")}</h3>
            <div className="space-y-2">{(routeCheck?.routes ?? []).map((route) => {
              const line = routeLine(route);
              return <details key={route.carrier} className="group rounded-lg border">
                <summary className="flex min-h-11 cursor-pointer list-none items-center justify-between gap-3 px-3 py-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring [&::-webkit-details-marker]:hidden">
                  <span className="font-medium">{carrierLabel(language, route.carrier)}</span>
                  <span className="ml-auto flex items-center gap-2">
                    <span className={cn("rounded-md px-2.5 py-1 text-sm font-semibold ring-1 ring-inset", routeTierColor(line.tier))}>{line.name || routeTierLabel(language, "unknown")}</span>
                    {line.tier !== "unknown" ? <span className="text-xs text-muted-foreground">{routeTierLabel(language, line.tier)}</span> : null}
                    <ChevronDownIcon className="size-4 text-muted-foreground transition-transform group-open:rotate-180" aria-hidden="true" />
                  </span>
                </summary>
                <div className="border-t px-3 pb-2">
                  <Table><TableHeader><TableRow><TableHead className="w-16">TTL</TableHead><TableHead>{copy(language, "地址", "Address")}</TableHead><TableHead className="text-right">{copy(language, "延迟", "Latency")}</TableHead></TableRow></TableHeader><TableBody>{route.hops.map((hop) => <TableRow key={hop.ttl}><TableCell>{hop.ttl}</TableCell><TableCell className="font-mono text-xs">{hop.address || "*"}</TableCell><TableCell className="text-right tabular-nums">{hop.latencyMs === undefined ? "—" : `${hop.latencyMs.toFixed(1)} ms`}</TableCell></TableRow>)}</TableBody></Table>
                </div>
              </details>;
            })}{!(routeCheck?.routes ?? []).length ? <p className="py-4 text-center text-sm text-muted-foreground">{copy(language, "尚无回程检测结果", "No return-route result yet")}</p> : null}</div>
            {routeCheck?.error ? <p role="alert" className="text-sm text-destructive">{routeCheck.error}</p> : null}
            {!agent?.capabilities.returnRoute ? <p className="text-xs text-muted-foreground">{copy(language, "回程检测需要升级后的 Linux root Agent。", "Return-route diagnostics require an upgraded Linux root Agent.")}</p> : null}
            <Button variant="outline" disabled={diagnosticsUnavailable || !agent?.capabilities.returnRoute || submitting || routeCheck?.state === "pending" || routeCheck?.state === "running"} onClick={() => void startDiagnostic("node.return-route")}>{routeCheck?.state === "pending" || routeCheck?.state === "running" ? copy(language, "检测中…", "Checking…") : copy(language, "检测三网回程", "Check return routes")}</Button>
              </section>
              <section className="space-y-3 rounded-lg border p-3 sm:p-4" aria-label={copy(language, "国际带宽", "International bandwidth")}>
                <div><h3 className="text-sm font-semibold">{copy(language, "国际带宽", "International bandwidth")}</h3><p className="text-xs text-muted-foreground">{copy(language, "Leaseweb 公共节点 · 手动测速 · 总量最多 48 MiB", "Leaseweb public endpoints · manual test · 48 MiB total cap")}</p></div>
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
            {linkBandwidth ? <MeridianLinkBandwidth nodeId={nodeId} language={language} checks={state.diagnostics} agents={state.agents} refresh={state.refresh} /> : null}
              </section>
              <section className="space-y-3 rounded-lg border p-3 sm:p-4" aria-label={copy(language, "主机与 TCP", "Host & TCP")}>
                <div><h3 className="text-sm font-semibold">{copy(language, "主机与 TCP", "Host & TCP")}</h3><p className="text-xs text-muted-foreground">{copy(language, "只读采集实际生效值", "Read-only effective values")}</p></div>
            {hostCheck?.host ? <>
              <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs sm:grid-cols-3">{[["vCPU", hostCheck.host.cpuCount.toString()], [copy(language, "内存", "Memory"), humanBytes(hostCheck.host.memoryBytes)], [copy(language, "磁盘", "Disk"), humanBytes(hostCheck.host.diskBytes)], [copy(language, "内核", "Kernel"), hostCheck.host.kernel], [copy(language, "架构", "Architecture"), hostCheck.host.architecture], ["TCP", `${hostCheck.host.congestionControl || "—"} / ${hostCheck.host.defaultQdisc || "—"}`]].map(([label, value]) => <div key={label} className="min-w-0"><dt className="text-muted-foreground">{label}</dt><dd className="truncate font-medium" title={value}>{value}</dd></div>)}</dl>
              <details className="group rounded-md border"><summary className="flex min-h-11 cursor-pointer list-none items-center justify-between gap-2 px-3 text-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring [&::-webkit-details-marker]:hidden">{copy(language, "TCP 参数与建议", "TCP values and recommendations")}<ChevronDownIcon className="size-4 text-muted-foreground transition-transform group-open:rotate-180" aria-hidden="true" /></summary><div className="space-y-2 border-t p-3"><div className="overflow-x-auto"><Table><TableHeader><TableRow><TableHead>{copy(language, "参数", "Parameter")}</TableHead><TableHead>{copy(language, "当前值", "Current value")}</TableHead><TableHead>{copy(language, "建议值", "Recommended")}</TableHead><TableHead>{copy(language, "依据", "Basis")}</TableHead></TableRow></TableHeader><TableBody>{hostCheck.host.recommendations.map((value) => <TableRow key={value.parameter}><TableCell className="font-mono text-xs">{value.parameter}</TableCell><TableCell className="break-all font-mono text-xs">{value.current || "—"}</TableCell><TableCell className={cn("break-all font-mono text-xs", value.current !== value.value ? "text-amber-400" : "text-muted-foreground")}>{value.value || "—"}</TableCell><TableCell className="text-xs text-muted-foreground">{recommendationReason(language, value.reason)}</TableCell></TableRow>)}</TableBody></Table></div><p className="text-xs text-muted-foreground">{hostCheck.host.persistentConfig ? copy(language, "发现 tcpfit 配置文件；需与当前值对照，不能据此断言已优化。", "Found tcpfit config; compare against effective values before claiming tuning is applied.") : copy(language, "未发现 tcpfit 持久化配置文件。", "No tcpfit persistent config file found.")}</p></div></details>
            </> : <p className="py-6 text-center text-sm text-muted-foreground">{copy(language, "尚未采集主机参数", "Host values not collected yet")}</p>}
            {hostCheck?.error ? <p role="alert" className="text-sm text-destructive">{hostCheck.error}</p> : null}
            <Button variant="outline" disabled={!agent?.connected || !agent.capabilities.hostProfile || submitting || hostCheck?.state === "pending" || hostCheck?.state === "running"} onClick={() => void startDiagnostic("node.host-profile")}>{hostCheck?.state === "pending" || hostCheck?.state === "running" ? copy(language, "采集中…", "Collecting…") : copy(language, "采集主机数据", "Collect host values")}</Button>
            {!agent?.capabilities.hostProfile ? <p className="text-xs text-muted-foreground">{copy(language, "需要升级 Linux Agent 才能采集。", "Upgrade the Linux Agent to collect host values.")}</p> : null}
              </section>
            </div>
          </TabsContent>
        </Tabs>
      </div>
      {includeIPQuality && tab === "quality" ? <SheetFooter className="border-t">
        {unavailable ? <p className="text-xs text-muted-foreground">{ipQualityError(language, unavailable)}</p> : null}
        <p className="text-xs text-muted-foreground">{copy(language, "使用 IPQuality 访问第三方检测服务，会暴露该节点的出口 IP；不上传在线报告，不测速。首次需下载镜像。", "IPQuality contacts third-party services, revealing this node's exit IP. No online report upload or speed test. The first run downloads an image.")}</p>
        <Button disabled={!!unavailable || active || submitting || state.loading || state.error} onClick={() => void start()}>{report ? copy(language, "重新检测", "Run again") : copy(language, "开始检测", "Run check")}</Button>
      </SheetFooter> : null}
    </SheetContent> : null}
  </Sheet>;
}
