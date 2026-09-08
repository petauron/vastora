import { createContext, useCallback, useContext, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { api } from "../api";
import type { LandingView } from "../landing-types";
import type { Language } from "../translations";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { copy } from "./shared";

type LandingContextValue = {
  view: LandingView | null;
  busy: boolean;
  failed: boolean;
  refresh: () => void;
  change: (operation: (signal: AbortSignal) => Promise<LandingView>) => Promise<void>;
};

const LandingContext = createContext<LandingContextValue | null>(null);

// One request stream for the installed-app list, not one poll per node.
export function LandingProvider({ enabled, children }: { enabled: boolean; children: ReactNode }) {
  const [view, setView] = useState<LandingView | null>(null);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const generation = useRef(0);
  const writing = useRef(false);
  const reading = useRef<AbortController | null>(null);
  const mounted = useRef(false);

  const refresh = useCallback(async () => {
    if (!mounted.current || writing.current || reading.current) return;
    const controller = new AbortController();
    reading.current = controller;
    const current = generation.current;
    const timeout = window.setTimeout(() => controller.abort(), 15000);
    try {
      const next = await api.landing(controller.signal);
      if (mounted.current && generation.current === current) {
        setView(next);
        setFailed(false);
      }
    } catch {
      if (mounted.current && generation.current === current) setFailed(true);
    } finally {
      window.clearTimeout(timeout);
      if (reading.current === controller) reading.current = null;
    }
  }, []);

  useEffect(() => {
    if (!enabled) return;
    mounted.current = true;
    void refresh();
    const timer = window.setInterval(() => void refresh(), 15000);
    return () => {
      mounted.current = false;
      generation.current++;
      reading.current?.abort();
      reading.current = null;
      window.clearInterval(timer);
    };
  }, [enabled, refresh]);

  const change = async (operation: (signal: AbortSignal) => Promise<LandingView>) => {
    if (!mounted.current || writing.current) return;
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
      if (mounted.current && generation.current === current) setView(next);
    } catch {
      // The server may have accepted a request whose reply was lost. Require
      // a new overview before allowing another revision-sensitive mutation.
      if (mounted.current && generation.current === current) setFailed(true);
    } finally {
      window.clearTimeout(timeout);
      writing.current = false;
      if (mounted.current) setBusy(false);
    }
  };

  return <LandingContext.Provider value={{ view, busy, failed, refresh: () => void refresh(), change }}>{children}</LandingContext.Provider>;
}

export function LandingSelector({ language }: { language: Language }) {
  const state = useContext(LandingContext);
  const id = useId();
  if (!state) return null;
  const { view, busy, failed } = state;
  const inUse = view?.proxies.some((proxy) => proxy.enabled || proxy.status === "pending" || proxy.status === "applying") ?? false;
  const disabled = !view || busy || failed || inUse;
  const items = [
    { value: "", label: copy(language, "不使用落地机", "No landing server") },
    ...(view?.candidates.map((candidate) => ({ value: candidate.nodeId, label: candidate.name })) ?? [])
  ];
  if (view?.nodeId && !items.some((item) => item.value === view.nodeId)) {
    items.push({ value: view.nodeId, label: copy(language, "当前落地机（暂不可用）", "Current landing server (unavailable)") });
  }
  return <FieldGroup className="max-w-xl gap-3">
    <Field data-disabled={disabled} aria-busy={busy || (!view && !failed)}>
      <FieldLabel htmlFor={id}>{copy(language, "落地机", "Landing server")}</FieldLabel>
      <Select items={items} value={view?.nodeId ?? ""} disabled={disabled} onValueChange={(nodeId) => {
        if (view && typeof nodeId === "string" && nodeId !== view.nodeId) void state.change((signal) => api.selectLanding(nodeId, view.revision, signal));
      }}>
        <SelectTrigger id={id} className="w-full min-h-11" aria-describedby={`${id}-help`}><SelectValue /></SelectTrigger>
        <SelectContent><SelectGroup>{items.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectGroup></SelectContent>
      </Select>
      <FieldDescription id={`${id}-help`}>
        {inUse ? copy(language, "更换前，请先关闭下方节点的落地开关。", "Turn off landing on the nodes below before changing servers.")
          : copy(language, "选好落地机后，在需要的节点上开启。切换时连接会短暂中断。", "Choose a server, then enable it on individual nodes. Switching briefly interrupts connections.")}
      </FieldDescription>
      <FieldDescription aria-live="polite">
        {busy ? copy(language, "正在保存…", "Saving…") : !view && !failed ? copy(language, "正在读取…", "Loading…")
          : !failed && view?.status === "pending" ? copy(language, "正在准备落地机…", "Preparing landing server…")
            : !failed && view?.status === "failed" ? copy(language, "落地机尚未就绪，请检查节点。", "Landing server is not ready. Check the node.") : null}
      </FieldDescription>
    </Field>
    {failed ? <Alert variant="destructive">
      <AlertTitle>{copy(language, "暂时无法确认落地设置", "Landing settings unavailable")}</AlertTitle>
      <AlertDescription>
        <p>{copy(language, "请刷新状态后再操作。", "Refresh the status before making changes.")}</p>
        <Button type="button" variant="outline" className="min-h-11" disabled={busy} onClick={state.refresh}>{copy(language, "刷新状态", "Refresh status")}</Button>
      </AlertDescription>
    </Alert> : null}
  </FieldGroup>;
}

export function LandingSwitch({ applicationId, nodeId, name, locked, language }: { applicationId: string; nodeId: string; name: string; locked: boolean; language: Language }) {
  const state = useContext(LandingContext);
  const id = useId();
  if (!state) return null;
  const { view, busy, failed } = state;
  const proxy = view?.proxies.find((item) => item.applicationId === applicationId);
  const enabled = proxy?.enabled ?? false;
  const pending = proxy?.status === "pending" || proxy?.status === "applying";
  const configurationFailed = proxy?.status === "failed";
  const latency = view?.latencies.find((item) => item.nodeId === nodeId);
  const landingName = view?.candidates.find((item) => item.nodeId === view.nodeId)?.name ?? copy(language, "落地机", "Landing server");
  const disabled = locked || busy || failed || !view || pending || (!enabled && (view.status !== "ready" || view.nodeId === nodeId));
  const status = failed ? copy(language, "状态暂不可用", "Status unavailable")
    : pending ? copy(language, "正在调整…", "Applying…")
      : configurationFailed ? copy(language, "调整失败，请重试", "Change failed. Please retry.")
      : enabled ? proxy?.connection === "healthy" ? copy(language, "落地已连接", "Landing connected") : copy(language, "落地连接不可用", "Landing unavailable")
        : !view ? copy(language, "正在读取…", "Loading…")
          : view.nodeId === nodeId ? copy(language, "本机提供落地", "This is the landing server")
            : !view.nodeId ? copy(language, "请先选择落地机", "Choose a landing server first")
              : null;
  const latencyText = !failed && view?.nodeId && view.nodeId !== nodeId
    ? `${landingName} · ${latency?.state === "direct" && latency.latencyMs != null
      ? `${latency.latencyMs < 1 ? "<1" : Math.round(latency.latencyMs)} ms`
      : latency?.state === "unavailable" ? copy(language, "无法直连", "Direct connection unavailable")
        : copy(language, "延迟待检测", "Latency pending")}`
    : null;
  return <FieldGroup className="mt-2 gap-1">
    <Field orientation="horizontal" data-disabled={disabled} className="min-h-11" aria-busy={busy || pending}>
      <Switch id={id} checked={enabled} disabled={disabled} aria-label={copy(language, `${name} 使用落地机`, `Use landing server for ${name}`)} aria-describedby={`${id}-status`} onCheckedChange={(checked) => void state.change((signal) => api.configureLandingProxy(applicationId, checked, proxy?.revision ?? 0, signal))} />
      <FieldLabel htmlFor={id}>{copy(language, "使用落地", "Use landing")}</FieldLabel>
    </Field>
    <FieldDescription id={`${id}-status`} aria-live="polite">{status}{status && latencyText ? " · " : null}{latencyText}</FieldDescription>
    {configurationFailed ? <Button type="button" variant="outline" size="sm" disabled={locked || busy || failed || !view || (enabled && view.status !== "ready")} aria-label={copy(language, `${name} 重试落地设置`, `Retry landing settings for ${name}`)} onClick={() => void state.change((signal) => api.configureLandingProxy(applicationId, enabled, proxy.revision, signal))}>{copy(language, "重试", "Retry")}</Button> : null}
  </FieldGroup>;
}
