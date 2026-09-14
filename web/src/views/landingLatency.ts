import type { LandingView } from "../landing-types";

export function selectedLandingLatencies(view: LandingView, nodeId: string, applicationId: string) {
  void applicationId;
  return view.servers
    .filter((server) => view.nodeIds.includes(server.nodeId) && server.nodeId !== nodeId)
    .map((server) => ({
      server,
      latency: view.latencies.find((sample) => sample.nodeId === nodeId && sample.landingNodeId === server.nodeId),
    }))
    .sort((left, right) => {
      const leftLatency = measuredLatency(left.latency?.state, left.latency?.latencyMs);
      const rightLatency = measuredLatency(right.latency?.state, right.latency?.latencyMs);
      if (leftLatency !== rightLatency) return leftLatency - rightLatency;
      return left.server.name.localeCompare(right.server.name);
    });
}

function measuredLatency(state: string | undefined, milliseconds: number | null | undefined): number {
  return state === "direct" && milliseconds != null && Number.isFinite(milliseconds) && milliseconds >= 0
    ? milliseconds
    : Number.POSITIVE_INFINITY;
}

export function landingLatencyColor(milliseconds: number | null | undefined): string {
  if (milliseconds == null || !Number.isFinite(milliseconds) || milliseconds < 0) return "text-muted-foreground";
  if (milliseconds < 20) return "text-latency-fast";
  if (milliseconds <= 100) return "text-latency-medium";
  return "text-destructive";
}
