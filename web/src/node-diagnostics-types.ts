export type Carrier = "telecom" | "unicom" | "mobile";
export type NetworkMeasurement = { carrier: Carrier; latencyMs: number; jitterMs: number; lossPercent: number };
export type RouteHop = { ttl: number; address?: string; hostname?: string; latencyMs?: number };
export type ReturnRoute = { carrier: Carrier; stopReason: string; hops: RouteHop[] };
export type BandwidthMeasurement = { region: "apac" | "north-america" | "europe"; location: string; direction: "download" | "upload"; state: "completed" | "unavailable" | "failed"; error?: "endpoint_busy" | "probe_failed"; megabitsPerSecond: number; bytes: number; durationSeconds: number };
export type NodeDiagnosticCheck = {
  agentId: string;
  kind: "node.network-quality" | "node.return-route" | "node.international-bandwidth";
  id: string;
  state: "pending" | "running" | "succeeded" | "failed";
  error?: string;
  targetRevision: number;
  network?: NetworkMeasurement[];
  routes?: ReturnRoute[];
  bandwidth?: BandwidthMeasurement[];
  checkedAt?: string;
  updatedAt: string;
};
