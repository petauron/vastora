export type Carrier = "telecom" | "unicom" | "mobile";
export type NetworkMeasurement = { carrier: Carrier; latencyMs: number; jitterMs: number; lossPercent: number };
export type RouteHop = { ttl: number; address?: string; hostname?: string; latencyMs?: number };
export type ReturnRoute = { carrier: Carrier; stopReason: string; hops: RouteHop[] };
export type BandwidthMeasurement = { region: "apac" | "north-america" | "europe"; location: string; direction: "download" | "upload"; state: "completed" | "unavailable" | "failed"; error?: "endpoint_busy" | "probe_failed"; megabitsPerSecond: number; bytes: number; durationSeconds: number };
export type LinkBandwidthMeasurement = { transportState?: "direct"; sourceNodeId: string; landingNodeId: string; uploadMbps: number; downloadMbps: number; uploadBytes: number; downloadBytes: number; uploadSeconds: number; downloadSeconds: number };
export type ParameterRecommendation = { parameter: string; current: string; value: string; reason: "available_low_latency_control" | "available_pacing_queue" | "avoid_idle_restart" | "requires_path_measurement" | "preserve_current" };
export type OSReinstallNetwork = { interface: string; mac: string; addresses: { prefix: string; dynamic: boolean }[]; gateways: { family: string; address?: string; dynamic?: boolean; onLink?: boolean }[]; dns: string[]; additional?: OSReinstallNetwork[] };
export type OSReinstallInspection = { virtualization: string; architecture: string; bootMode?: string; networkCandidate?: string; missingPrerequisites?: string[]; disk: { path: string; bytes: number; majorMinor: string; partitionTableId?: string }; network: OSReinstallNetwork; limitations?: string[] };

export type HostProfile = { osReinstall?: OSReinstallInspection; osReinstallError?: string; cpuCount: number; memoryBytes: number; diskBytes: number; kernel: string; architecture: string; congestionControl?: string; defaultQdisc?: string; tcpRmem?: string; tcpWmem?: string; tcpSlowStartAfterIdle?: string; persistentConfig: boolean; recommendations: ParameterRecommendation[] };
export type NodeDiagnosticCheck = {
  agentId: string;
  kind: "node.network-quality" | "node.return-route" | "node.international-bandwidth" | "node.host-profile" | "meridian.link-bandwidth" | "meridian.link-bandwidth-server";
  id: string;
  state: "pending" | "running" | "succeeded" | "failed";
  error?: string;
  targetRevision: number;
  network?: NetworkMeasurement[];
  routes?: ReturnRoute[];
  bandwidth?: BandwidthMeasurement[];
  link?: LinkBandwidthMeasurement;
  landingNodeId?: string;
  host?: HostProfile;
  checkedAt?: string;
  updatedAt: string;
};
