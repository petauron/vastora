import type { LandingView } from "../landing-types";

export function selectedLandingLatencies(view: LandingView, nodeId: string, applicationId: string) {
  const policy = view.nodeExits?.find((item) => item.applicationId === applicationId);
  const previous = view.proxies.find((proxy) => proxy.applicationId === applicationId && proxy.enabled);
  const targets = policy?.revision ? policy.landingNodeIds : previous ? [previous.landingNodeId] : [];
  // Show only configured exits. A fast, unselected server is not active routing.
  return targets.map((target) => ({
    server: view.servers.find((server) => server.nodeId === target),
    latency: view.latencies.find((sample) => sample.nodeId === nodeId && sample.landingNodeId === target),
  }));
}

export function landingLatencyColor(milliseconds: number | null | undefined): string {
  if (milliseconds == null || !Number.isFinite(milliseconds) || milliseconds < 0) return "text-muted-foreground";
  if (milliseconds < 20) return "text-latency-fast";
  if (milliseconds <= 100) return "text-latency-medium";
  return "text-destructive";
}
