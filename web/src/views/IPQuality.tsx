import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { ActivityIcon, ChevronRightIcon, RefreshCwIcon } from "lucide-react";
import { api, APIError } from "../api";
import type { AgentView } from "../types";
import type { Language } from "../translations";
import type { IPQualityCheck } from "../ip-quality-types";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Spinner } from "@/components/ui/spinner";
import { cn } from "@/lib/utils";
import { copy } from "./shared";
import { RegionFlag } from "./RegionFlag";
import { checkPending, ipQualityError, ipQualitySummary, unlockLabel } from "./ipQualityModel";

type QualityState = {
  checks: IPQualityCheck[]; agents: AgentView[]; loading: boolean; error: boolean;
  refresh: () => Promise<void>;
};
const QualityContext = createContext<QualityState | null>(null);

// One read per page, then poll only while an explicitly requested check exists.
// A list refresh never starts a diagnostic on any node.
export function IPQualityProvider({ agents, enabled, children }: { agents: AgentView[]; enabled: boolean; children: ReactNode }) {
  const [checks, setChecks] = useState<IPQualityCheck[]>([]);
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
      const result = await api.ipQuality(request.signal);
      if (!request.signal.aborted) { setChecks(result.checks); setError(false); }
    } catch {
      if (pending.current === request) setError(true);
    } finally {
      clearTimeout(timeout);
      if (pending.current === request) { setLoading(false); pending.current = null; }
    }
  }, []);
  useEffect(() => {
    if (enabled) void refresh();
    return () => { const request = pending.current; pending.current = null; request?.abort(); };
  }, [enabled, refresh]);
  const active = checks.some(checkPending);
  useEffect(() => {
    if (!enabled || !active || error || loading) return;
    const timer = setTimeout(() => { if (!document.hidden) void refresh(); }, 4000);
    const visible = () => { if (!document.hidden) void refresh(); };
    document.addEventListener("visibilitychange", visible);
    return () => { clearTimeout(timer); document.removeEventListener("visibilitychange", visible); };
  }, [enabled, active, error, loading, refresh]);
  return <QualityContext.Provider value={{ checks, agents, loading, error, refresh }}>{children}</QualityContext.Provider>;
}

export function IPQualityButton({ nodeId, name, language, compact = false }: { nodeId: string; name: string; language: Language; compact?: boolean }) {
  const state = useContext(QualityContext);
  const [open, setOpen] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const pending = useRef<AbortController | null>(null);
  useEffect(() => () => { const request = pending.current; pending.current = null; request?.abort(); }, []);
  if (!state) return null;
  const agent = state.agents.find((value) => value.id === nodeId);
  const check = state.checks.find((value) => value.agentId === nodeId);
  const report = check?.report;
  const active = checkPending(check);
  const unavailable = !agent || agent.status !== "active" || agent.credentialRevoked ? "ip_quality_node_unavailable"
    : !agent.connected ? "ip_quality_node_offline"
    : !agent.capabilities.ipQuality || !agent.capabilities.docker ? "ip_quality_agent_upgrade_required"
    : !agent.publicEgress?.address ? "ip_quality_address_unavailable" : "";
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
  const summary = state.error ? copy(language, "IP 质量 · 读取失败", "IP quality · Unavailable") : ipQualitySummary(language, check);
  const score = !check?.stale && !check?.error && !active ? report?.scores.find((value) => value.source === "IPQS") : undefined;
  return <Sheet open={open} onOpenChange={(value) => { setOpen(value); if (value) { setError(""); void state.refresh(); } }}>
    <SheetTrigger render={<Button type="button" variant="ghost" size={compact ? "icon-sm" : "sm"} className={compact ? "shrink-0" : "h-auto min-h-8 max-w-full justify-start px-1 py-1 text-left text-xs text-muted-foreground"} />} aria-label={copy(language, `查看 ${name} 的 IP 质量`, `View IP quality for ${name}`)} title={compact ? copy(language, "查看 IP 质量", "View IP quality") : undefined}>
      {compact ? <ActivityIcon aria-hidden="true" /> : <><span className="min-w-0 whitespace-normal">{summary}{score ? ` · IPQS ${score.value}` : ""}</span><ChevronRightIcon className="shrink-0" aria-hidden="true" /></>}
    </SheetTrigger>
    {open ? <SheetContent className="data-[side=right]:w-full data-[side=right]:sm:max-w-lg">
      <SheetHeader className="pr-12">
        <SheetTitle>{name} · {copy(language, "IP 质量", "IP quality")}</SheetTitle>
        <SheetDescription>{copy(language, "检测此机器自身的公网出口，不代表入口 → 落地组合的链路实测。", "Checks this host's own public exit, not an entry-to-landing route.")}</SheetDescription>
      </SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto px-4 pb-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 text-xs text-muted-foreground">
            <p className="break-words">{copy(language, "当前出口", "Current exit")}: {agent?.publicEgress?.address ?? "—"}</p>
            {check?.checkedAt ? <p className="mt-1">{copy(language, "最近结果", "Last result")}: {new Date(check.checkedAt).toLocaleString(language)}</p> : null}
          </div>
          <Button size="icon-sm" variant="ghost" disabled={state.loading} onClick={() => void state.refresh()} aria-label={copy(language, "刷新检测状态", "Refresh check status")}><RefreshCwIcon aria-hidden="true" /></Button>
        </div>
        {active || submitting ? <p role="status" className="flex items-center gap-2 text-sm"><Spinner aria-hidden="true" />{submitting ? copy(language, "正在提交检测…", "Submitting check…") : check?.state === "pending" ? copy(language, "等待节点执行，可关闭此面板。", "Waiting for the node. You can close this panel.") : copy(language, "正在检测，可关闭此面板。", "Checking. You can close this panel.")}</p> : null}
        {state.error || error || check?.error ? <p role="alert" className="text-sm text-destructive">{ipQualityError(language, state.error ? "read_failed" : error || check!.error!)}</p> : null}
        {check?.stale ? <p role="status" className="text-sm text-destructive">{copy(language, "出口 IP 已变化。以下为旧 IP 的结果，请重新检测。", "The exit IP changed. The results below belong to the previous IP; run a new check.")}</p> : null}
        {report ? <>
          <p className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground"><RegionFlag code={report.regionCode} language={language} /><span className="break-words">{copy(language, "检测出口", "Checked exit")}: {report.address}</span><span>IPQuality {report.version}</span></p>
          <section className="space-y-2" aria-label={copy(language, "流媒体与 AI 解锁", "Streaming and AI availability")}>
            <h3 className="font-medium">{copy(language, "流媒体与 AI 解锁", "Streaming and AI availability")}</h3>
            <Table><TableHeader><TableRow><TableHead>{copy(language, "服务", "Service")}</TableHead><TableHead>{copy(language, "结果", "Result")}</TableHead><TableHead>{copy(language, "地区", "Region")}</TableHead></TableRow></TableHeader>
              <TableBody>{report.services.map((service) => <TableRow key={service.name}>
                <TableCell className="whitespace-normal">{service.name === "AmazonPrimeVideo" ? "Prime Video" : service.name === "DisneyPlus" ? "Disney+" : service.name}</TableCell>
                <TableCell className={cn("whitespace-normal", service.status.toLowerCase() === "yes" ? "text-latency-fast" : service.status.toLowerCase() === "no" ? "text-destructive" : "text-muted-foreground")}>{unlockLabel(language, service.status)}{service.type && service.type !== "null" ? <span className="block text-xs text-muted-foreground">{service.type === "Native" ? copy(language, "原生", "Native") : service.type}</span> : null}</TableCell>
                <TableCell><span className="inline-flex gap-1"><RegionFlag code={service.regionCode} language={language} />{service.regionCode ?? "—"}</span></TableCell>
              </TableRow>)}{!report.services.length ? <TableRow><TableCell colSpan={3}>{copy(language, "暂无解锁结果", "No availability results")}</TableCell></TableRow> : null}</TableBody>
            </Table>
          </section>
          <section className="space-y-2" aria-label={copy(language, "风险评分", "Risk scores")}>
            <h3 className="font-medium">{copy(language, "风险评分", "Risk scores")}</h3>
            <p className="text-xs text-muted-foreground">{copy(language, "各来源口径不同，保留原始分值，不合成为总分；缺失不代表零风险。", "Providers use different scales. Values are not averaged; missing data does not mean zero risk.")}</p>
            <Table><TableHeader><TableRow><TableHead>{copy(language, "来源", "Provider")}</TableHead><TableHead className="text-right">{copy(language, "原始分值", "Reported value")}</TableHead></TableRow></TableHeader><TableBody>
              {report.scores.map((value) => <TableRow key={value.source}><TableCell>{value.source}</TableCell><TableCell className="text-right tabular-nums">{value.value}</TableCell></TableRow>)}
              {!report.scores.length ? <TableRow><TableCell colSpan={2}>{copy(language, "暂无评分数据", "No score data")}</TableCell></TableRow> : null}
            </TableBody>
            </Table>
          </section>
        </> : !active && !submitting ? <p className="py-6 text-center text-sm text-muted-foreground">{copy(language, "尚无检测结果，手动检测后会保留最近一次结果。", "No report yet. Run a check to save the latest result.")}</p> : null}
      </div>
      <SheetFooter className="border-t">
        {unavailable ? <p className="text-xs text-muted-foreground">{ipQualityError(language, unavailable)}</p> : null}
        <p className="text-xs text-muted-foreground">{copy(language, "使用 IPQuality 访问第三方检测服务，会暴露该节点的出口 IP；不上传在线报告，不测速。首次需下载镜像。", "IPQuality contacts third-party services, revealing this node's exit IP. No online report upload or speed test. The first run downloads an image.")}</p>
        <Button disabled={!!unavailable || active || submitting || state.loading || state.error} onClick={() => void start()}>{report ? copy(language, "重新检测", "Run again") : copy(language, "开始检测", "Run check")}</Button>
      </SheetFooter>
    </SheetContent> : null}
  </Sheet>;
}
