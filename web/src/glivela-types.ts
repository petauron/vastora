export type GlivelaCutover = {
  state: "not_required" | "inspect" | "backup" | "import" | "publish" | "project" | "verify" | "retire" | "complete" | "failed";
  subscriptionAuthority: "legacy" | "meridian";
  legacyControllerApplicationId?: string;
  legacyControllerName?: string;
  importSha256?: string;
  backupRevision?: number;
  expectedAccounts: number;
  importedAccounts: number;
  expectedCredentials: number;
  importedCredentials: number;
  expectedEndpoints: number;
  readyEndpoints: number;
  retiredEndpoints: number;
  expectedRoutes: number;
  readyRoutes: number;
  blockedRoutes: number;
  pendingDeployments: number;
  failedDeployments: number;
  lastError?: string;
  updatedAt: string;
  switchedAt?: string;
  complete: boolean;
};

export type GlivelaSourceRecovery = {
  previousFingerprint: string;
  currentFingerprint: string;
  previousAddress: string;
  currentAddress: string;
  endpointRevision: number;
  observedAt: string;
};

export type GlivelaEndpoint = {
  id: string;
  applicationId: string;
  applicationName: string;
  displayName: string;
  regionCode?: string;
  nodeId: string;
  serviceId: string;
  advertiseHost: string;
  advertisePort: number;
  target: string;
  serverNames: string[];
  publicKey: string;
  shortIds: string[];
  fingerprint: string;
  vless: boolean;
  hy2: boolean;
  hy2ServerName?: string;
  desiredRevision: number;
  appliedRevision: number;
  runtimeHealthy: boolean;
  legacyRetired: boolean;
  status: "importing" | "pending" | "applying" | "ready" | "failed" | "retired";
  lastError?: string;
  updatedAt: string;
};

export type GlivelaAccount = {
  id: string;
  displayName: string;
  totalBytes: number;
  usedBytes: number;
  remainingBytes: number;
  expiryTime: number;
  resetDays: number;
  nextResetAt?: string;
  lastResetAt?: string;
  enabled: boolean;
  desiredRevision: number;
  appliedRevision: number;
  status: "importing" | "active" | "disabled" | "expired" | "failed";
  lastError?: string;
  credentialCount: number;
  routeCount: number;
  subscriptionPath?: string;
  updatedAt: string;
};

export type GlivelaRouteGrant = {
  id: string;
  accountId: string;
  endpointId: string;
  egressNodeId: string;
  egressNodeName: string;
  ipv4EgressNodeId?: string;
  ipv4EgressNodeName?: string;
  hideNative: boolean;
  enabled: boolean;
  desiredRevision: number;
  appliedRevision: number;
  runtimeHealthy: boolean;
  status: "importing" | "pending" | "applying" | "ready" | "blocked" | "revoking" | "failed";
  lastError?: string;
  updatedAt: string;
};

export type GlivelaInventory = {
  cutover: GlivelaCutover;
  endpoints: GlivelaEndpoint[];
  accounts: GlivelaAccount[];
  grants: GlivelaRouteGrant[];
};

export type GlivelaAccountInput = {
  displayName: string;
  totalBytes: number;
  expiryTime: number;
  resetDays: number;
  enabled?: boolean;
};

export type GlivelaAccountCreated = {
  account: GlivelaAccount;
  subscriptionToken: string;
  subscriptionPath: string;
};

export type GlivelaLineUsage = {
 entryNodeId: string; entryName: string; egressNodeId?: string; egressName?: string;
 uploadBytes: number; downloadBytes: number; totalBytes: number; credentialCount: number; trackedCredentials: number;
 startedAt?: string; observedAt?: string; state: "current" | "missing" | "partial" | "stale";
};
export type GlivelaTraffic = { lines: GlivelaLineUsage[]; period: "since_tracking_started"; checkedAt: string };
