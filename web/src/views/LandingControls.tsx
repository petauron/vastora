import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { api } from "../api";
import type { LandingLatencyEvent, LandingLatencySnapshot, LandingView } from "../landing-types";
import type { Language } from "../translations";
import type { AgentView } from "../types";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { copy } from "./shared";
import { applyLandingLatencyEvent, freshLandingLatencies } from "./landingLatencyEvents";
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

export function useLanding() { return useContext(LandingContext); }

// One shared stream delivers per-pair changes. The overview poll only keeps
// configuration/status current; it must not overwrite newer streamed latency.
export function LandingProvider({ enabled, agents = [], children }: { enabled: boolean; agents?: AgentView[]; children: ReactNode }) {
  const [view, setView] = useState<LandingView | null>(null);
  const discoveredRegions = useLandingRegions(enabled ? [...(view?.nodeIds ?? []), ...(view?.candidates.map((candidate) => candidate.nodeId) ?? [])] : [], agents);
  const regions = { ...(view?.landingRegionCodes ?? {}) };
  for (const [nodeId, code] of Object.entries(discoveredRegions)) {
    if (code) regions[nodeId] = code;
  }
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
    const source = new EventSource("/api/v1/meridian/landing/latencies/events", { withCredentials: true });
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
