import { useEffect, useState } from "react";
import { api } from "../api";
import type { AgentView } from "../types";
import { regionFlag } from "@/lib/regions";

export function useLandingRegions(nodeIds: readonly string[], agents: readonly AgentView[]) {
  // Depend on identity/address, not heartbeat or latency updates. IP changes
  // invalidate old flags; no region polling or unbounded request cache.
  const key = JSON.stringify(nodeIds.slice(0, 16).sort().flatMap((nodeId) => {
    const profile = agents.find((agent) => agent.id === nodeId)?.networkProfile;
    return profile?.directPublic && profile.publicAddress ? [[nodeId, profile.publicAddress]] : [];
  }));
  const [result, setResult] = useState<{ key: string; regions: Record<string, string> } | null>(null);

  useEffect(() => {
    const targets = JSON.parse(key) as [string, string][];
    if (!targets.length) return;
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 10000);
    let cancelled = false;
    void Promise.all(targets.map(async ([nodeId, address]) => {
      try {
        const suggestion = await api.agentRegionSuggestion(nodeId, controller.signal);
        // Never apply a result for an address that has since changed.
        return [nodeId, suggestion.agentId === nodeId && suggestion.publicAddress === address && regionFlag(suggestion.regionCode) ? suggestion.regionCode : ""] as const;
      } catch {
        return [nodeId, ""] as const;
      }
    })).then((entries) => {
      if (!cancelled) setResult({ key, regions: Object.fromEntries(entries) });
    }).finally(() => window.clearTimeout(timeout));
    return () => {
      cancelled = true;
      controller.abort();
      window.clearTimeout(timeout);
    };
  }, [key]);

  return result?.key === key ? result.regions : {};
}
