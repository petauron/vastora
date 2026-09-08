export type LandingProxyView = {
  applicationId: string;
  revision: number;
  enabled: boolean;
  status: "pending" | "applying" | "ready" | "failed" | "stopped";
  connection: "disabled" | "pending" | "healthy" | "unhealthy";
};

export type LandingView = {
  nodeId: string;
  revision: number;
  status: "disabled" | "pending" | "ready" | "failed";
  candidates: Array<{ nodeId: string; name: string }>;
  proxies: LandingProxyView[];
  latencies: Array<{ nodeId: string; state: "direct" | "unavailable"; latencyMs?: number }>;
};
