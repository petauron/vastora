export type IPQualityService = { name: string; status: string; regionCode?: string; type?: string };
export type IPQualityClassification = { source: string; value: string };
export type IPQualityRiskFactor = { source: string; kind: "Proxy" | "VPN" | "Tor" | "Server" | "Abuser" | "Robot"; value: boolean };
export type IPQualityReport = {
  address: string; version: string; asn?: string; organization?: string; city?: string; timeZone?: string;
  regionCode?: string; regionName?: string; registeredCode?: string; registeredRegion?: string;
  usageTypes?: IPQualityClassification[]; companyTypes?: IPQualityClassification[]; riskFactors?: IPQualityRiskFactor[];
  scores: { source: string; value: string }[];
  services: IPQualityService[];
};
export type IPQualityCheck = {
  agentId: string; id: string; state: "pending" | "running" | "succeeded" | "failed";
  error?: string; report?: IPQualityReport; checkedAt?: string; updatedAt: string; stale: boolean;
};
