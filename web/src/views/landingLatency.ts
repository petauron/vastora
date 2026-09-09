import type { LandingView } from "../landing-types";

export function landingLatencyPreview(view: LandingView, nodeId: string, applicationId: string) {
  const selected = view.proxies.find((proxy) => proxy.applicationId === applicationId && proxy.enabled)?.landingNodeId;
  const samples = view.latencies.filter((sample) => sample.nodeId === nodeId && view.servers.some((server) => server.nodeId === sample.landingNodeId && server.status === "ready"));
  const best = samples.filter((sample) => sample.state === "direct" && sample.latencyMs != null && Number.isFinite(sample.latencyMs) && sample.latencyMs > 0).reduce<typeof samples[number] | undefined>((best, sample) => !best || sample.latencyMs! < best.latencyMs! ? sample : best, undefined);
  const landingId = selected ?? best?.landingNodeId ?? view.servers.find((server) => server.nodeId !== nodeId && server.status === "ready")?.nodeId;
  return { selected: Boolean(selected), server: view.servers.find((server) => server.nodeId === landingId), latency: samples.find((sample) => sample.landingNodeId === landingId) };
}

export function landingLatencyColor(milliseconds: number | null | undefined): string {
  if (milliseconds == null || !Number.isFinite(milliseconds) || milliseconds <= 0) return "text-muted-foreground";
  if (milliseconds < 20) return "text-latency-fast";
  if (milliseconds <= 100) return "text-latency-medium";
  return "text-destructive";
}
