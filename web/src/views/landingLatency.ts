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
  return milliseconds == null || !Number.isFinite(milliseconds) || milliseconds < 0 ? "text-muted-foreground" : "text-foreground";
}
