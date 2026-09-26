import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { Language } from "../translations";
import type { InstalledAppInstance } from "./installed-apps-model";
import { useLanding } from "./LandingControls";
import { NodeDiagnosticsButton, useIPQuality } from "./IPQuality";
import { LinkBandwidthSummary } from "./LinkBandwidthSummary";
import { RegionFlag } from "./RegionFlag";
import { landingLatencyColor, selectedLandingLatencies } from "./landingLatency";
import { copy } from "./shared";

export function MeridianNetworkMatrix({ instances, language }: { instances: InstalledAppInstance[]; language: Language }) {
  const landing = useLanding();
  const quality = useIPQuality();
  if (!landing?.view || landing.failed) return <p role="status" className="py-8 text-sm text-muted-foreground">{copy(language, landing?.failed ? "落地列表读取失败" : "正在读取落地机…", landing?.failed ? "Unable to load landing nodes" : "Loading landing nodes…")}</p>;
  const view = landing.view;
  const servers = view.servers.filter((server) => view.nodeIds.includes(server.nodeId));
  if (!servers.length) return <p className="py-8 text-sm text-muted-foreground">{copy(language, "尚未添加落地机", "No landing nodes configured")}</p>;
  const checks = new Map(quality?.diagnostics.filter((check) => check.kind === "meridian.link-bandwidth").map((check) => [`${check.agentId}:${check.landingNodeId}`, check]));
  return <div className="flex min-w-0 flex-col gap-2">
    <p className="text-xs text-muted-foreground">{copy(language, "Mbps · ↑入口到落地 / ↓落地到入口 · 每方向 10 秒", "Mbps · ↑entry to landing / ↓landing to entry · 10 s per direction")}</p>
    <Table className="meridian-network-matrix" aria-label={copy(language, "线路测速矩阵", "Link bandwidth matrix")}>
      <TableHeader><TableRow><TableHead className="sticky left-0 z-10 min-w-44 bg-background">{copy(language, "线路机", "Entry node")}</TableHead>{servers.map((server) => <TableHead key={server.nodeId} className="min-w-44 text-center"><span className="inline-flex items-center gap-2"><RegionFlag code={landing.regions[server.nodeId]} language={language} />{server.name}</span></TableHead>)}<TableHead><span className="sr-only">{copy(language, "检测", "Diagnostics")}</span></TableHead></TableRow></TableHeader>
      <TableBody>{instances.map((instance) => {
        const nodeId = instance.application.nodeId;
        const name = instance.agent?.name ?? nodeId;
        const pairs = selectedLandingLatencies(view, nodeId, instance.application.id);
        return <TableRow key={instance.application.id}>
          <TableCell className="sticky left-0 z-10 bg-background font-medium"><span className="flex items-center gap-2"><RegionFlag code={instance.realityServices[0]?.regionCode} language={language} />{name}</span></TableCell>
          {servers.map((server) => {
            const check = checks.get(`${nodeId}:${server.nodeId}`);
            const latency = pairs.find((pair) => pair.server?.nodeId === server.nodeId)?.latency;
            const measured = latency?.state === "direct" && latency.latencyMs != null && Number.isFinite(latency.latencyMs) && latency.latencyMs >= 0 ? latency.latencyMs : undefined;
            const stamp = check?.checkedAt || check?.updatedAt;
            return <TableCell key={server.nodeId} className="text-center tabular-nums"><div className="flex flex-col items-center gap-1">
              <LinkBandwidthSummary check={check} language={language} unavailable={Boolean(quality?.error)} showUnit={false} />
              <div className="flex items-center gap-2 text-[11px] text-muted-foreground">{measured !== undefined ? <span className={landingLatencyColor(measured)}>{Math.round(measured)} ms</span> : null}{stamp ? <time dateTime={stamp}>{new Date(stamp).toLocaleString(language, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}</time> : <span>—</span>}</div>
            </div></TableCell>;
          })}
          <TableCell><NodeDiagnosticsButton nodeId={nodeId} name={name} language={language} compact linkBandwidth /></TableCell>
        </TableRow>;
      })}{!instances.length ? <TableRow><TableCell colSpan={servers.length + 2} className="py-8 text-center text-muted-foreground">{copy(language, "没有匹配的线路机", "No matching entry nodes")}</TableCell></TableRow> : null}</TableBody>
    </Table>
  </div>;
}
