export type LandingProxyView = {
  applicationId: string;
  landingNodeId: string;
  revision: number;
  enabled: boolean;
  status: "pending" | "applying" | "ready" | "failed" | "stopped";
  connection: "disabled" | "pending" | "healthy" | "unhealthy";
  applied?: { revision: number; landingNodeId: string };
  peers: Array<{ nodeId: string; healthy: boolean; state: "healthy" | "blocked"; reason: string; checkedAt?: string }>;
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
  tasksPaused?: boolean;
  controllerBlocked?: boolean;
  blockedNodeIds?: string[];
  nodeIds: string[];
  retiringNodeIds?: string[];
  landingRegionCodes?: Record<string, string>;
  revision: number;
  status: "ready" | "applying" | "failed";
  eligibleEntries: number;
  readyCombinations: number;
  failedCombinations: number;
  withheldCombinations: number;
  servers: Array<{ egressAddresses?: Array<{ address: string; interface: string }>; egressIp?: string; egressRevision?: number; egressSupported?: boolean; egressError?: string; nodeId: string; name: string; status: "pending" | "applying" | "ready" | "failed" | "stopped" | "offline" | "draining"; inUse: boolean; eligibleEntries: number; readyCombinations: number; failedCombinations: number; withheldCombinations: number }>;
  candidates: Array<{ nodeId: string; name: string }>;
  proxies: LandingProxyView[];
  latencies: LandingLatencyView[];
};
