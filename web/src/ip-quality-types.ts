export type IPQualityService = { name: string; status: string; regionCode?: string; type?: string };
export type IPQualityClassification = { source: string; value: string };
export type IPQualityRiskFactor = { source: string; kind: "Proxy" | "VPN" | "Tor" | "Server" | "Abuser" | "Robot"; value: boolean };
export type IPQualityReport = {
	observations?: { source: string; status: "ok" | "missing"; address: string; checkedAt: string }[];
	ippure?: { provider: string; status: "ok" | "unavailable" | "invalid_response" | "ip_mismatch" | "unsupported"; address?: string; checkedAt: string; riskScore?: number; residential?: boolean; broadcast?: boolean };
  address: string; version: string; asn?: string; organization?: string; city?: string; timeZone?: string;
  regionCode?: string; regionName?: string; registeredCode?: string; registeredRegion?: string;
  usageTypes?: IPQualityClassification[]; companyTypes?: IPQualityClassification[]; riskFactors?: IPQualityRiskFactor[];
  scores: { source: string; value: string }[];
  services: IPQualityService[];
};
export type IPQualityCheck = {
	assessment?: IPQualityAssessment;
  agentId: string; id: string; state: "pending" | "running" | "succeeded" | "failed";
  error?: string; report?: IPQualityReport; checkedAt?: string; updatedAt: string; stale: boolean;
};

export type IPQualityPreferences = { requiredServices: string[]; targetRegion: string };
export type IPQualityAssessment = {
  version: string;
  status: "complete" | "partial" | "expired" | "ip_changed";
  score?: number; min: number; max: number;
  grade: "excellent" | "premium" | "good" | "fair" | "poor" | "unknown";
  ipType: "residential" | "mobile" | "business" | "hosting" | "unknown";
  typeCandidates: string[]; typeEvidence: IPQualityClassification[];
  contributions: { id: string; weight: number; min: number; max: number; missing: string[] }[];
  missing: string[]; advice: "direct" | "compare" | "recheck"; reasons: string[];
  requiredFailed: string[]; requiredUnknown: string[]; preferences: IPQualityPreferences;
  validUntil?: string;
};
export type IPQualityComparison = {
  nodeId: string; name: string; compatible: boolean; connectionVerified: boolean; recommended: boolean; reason: string;
  delta?: number; assessment: IPQualityAssessment; services: IPQualityService[];
};
export type IPQualityResponse = { checks: IPQualityCheck[]; comparisons: IPQualityComparison[] };
