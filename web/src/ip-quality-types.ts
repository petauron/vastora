export type IPQualityService = { name: string; status: string; regionCode?: string; type?: string };
export type IPQualityReport = {
  address: string; version: string; regionCode?: string;
  scores: { source: string; value: string }[];
  services: IPQualityService[];
};
export type IPQualityCheck = {
  agentId: string; id: string; state: "pending" | "running" | "succeeded" | "failed";
  error?: string; report?: IPQualityReport; checkedAt?: string; updatedAt: string; stale: boolean;
};
