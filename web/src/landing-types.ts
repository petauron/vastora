export type LandingProxyView = {
  applicationId: string;
  landingNodeId: string;
  revision: number;
  enabled: boolean;
  status: "pending" | "applying" | "ready" | "failed" | "stopped";
  connection: "disabled" | "pending" | "healthy" | "unhealthy";
};

export type LandingView = {
  nodeIds: string[];
  revision: number;
  servers: Array<{ nodeId: string; name: string; status: "pending" | "applying" | "ready" | "failed" | "stopped" | "offline"; inUse: boolean }>;
  candidates: Array<{ nodeId: string; name: string }>;
  proxies: LandingProxyView[];
  latencies: Array<{ nodeId: string; landingNodeId: string; state: "direct" | "unavailable"; latencyMs?: number }>;
};
