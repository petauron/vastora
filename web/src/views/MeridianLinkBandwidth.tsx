import { useEffect, useState } from "react";
import { api } from "@/api";
import type { LandingView } from "@/landing-types";
import type { NodeDiagnosticCheck } from "@/node-diagnostics-types";
import type { AgentView } from "@/types";
import type { Language } from "@/translations";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { copy } from "@/views/shared";

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
  const results = checks.filter((value) => value.agentId === nodeId && value.kind === "meridian.link-bandwidth");
  const active = results.some((value) => value.state === "pending" || value.state === "running");
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
    {results.length > 0 ? <div className="overflow-x-auto rounded-md border"><table className="w-full text-xs tabular-nums">
      <caption className="sr-only">{copy(language, "各落地机最近一次内置测速结果，单位 Mbps", "Latest built-in test per landing server, in Mbps")}</caption>
      <thead className="bg-muted/40 text-muted-foreground"><tr><th className="px-2 py-1.5 text-left font-medium">{copy(language, "落地机", "Landing")}</th><th className="px-2 py-1.5 text-right font-medium">{copy(language, "入口 → 落地", "Entry → landing")}</th><th className="px-2 py-1.5 text-right font-medium">{copy(language, "落地 → 入口", "Landing → entry")}</th><th className="px-2 py-1.5 text-right font-medium">{copy(language, "检测时间", "Checked")}</th></tr></thead>
      <tbody>{results.map((result) => {
        const name = agents.find((agent) => agent.id === result.landingNodeId)?.name ?? result.landingNodeId;
        const failed = result.state === "failed" || Boolean(result.error);
        const measuring = result.state === "pending" || result.state === "running";
        const valid = !failed && !measuring && result.link;
        return <tr key={result.id} className="border-t"><th className="whitespace-nowrap px-2 py-1.5 text-left font-medium">{name}</th>{valid ? <><td className="whitespace-nowrap px-2 py-1.5 text-right">{valid.uploadMbps.toFixed(1)} Mbps</td><td className="whitespace-nowrap px-2 py-1.5 text-right">{valid.downloadMbps.toFixed(1)} Mbps</td></> : <td colSpan={2} className={`px-2 py-1.5 text-right ${failed ? "text-destructive" : "text-muted-foreground"}`}>{measuring ? copy(language, "测速中…", "Testing…") : copy(language, "测速失败", "Test failed")}</td>}<td className="whitespace-nowrap px-2 py-1.5 text-right text-muted-foreground"><time dateTime={result.checkedAt || result.updatedAt}>{new Date(result.checkedAt || result.updatedAt).toLocaleString(language === "zh-CN" ? "zh-CN" : "en-US", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}</time></td></tr>;
      })}</tbody>
    </table></div> : <p className="text-xs text-muted-foreground">{copy(language, "暂无测速结果", "No bandwidth results yet")}</p>}
    {error ? <p role="alert" className="text-xs text-destructive">{error}</p> : null}
    {!sourceReady ? <p className="text-xs text-muted-foreground">{copy(language, "此入口需要升级 Agent 才能测速。", "Upgrade this entry Agent to test the link.")}</p> : null}
    {landing && servers.length === 0 ? <p className="text-xs text-muted-foreground">{copy(language, "暂无就绪的落地机", "No ready landing server")}</p> : null}
  </div>;
}
