import type { AgentView } from "./types";

// A stale or offline observation cannot establish the current candidate.
export function freshTailscaleUpdate(agent: AgentView): string | null {
  const update = agent.capabilities.tailscaleUpdate;
  const checkedAt = update?.checkedAt ? Date.parse(update.checkedAt) : NaN;
  if (!agent.connected || agent.status !== "active" || !update || update.error || !update.updateAvailable || !update.latestVersion || !Number.isFinite(checkedAt) || checkedAt > Date.now() + 5 * 60_000 || checkedAt < Date.now() - 48 * 60 * 60_000) return null;
  return update.latestVersion;
}
