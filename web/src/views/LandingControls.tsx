import { createContext, useCallback, useContext, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { api } from "../api";
import type { LandingLatencyEvent, LandingLatencySnapshot, LandingView } from "../landing-types";
import type { Language } from "../translations";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldGroup, FieldLabel, FieldSet, FieldLegend } from "@/components/ui/field";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { Checkbox } from "@/components/ui/checkbox";
import { ServerIcon, SlidersHorizontalIcon } from "lucide-react";
import { copy } from "./shared";
import { landingLatencyColor, landingLatencyPreview } from "./landingLatency";
import { applyLandingLatencyEvent, freshLandingLatencies } from "./landingLatencyEvents";

type LandingContextValue = {
  view: LandingView | null;
  busy: boolean;
  failed: boolean;
  refresh: () => void;
  change: (operation: (signal: AbortSignal) => Promise<LandingView>) => Promise<boolean>;
};

const LandingContext = createContext<LandingContextValue | null>(null);

// One shared stream delivers per-pair changes. The overview poll only keeps
// configuration/status current; it must not overwrite newer streamed latency.
export function LandingProvider({ enabled, children }: { enabled: boolean; children: ReactNode }) {
  const [view, setView] = useState<LandingView | null>(null);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
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
    } catch {
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

  return <LandingContext.Provider value={{ view, busy, failed, refresh: () => void refresh(), change }}>{children}</LandingContext.Provider>;
}

export function LandingManager({ language }: { language: Language }) {
  const state = useContext(LandingContext);
  const [open, setOpen] = useState(false);
  const [candidateID, setCandidateID] = useState("");
  const id = useId();
  if (!state) return null;
  const { view, busy, failed } = state;
  const candidates = view?.candidates.filter((candidate) => !view.nodeIds.includes(candidate.nodeId)) ?? [];
  const candidate = candidates.find((item) => item.nodeId === candidateID);
  const disabled = !view || busy || failed;
  return <Sheet open={open} onOpenChange={setOpen}>
    <SheetTrigger render={<Button variant="outline" size="sm" />}>
      <ServerIcon data-icon="inline-start" />{copy(language, "管理落地机", "Landing servers")}
    </SheetTrigger>
    <SheetContent className="apps-workspace apps-landing-sheet">
      <SheetHeader>
        <SheetTitle>{copy(language, "管理落地机", "Landing servers")}</SheetTitle>
        <SheetDescription>{copy(language, "可添加多台，在节点列表中勾选一个或多个出口。", "Add servers, then select one or more exits in each node row.")}</SheetDescription>
      </SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-8 overflow-y-auto px-5 pb-5">
        <LandingNotice language={language} />
        {!view && !failed ? <p role="status" className="text-muted-foreground">{copy(language, "正在读取…", "Loading…")}</p> : null}
        <section aria-label={copy(language, "已配置的落地机", "Configured landing servers")}>
          <ul className="divide-y divide-border">
            {view?.servers.map((server) => <li key={server.nodeId} className="flex items-center gap-3 py-4">
              <span aria-hidden="true" className={`apps-status-dot ${server.status === "ready" ? "bg-latency-fast" : server.status === "failed" || server.status === "offline" ? "bg-destructive" : "bg-muted-foreground"}`} />
              <div className="min-w-0 flex-1">
                <p className="truncate font-medium" title={server.name}>{server.name}</p>
                <p className="mt-1 text-xs text-muted-foreground">
                  {server.status === "ready" ? copy(language, "可用", "Available") : server.status === "offline" ? copy(language, "离线", "Offline") : server.status === "failed" ? copy(language, "配置失败，请检查节点", "Setup failed. Check the node.") : copy(language, "正在准备…", "Preparing…")}
                  {server.inUse ? copy(language, " · 使用中", " · In use") : ""}
                </p>
              </div>
              <Button variant="outline" size="sm" disabled={disabled || server.inUse} aria-label={copy(language, `移除 ${server.name}`, `Remove ${server.name}`)} onClick={() => {
                if (view) void state.change((signal) => api.selectLanding(view.nodeIds.filter((nodeID) => nodeID !== server.nodeId), view.revision, signal));
              }}>{copy(language, "移除", "Remove")}</Button>
            </li>)}
          </ul>
          {view && !view.servers.length ? <p className="py-4 text-muted-foreground">{copy(language, "还没有添加落地机。", "No landing servers yet.")}</p> : null}
          {view?.servers.some((server) => server.inUse) ? <p className="mt-3 text-xs text-muted-foreground">{copy(language, "使用中的落地机不能移除，请先切换相关节点的出口。", "Move connected nodes to another exit before removing a server.")}</p> : null}
        </section>
        <form onSubmit={(event) => {
          event.preventDefault();
          if (!disabled && view && candidate && view.nodeIds.length < 16) void state.change((signal) => api.selectLanding([...view.nodeIds, candidate.nodeId], view.revision, signal));
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
  if (latency?.state === "direct" && latency.latencyMs != null) return `${latency.latencyMs < 1 ? "<1" : Math.round(latency.latencyMs)} ms`;
  return latency?.state === "unavailable" ? copy(language, "无法直连", "Unavailable") : copy(language, "待检测", "Pending");
}

export function LandingExitSelect({ applicationId, nodeId, name, locked, language }: { applicationId: string; nodeId: string; name: string; locked: boolean; language: Language }) {
  const state = useContext(LandingContext);
  const id = useId();
  const [draft, setDraft] = useState<{ ownExit: boolean; landingNodeIds: string[]; revision: number } | null>(null);
  if (!state) return null;
  const { view, busy, failed } = state;
  const policy = view?.nodeExits?.find((item) => item.applicationId === applicationId);
  const servers = view?.servers.filter((server) => server.nodeId !== nodeId) ?? [];
  const blocked = Boolean(view?.tasksPaused || view?.blockedNodeIds?.includes(nodeId));
  const disabled = locked || busy || failed || !view || blocked || policy?.status === "applying";
  const ownName = copy(language, "本机出口", "Own exit");
  const exitName = (target: string) => servers.find((server) => server.nodeId === target)?.name ?? copy(language, "不可用落地机", "Unavailable exit");
  const previous = view?.proxies.find((item) => item.applicationId === applicationId && item.enabled);
  const names = [...(policy?.ownExit !== false ? [ownName] : []), ...(policy?.landingNodeIds ?? []).map(exitName)];
  const stale = draft !== null && draft.revision !== (policy?.revision ?? 0);
  const invalid = !draft || (!draft.ownExit && draft.landingNodeIds.length === 0) || draft.landingNodeIds.some((target) => view?.blockedNodeIds?.includes(target) || !servers.some((server) => server.nodeId === target && server.status === "ready"));
  const status = view?.tasksPaused ? copy(language, "任务已暂停，等待恢复", "Tasks paused; waiting to resume")
    : blocked ? copy(language, "节点任务待处理", "Node task needs attention")
    : policy?.status === "applying" ? copy(language, "正在同步组合…", "Syncing combinations…")
    : policy?.status === "failed" ? copy(language, "同步失败，请重新保存", "Sync failed; save again to retry")
    : policy?.revision ? copy(language, "出口配置已保存", "Exit configuration saved") : null;
  return <div className="flex min-w-0 flex-col gap-1">
    <Button id={id} variant="outline" className="w-full justify-between" disabled={disabled} aria-label={copy(language, `配置 ${name} 的出口`, `Configure exits for ${name}`)} title={names.join(" · ")} onClick={() => setDraft({ ownExit: policy?.ownExit ?? true, landingNodeIds: [...(policy?.landingNodeIds ?? [])], revision: policy?.revision ?? 0 })}>
      <span className="truncate">{failed ? copy(language, "状态未知", "Unknown") : view ? !policy?.revision && previous ? copy(language, `原单出口：${exitName(previous.landingNodeId)}`, `Previous exit: ${exitName(previous.landingNodeId)}`) : names.join(" · ") : copy(language, "读取出口", "Loading exits")}</span><SlidersHorizontalIcon data-icon="inline-end" />
    </Button>
    {status ? <p role="status" className="text-xs text-muted-foreground">{status}</p> : null}
    <Sheet open={draft !== null} onOpenChange={(open) => { if (!open && !busy) setDraft(null); }}>
      <SheetContent className="apps-workspace" finalFocus={() => document.getElementById(id)}>
        <SheetHeader>
          <SheetTitle>{copy(language, `${name} · 出口组合`, `${name} · Exit combinations`)}</SheetTitle>
          <SheetDescription>{copy(language, "每个勾选项生成一个独立订阅节点。同步给已接入此节点的客户端，后续新增客户端也会继承。", "Each selected exit generates a separate subscription node for clients attached to this entry, including future clients.")}</SheetDescription>
        </SheetHeader>
        <div className="min-h-0 flex-1 overflow-y-auto px-4">
          <FieldSet disabled={busy}>
            <FieldLegend>{copy(language, "选择出口", "Choose exits")}</FieldLegend>
            <FieldDescription>{copy(language, "至少选择一项；保存可能短暂中断此节点连接，订阅地址不变。", "Choose at least one. Saving may briefly interrupt this entry; subscription URLs stay unchanged.")}</FieldDescription>
            <FieldGroup>
              <Field orientation="horizontal" data-disabled={policy?.requiresOwnExit}>
                <Checkbox id={id + "-own"} checked={draft?.ownExit ?? true} disabled={policy?.requiresOwnExit} onCheckedChange={(checked) => setDraft((value) => value ? { ...value, ownExit: checked } : value)} />
                <FieldLabel htmlFor={id + "-own"}>{ownName}</FieldLabel>
              </Field>
              {servers.map((server) => {
                const selected = draft?.landingNodeIds.includes(server.nodeId) ?? false;
                const unavailable = server.status !== "ready" || view?.blockedNodeIds?.includes(server.nodeId);
                const latency = view?.latencies.find((sample) => sample.nodeId === nodeId && sample.landingNodeId === server.nodeId);
                return <Field key={server.nodeId} orientation="horizontal" data-disabled={!selected && unavailable}>
                  <Checkbox id={id + server.nodeId} checked={selected} disabled={!selected && unavailable} onCheckedChange={(checked) => setDraft((value) => value ? { ...value, landingNodeIds: checked ? [...value.landingNodeIds, server.nodeId] : value.landingNodeIds.filter((target) => target !== server.nodeId) } : value)} />
                  <FieldLabel className="min-w-0 flex-1" htmlFor={id + server.nodeId}>{server.name}</FieldLabel>
                  <span className={landingLatencyColor(latency?.state === "direct" ? latency.latencyMs : null)}>{unavailable ? copy(language, "未就绪", "Not ready") : latencyLabel(language, latency)}</span>
                </Field>;
              })}
            </FieldGroup>
            {policy?.requiresOwnExit ? <FieldDescription>{copy(language, "HY2 当前仅支持本机出口；落地组合使用 VLESS，因此需保留本机出口。", "HY2 currently uses the own exit. Landing combinations use VLESS, so keep the own exit enabled.")}</FieldDescription> : null}
            {stale ? <FieldDescription role="alert">{copy(language, "配置已变化，请关闭后重新打开。", "Configuration changed. Close and reopen.")}</FieldDescription> : null}
            <LandingNotice language={language} />
          </FieldSet>
        </div>
        <SheetFooter>
          <Button variant="outline" disabled={busy} onClick={() => setDraft(null)}>{copy(language, "取消", "Cancel")}</Button>
          <Button disabled={disabled || stale || invalid} onClick={() => {
            if (!draft || disabled || stale || invalid) return;
            void state.change((signal) => api.configureNodeExits(applicationId, { ...draft, confirmSessionReset: true }, signal)).then((saved) => { if (saved) setDraft(null); });
          }}>{busy ? copy(language, "正在保存…", "Saving…") : copy(language, "保存出口组合", "Save exit combinations")}</Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  </div>;
}

// Keep a useful preview when a node still uses its own exit. Never mistake a
// sample from another source or landing server for the selected pair.
export function LandingLatency({ applicationId, nodeId, language }: { applicationId: string; nodeId: string; language: Language }) {
  const state = useContext(LandingContext);
  const view = state?.view;
  if (!state || !view || state.failed) return <span className="text-muted-foreground">—</span>;
  const policy = view.nodeExits?.find((item) => item.applicationId === applicationId);
  if (policy?.landingNodeIds.length) return <div className="flex min-w-0 flex-col gap-2">
    {policy.landingNodeIds.map((target) => {
      const server = view.servers.find((item) => item.nodeId === target);
      const latency = view.latencies.find((sample) => sample.nodeId === nodeId && sample.landingNodeId === target);
      return <div key={target} className="flex min-w-0 flex-col gap-1">
        <span className={landingLatencyColor(latency?.state === "direct" ? latency.latencyMs : null)}>{server?.status === "ready" ? latencyLabel(language, latency) : copy(language, "未就绪", "Not ready")}</span>
        <span className="truncate text-xs text-muted-foreground">{server?.name ?? copy(language, "不可用落地机", "Unavailable exit")}</span>
      </div>;
    })}
  </div>;
  const { selected, server, latency } = landingLatencyPreview(view, nodeId, applicationId);
  if (!server) return <span className="text-muted-foreground">—</span>;
  return <div className="flex min-w-0 flex-col gap-1">
    <span className={`tabular-nums ${landingLatencyColor(latency?.state === "direct" ? latency.latencyMs : null)}`}>{server?.status === "ready" ? latencyLabel(language, latency) : copy(language, "未就绪", "Not ready")}</span>
    {!selected && server ? <span className="truncate text-xs text-muted-foreground" title={server.name}>{server.name}</span> : null}
  </div>;
}
