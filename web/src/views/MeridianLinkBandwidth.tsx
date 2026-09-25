import { useEffect, useState } from "react";
import { api } from "../api";
import type { LandingView } from "../landing-types";
import type { NodeDiagnosticCheck } from "../node-diagnostics-types";
import type { AgentView } from "../types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { copy } from "./shared";

export function MeridianLinkBandwidth({ nodeId, language, checks, agents, refresh }: { nodeId: string; language: Language; checks: NodeDiagnosticCheck[]; agents: AgentView[]; refresh: () => Promise<void> }) {
  const [landing, setLanding] = useState<LandingView | null>(null);
  const [landingId, setLandingId] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    void api.landing(controller.signal).then(setLanding).catch(() => { if (!controller.signal.aborted) setError(copy(language, "落地列表读取失败", "Could not load landing servers")); });
    return () => controller.abort();
  }, [language]);
  const sourceReady = agents.some((agent) => agent.id === nodeId && agent.connected && agent.capabilities.meridianLinkBandwidth);
  const servers = landing?.servers.filter((server) => landing.nodeIds.includes(server.nodeId) && server.status === "ready" && server.nodeId !== nodeId && agents.some((agent) => agent.id === server.nodeId && agent.connected && agent.capabilities.meridianLinkBandwidth)) ?? [];
  const check = checks.find((value) => value.agentId === nodeId && value.kind === "meridian.link-bandwidth");
  const current = check?.landingNodeId === landingId ? check : undefined;
  const active = current?.state === "pending" || current?.state === "running";
  const start = async () => {
    if (!sourceReady || !landingId || submitting || active) return;
    setError(""); setSubmitting(true);
    try {
      await api.checkMeridianLinkBandwidth(nodeId, landingId);
      await refresh();
    } catch { setError(copy(language, "测速任务未能创建，请查看节点状态与活动记录。", "Could not queue test; check node status and Activity.")); }
    finally { setSubmitting(false); }
  };
  const selected = servers.find((server) => server.nodeId === landingId);
  return <div className="space-y-2 border-t pt-3">
    <div><h4 className="text-sm font-semibold">{copy(language, "入口 → 落地带宽", "Entry → landing bandwidth")}</h4><p className="text-xs text-muted-foreground">{copy(language, "私网 TCP · 单流 · 每方向 10 秒 · 手动检测；流量随带宽变化", "Private TCP · one stream · 10 s per direction · manual; data use scales with speed")}</p></div>
    <div className="flex flex-wrap items-center gap-2"><Select value={landingId || null} onValueChange={(value) => setLandingId(value ?? "")} items={servers.map((server) => ({ value: server.nodeId, label: server.name }))}><SelectTrigger className="min-w-36 max-w-56" aria-label={copy(language, "选择落地机", "Choose landing server")}><SelectValue placeholder={copy(language, "选择落地机", "Choose landing server")} /></SelectTrigger><SelectContent><SelectGroup>{servers.map((server) => <SelectItem key={server.nodeId} value={server.nodeId}>{server.name}</SelectItem>)}</SelectGroup></SelectContent></Select><Button variant="outline" disabled={!sourceReady || !selected || active || submitting} onClick={() => void start()}>{active ? copy(language, "测速中…", "Testing…") : copy(language, "测试链路", "Test link")}</Button></div>
    {current?.link ? <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm tabular-nums"><span>{copy(language, "入口 → 落地", "Entry → landing")} {current.link.uploadMbps.toFixed(1)} Mbps</span><span>{copy(language, "落地 → 入口", "Landing → entry")} {current.link.downloadMbps.toFixed(1)} Mbps</span>{current.checkedAt ? <time className="text-xs text-muted-foreground" dateTime={current.checkedAt}>{new Date(current.checkedAt).toLocaleString(language === "zh-CN" ? "zh-CN" : "en-US")}</time> : null}</div> : null}
    {current?.state === "failed" || current?.error ? <p role="alert" className="text-xs text-destructive">{copy(language, "链路测速失败，请查看活动记录。", "Link test failed; see Activity.")}</p> : null}
    {error ? <p role="alert" className="text-xs text-destructive">{error}</p> : null}
    {!sourceReady ? <p className="text-xs text-muted-foreground">{copy(language, "此入口需要升级 Agent 才能测速。", "Upgrade this entry Agent to test the link.")}</p> : null}
    {landing && servers.length === 0 ? <p className="text-xs text-muted-foreground">{copy(language, "暂无就绪的落地机", "No ready landing server")}</p> : null}
  </div>;
}
