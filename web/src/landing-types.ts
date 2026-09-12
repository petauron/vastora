export type LandingProxyView = {
  applicationId: string;
  landingNodeId: string;
  revision: number;
  enabled: boolean;
  status: "pending" | "applying" | "ready" | "failed" | "stopped";
  connection: "disabled" | "pending" | "healthy" | "unhealthy";
  applied?: { revision: number; landingNodeId: string };
};

export type LandingLatencyView = {
  nodeId: string;
  landingNodeId: string;
  state: "direct" | "unavailable";
  latencyMs?: number;
  checkedAt: string;
};

export type LandingLatencyEvent = {
  revision: number;
  reset: boolean;
  upserts: LandingLatencyView[];
  removed: Array<Pick<LandingLatencyView, "nodeId" | "landingNodeId">>;
};

export type LandingLatencySnapshot = { revision: number; samples: LandingLatencyView[] };

export type LandingView = {
  nodeIds: string[];
  revision: number;
  servers: Array<{ nodeId: string; name: string; status: "pending" | "applying" | "ready" | "failed" | "stopped" | "offline"; inUse: boolean }>;
  candidates: Array<{ nodeId: string; name: string }>;
  proxies: LandingProxyView[];
  latencies: LandingLatencyView[];
};

export type LandingPublishingMode = "fixed" | "advanced" | "both";
export type LandingClientMode = { parentId: string; mode: LandingPublishingMode; revision: number };
export type LandingClientGrant = {
  id: string;
  parentId: string;
  applicationId: string;
  serviceId: string;
  landingNodeId: string;
  mode: LandingPublishingMode;
  enabled: boolean;
  revision: number;
  appliedRevision: number;
  status: "preparing" | "prepared" | "configuring" | "activating" | "ready" | "paused" | "revoking" | "revoked" | "failed";
  error?: string;
};
export type LandingClientGrantInput = Pick<LandingClientGrant, "parentId" | "serviceId" | "landingNodeId" | "mode" | "enabled" | "revision"> & { confirmSessionReset: boolean };
