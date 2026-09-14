import { createContext, useCallback, useContext, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { api } from "../api";
import type { LandingLatencyEvent, LandingLatencySnapshot, LandingView } from "../landing-types";
import type { Language } from "../translations";
import type { AgentView } from "../types";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { cn } from "@/lib/utils";
import { ServerIcon } from "lucide-react";
import { copy, userError } from "./shared";
import { landingLatencyColor, selectedLandingLatencies } from "./landingLatency";
import { applyLandingLatencyEvent, freshLandingLatencies } from "./landingLatencyEvents";
import { RegionFlag } from "./RegionFlag";
import { IPQualityButton } from "./IPQuality";
import { useLandingRegions } from "./useLandingRegions";

type LandingContextValue = {
  view: LandingView | null;
  regions: Record<string, string>;
  busy: boolean;
  failed: boolean;
  changeError: unknown;
  refresh: () => void;
  change: (operation: (signal: AbortSignal) => Promise<LandingView>) => Promise<boolean>;
};

const LandingContext = createContext<LandingContextValue | null>(null);

// One shared stream delivers per-pair changes. The overview poll only keeps
// configuration/status current; it must not overwrite newer streamed latency.
export function LandingProvider({ enabled, agents = [], children }: { enabled: boolean; agents?: AgentView[]; children: ReactNode }) {
  const [view, setView] = useState<LandingView | null>(null);
  const discoveredRegions = useLandingRegions(enabled ? [...(view?.nodeIds ?? []), ...(view?.candidates.map((candidate) => candidate.nodeId) ?? [])] : [], agents);
  const regions = { ...(view?.landingRegionCodes ?? {}), ...discoveredRegions };
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const [changeError, setChangeError] = useState<unknown>(null);
  const generation = useRef(0);
  const writing = useRef(false);
  const reading = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  const liveLatencies = useRef<LandingLatencySnapshot | null>(null);

  const adoptView = useCallback((next: LandingView) => {
    const live = liveLatencies.current;
    setView({ ...next, latencies: freshLandingLatencies(live?.revision === next.revision ? live.samples : next.latencies) });
  }, []);

  const refresh = useCallback(async () => {
    if (!mounted.current || writing.current || reading.current) return;
    const controller = new AbortController();
    reading.current = controller;
    const current = generation.current;
    const timeout = window.setTimeout(() => controller.abort(), 15000);
    try {
      const next = await api.landing(controller.signal);
      if (mounted.current && generation.current === current) {
        adoptView(next);
        setFailed(false);
      }
    } catch {
      if (mounted.current && generation.current === current) setFailed(true);
      return false;
    } finally {
      window.clearTimeout(timeout);
      if (reading.current === controller) reading.current = null;
    }
  }, [adoptView]);

  useEffect(() => {
    if (!enabled) return;
    mounted.current = true;
    liveLatencies.current = null;
    void refresh();
    const source = new EventSource("/api/v1/three-x-ui/landing/latencies/events", { withCredentials: true });
    source.onmessage = (message) => {
      if (!mounted.current) return;
      try {
        const next = applyLandingLatencyEvent(liveLatencies.current, JSON.parse(message.data) as LandingLatencyEvent);
        if (!next) return;
        liveLatencies.current = next;
        setView((current) => current?.revision === next.revision ? { ...current, latencies: freshLandingLatencies(next.samples) } : current);
      } catch {
        // Ignore an incomplete event. Reconnection begins with a fresh snapshot.
      }
    };
    // EventSource owns reconnection. Retain fresh values while disconnected.
    const expiryTimer = window.setInterval(() => {
      setView((current) => {
        if (!current) return current;
        const latencies = freshLandingLatencies(current.latencies);
        return latencies === current.latencies ? current : { ...current, latencies };
      });
    }, 1000);
    const timer = window.setInterval(() => void refresh(), 15000);
    return () => {
      mounted.current = false;
      generation.current++;
      reading.current?.abort();
      reading.current = null;
      source.onmessage = null;
      source.close();
      liveLatencies.current = null;
      window.clearInterval(expiryTimer);
      window.clearInterval(timer);
    };
  }, [enabled, refresh]);

  const change = async (operation: (signal: AbortSignal) => Promise<LandingView>) => {
    if (!mounted.current || writing.current) return false;
    writing.current = true;
    setChangeError(null);
    const current = ++generation.current;
    reading.current?.abort();
    reading.current = null;
    setBusy(true);
    setFailed(false);
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 15000);
    try {
      const next = await operation(controller.signal);
      if (mounted.current && generation.current === current) adoptView(next);
      return true;
    } catch (error) {
      if (mounted.current && generation.current === current) setChangeError(error);
      // The server may have accepted a request whose reply was lost. Require
      // a new overview before allowing another revision-sensitive mutation.
      if (mounted.current && generation.current === current) setFailed(true);
      return false;
    } finally {
      window.clearTimeout(timeout);
      writing.current = false;
      if (mounted.current) setBusy(false);
    }
  };

  return <LandingContext.Provider value={{ view, regions, busy, failed, changeError, refresh: () => void refresh(), change }}>{children}</LandingContext.Provider>;
}

export function LandingManager({ language }: { language: Language }) {
  const state = useContext(LandingContext);
  const [open, setOpen] = useState(false);
  const [candidateID, setCandidateID] = useState("");
  const id = useId();
  if (!state) return null;
  const { view, busy, failed } = state;
  const candidates = view?.candidates.filter((candidate) => !view.nodeIds.includes(candidate.nodeId) && !view.retiringNodeIds?.includes(candidate.nodeId)) ?? [];
  const candidate = candidates.find((item) => item.nodeId === candidateID);
  const regionCodes = (nodeIds: string[]) => Object.fromEntries(nodeIds.flatMap((nodeId) => {
    const code = state.regions[nodeId] ?? view?.landingRegionCodes?.[nodeId];
    return code ? [[nodeId, code]] : [];
  }));
  const disabled = !view || busy || failed;
  return <Sheet open={open} onOpenChange={setOpen}>
    <SheetTrigger render={<Button variant="outline" size="sm" />}>
      <ServerIcon data-icon="inline-start" />{copy(language, "管理落地机", "Landing servers")}
    </SheetTrigger>
    <SheetContent className="apps-workspace apps-landing-sheet">
      <SheetHeader>
        <SheetTitle>{copy(language, "管理落地机", "Landing servers")}</SheetTitle>
        <SheetDescription>{copy(language, "添加后将自动应用到所有符合条件的 VLESS 节点，并可能重置现有组合连接。", "Added servers automatically apply to every eligible VLESS entry and may reset existing combination sessions.")}</SheetDescription>
      </SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-8 overflow-y-auto px-5 pb-5">
        <LandingNotice language={language} />
        {state.changeError != null ? <Alert variant="destructive"><AlertTitle>{copy(language, "全局落地池未更新", "Global landing pool was not updated")}</AlertTitle><AlertDescription>{userError(language, state.changeError)}</AlertDescription></Alert> : null}
        {view ? <Alert>
          <AlertTitle>{view.status === "ready" ? copy(language, "全局落地池已就绪", "Global landing pool ready") : view.status === "failed" ? copy(language, "部分组合需要处理", "Some combinations need attention") : copy(language, "正在同步全局落地池", "Syncing global landing pool")}</AlertTitle>
          <AlertDescription>{copy(language, `${view.eligibleEntries} 个 VLESS 入口 · ${view.readyCombinations} 个组合就绪 · ${view.failedCombinations} 个失败 · ${view.withheldCombinations} 个暂缓发布`, `${view.eligibleEntries} VLESS entries · ${view.readyCombinations} combinations ready · ${view.failedCombinations} failed · ${view.withheldCombinations} withheld`)}</AlertDescription>
        </Alert> : null}
        {!view && !failed ? <p role="status" className="text-muted-foreground">{copy(language, "正在读取…", "Loading…")}</p> : null}
        <section aria-label={copy(language, "已配置的落地机", "Configured landing servers")}>
          <ul className="divide-y divide-border">
            {view?.servers.map((server) => <li key={server.nodeId} className="flex items-center gap-3 py-4">
              <span aria-hidden="true" className={`apps-status-dot ${server.status === "ready" ? "bg-latency-fast" : server.status === "failed" || server.status === "offline" ? "bg-destructive" : "bg-muted-foreground"}`} />
              <div className="min-w-0 flex-1">
                <p className="flex items-center gap-2 font-medium" title={server.name}><RegionFlag code={state.regions[server.nodeId]} language={language} /><span className="truncate">{server.name}</span></p>
                <p className="mt-1 text-xs text-muted-foreground">
                  {server.status === "ready" ? copy(language, "可用", "Available") : server.status === "draining" ? copy(language, "正在排空并移除…", "Draining and removing…") : server.status === "offline" ? copy(language, "离线", "Offline") : server.status === "failed" ? copy(language, "配置失败，请检查节点", "Setup failed. Check the node.") : copy(language, "正在准备…", "Preparing…")}
                </p>
                <p className="mt-1 text-xs text-muted-foreground">{copy(language, `${server.eligibleEntries} 个入口 · ${server.readyCombinations} 就绪 · ${server.failedCombinations} 失败 · ${server.withheldCombinations} 暂缓`, `${server.eligibleEntries} entries · ${server.readyCombinations} ready · ${server.failedCombinations} failed · ${server.withheldCombinations} withheld`)}</p>
                <IPQualityButton nodeId={server.nodeId} name={server.name} language={language} />
              </div>
              <Button variant="outline" size="sm" disabled={disabled || server.status === "draining"} aria-label={copy(language, `移除 ${server.name}`, `Remove ${server.name}`)} onClick={() => {
                if (view) {
                  const nodeIds = view.nodeIds.filter((nodeID) => nodeID !== server.nodeId);
                  void state.change((signal) => api.selectLanding(nodeIds, view.revision, regionCodes(nodeIds), signal));
                }
              }}>{copy(language, "移除", "Remove")}</Button>
            </li>)}
          </ul>
          {view && !view.servers.length ? <p className="py-4 text-muted-foreground">{copy(language, "还没有添加落地机。", "No landing servers yet.")}</p> : null}
          {view?.servers.some((server) => server.status === "draining") ? <p className="mt-3 text-xs text-muted-foreground">{copy(language, "移除会先停止发布相关组合，确认会话、路由和授权均已清理后再卸载服务。", "Removal first stops publishing combinations, then uninstalls the service after sessions, routes, and grants are confirmed removed.")}</p> : null}
        </section>
        <form onSubmit={(event) => {
          event.preventDefault();
          if (!disabled && view && candidate && view.nodeIds.length < 16) {
            const nodeIds = [...view.nodeIds, candidate.nodeId];
            void state.change((signal) => api.selectLanding(nodeIds, view.revision, regionCodes(nodeIds), signal));
          }
        }}>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor={id}>{copy(language, "添加落地机", "Add a landing server")}</FieldLabel>
              <div className="flex gap-2">
                <Select items={candidates.map((item) => ({ value: item.nodeId, label: item.name }))} value={candidate?.nodeId ?? null} disabled={disabled || !candidates.length || (view?.nodeIds.length ?? 0) >= 16} onValueChange={(value) => setCandidateID(value ?? "")}>
                  <SelectTrigger id={id} className="min-w-0 flex-1"><SelectValue placeholder={copy(language, "选择节点", "Choose a node")} /></SelectTrigger>
                  <SelectContent className="apps-workspace"><SelectGroup>{candidates.map((item) => <SelectItem key={item.nodeId} value={item.nodeId}>{item.name}</SelectItem>)}</SelectGroup></SelectContent>
                </Select>
                <Button type="submit" disabled={disabled || !candidate || (view?.nodeIds.length ?? 0) >= 16}>{copy(language, "添加", "Add")}</Button>
              </div>
              <FieldDescription>{(view?.nodeIds.length ?? 0) >= 16 ? copy(language, "最多可配置 16 台落地机。", "Up to 16 landing servers.") : copy(language, "仅显示在线且已接入私网的节点。", "Only online nodes on the managed private network are listed.")}</FieldDescription>
            </Field>
          </FieldGroup>
        </form>
      </div>
      <SheetFooter className="border-t border-border"><Button onClick={() => setOpen(false)}>{copy(language, "完成", "Done")}</Button></SheetFooter>
    </SheetContent>
  </Sheet>;
}

export function LandingNotice({ language }: { language: Language }) {
  const state = useContext(LandingContext);
  if (!state?.failed) return null;
  return <Alert variant="destructive">
    <AlertTitle>{copy(language, "暂时无法确认落地设置", "Landing settings unavailable")}</AlertTitle>
    <AlertDescription>
      <Button type="button" variant="outline" size="sm" disabled={state.busy} onClick={state.refresh}>{copy(language, "刷新状态", "Refresh status")}</Button>
    </AlertDescription>
  </Alert>;
}

function latencyLabel(language: Language, latency: LandingView["latencies"][number] | undefined) {
  if (latency?.state === "direct" && latency.latencyMs != null && Number.isFinite(latency.latencyMs) && latency.latencyMs >= 0) return `${latency.latencyMs < 1 ? "<1" : Math.round(latency.latencyMs)} ms`;
  return latency?.state === "unavailable" ? copy(language, "无法直连", "Unavailable") : copy(language, "待检测", "Pending");
}

export function LandingLatency({ applicationId, nodeId, language }: { applicationId: string; nodeId: string; language: Language }) {
  const state = useContext(LandingContext);
  const view = state?.view;
  if (!state || !view || state.failed) return <span className="text-muted-foreground">—</span>;
  const pairs = selectedLandingLatencies(view, nodeId, applicationId);
  if (!pairs.length) return <span className="text-muted-foreground">—</span>;
  return <div className="flex min-w-0 flex-row flex-wrap items-center gap-x-3 gap-y-1">
    {pairs.map(({ server, latency }, index) => {
      const measured = server?.status === "ready" && latency?.state === "direct" && latency.latencyMs != null && Number.isFinite(latency.latencyMs) && latency.latencyMs >= 0;
      const unavailable = !server || ["offline", "failed", "stopped"].includes(server.status) || server.status === "ready" && latency?.state === "unavailable";
      const label = measured ? latencyLabel(language, latency) : unavailable ? copy(language, "不可用", "Unavailable") : copy(language, "待检测", "Pending");
      return <span className={cn("inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap text-xs tabular-nums", unavailable ? "text-destructive" : measured ? landingLatencyColor(latency.latencyMs) : "text-muted-foreground")} key={server?.nodeId ?? `missing-${index}`} title={server?.name}>
        <RegionFlag code={server ? state.regions[server.nodeId] : undefined} language={language} />
        <span>{label}</span>
      </span>;
    })}
  </div>;
}
