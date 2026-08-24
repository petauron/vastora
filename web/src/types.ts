export type LocalizedText = { en: string; "zh-CN": string };

export type CatalogSource = {
  id: string;
  displayName: string;
  url: string;
  publicKey: string;
  customCASet: boolean;
  bearerTokenSet: boolean;
  enabled: boolean;
  refreshIntervalSeconds: number;
  fetchedAt?: string;
  lastError?: string;
};

export type CenterStatus = {
  version: string;
  agentInstallerAvailable: boolean;
  agentConnectionMode: AgentConnectionMode;
  agentConnectUrl: string;
};

export type Screen = "home" | "nodes" | "apps" | "network" | "activity" | "settings";

export type AppData = {
  status: CenterStatus;
  sources: CatalogSource[];
  apps: AppView[];
  agents: AgentView[];
  deployments: Deployment[];
  organizations: Organization[];
  sites: Site[];
  applications: Application[];
  services: Service[];
  publications: Publication[];
  routes: Route[];
  integrations: Integration[];
  actions: Action[];
  threeXUIControllerMigrations: ThreeXUIControllerMigration[];
};

export type AgentConnectionMode = "lan" | "headscale" | "public";
export type SetupStatus = {
  administratorConfigured: boolean;
  onboardingComplete: boolean;
  suggestedAgentConnectUrl: string;
  builtinHeadscaleAvailable: boolean;
  cloudflareOAuthAvailable: boolean;
  cloudflareConfigured: boolean;
  cloudflareZone?: string;
  publicAddressCandidates: NetworkCandidate[];
};
export type SiteInput = { name: string; code: string; description: string; timezone: string; domainSuffix: string; gatewayNodes: string[] };
export type InitialSetupInput = {
  site: SiteInput;
  network: { agentConnectionMode: AgentConnectionMode; agentConnectUrl: string };
  headscale?: { mode: "builtin" | "external"; url: string; apiKey?: string };
};

export type DiagnosticCount = { total: number; healthy: number; warning: number; failed: number; disabled?: number };
export type Diagnostics = {
  generatedAt: string;
  version: string;
  schema: number;
  nodes: DiagnosticCount;
  applications: DiagnosticCount;
  publications: DiagnosticCount;
  integrations: Integration[];
  recentErrors: Action[];
};

export type AgentView = {
  id: string;
  name: string;
  version: string;
  operatingSystem: "linux";
  architecture: "amd64" | "arm64";
  status: "active" | "disabled";
  appliedInstallations: number;
  enrolledAt: string;
  lastSeenAt: string;
  connected: boolean;
  siteId: string;
  roles: string[];
  capabilities: { docker: boolean; gateway: boolean; tunnel: boolean; metrics: boolean; logs: boolean };
  networkCandidates: NetworkCandidate[];
  networkProfile?: NetworkProfile;
  gatewayHealthy: boolean;
};

export type AgentEnrollment = { token: string; siteId: string; installerUrl: string; expiresAt: string };

export type NetworkKind = "lan" | "headscale" | "public";
export type NetworkCandidate = { address: string; interface: string; family: "ipv4" | "ipv6"; kind: NetworkKind; observedAt: string };
export type NetworkProfile = {
  serviceAddress: string;
  lanAddress?: string;
  headscaleAddress?: string;
  publicAddress?: string;
  enabledKinds: NetworkKind[];
  directPublic: boolean;
  confirmedAt?: string;
  candidateObservedAt?: string;
};

export type AppView = {
  key: string;
  sourceId: string;
  fetchedAt: string;
  app: {
    id: string;
    version: string;
    name: LocalizedText;
    description: LocalizedText;
    hostAccess?: boolean;
    homepage?: { service: string; path: string };
    services?: Array<{ name: string; protocol: "http" | "https"; containerPort: number; defaultHostPort?: number; hostPortField?: string; healthPath?: string; management?: boolean }>;
    config: Array<{
      key: string;
      type: "string" | "boolean" | "integer";
      label: LocalizedText;
      description: LocalizedText;
      required: boolean;
      secret: boolean;
      default?: string | boolean | number;
    }>;
  };
};

export type Organization = { id: string; name: string; createdAt: string; updatedAt: string };
export type Site = { id: string; organizationId: string; name: string; code: string; description: string; timezone: string; domainSuffix: string; status: string; gatewayNodes: string[]; gatewayStatus: string; createdAt: string; updatedAt: string };
export type ThreeXUIRole = "master" | "worker";
export type ThreeXUIBackup = { applicationId: string; revision: number; state: "pending" | "ready" | "failed"; sha256?: string; size: number; lastError?: string; updatedAt: string };
export type ThreeXUIControllerMigration = { id: string; siteId: string; sourceApplicationId: string; targetApplicationId: string; backupRevision: number; state: "backing_up" | "restoring" | "switching" | "ready" | "failed"; step: "backup" | "restore" | "cleanup" | "switch" | "complete"; lastError?: string; backup?: ThreeXUIBackup; createdAt: string; updatedAt: string };
export type Application = { id: string; name: string; nodeId: string; siteId: string; appKey: string; image: string; status: string; runtime: string; role?: ThreeXUIRole; controllerApplicationId?: string; nodeSyncStatus?: "pending" | "applying" | "ready" | "failed" | "stopped"; nodeSyncError?: string; restorePointState?: "pending" | "ready" | "failed"; restorePointAt?: string; installedVersion?: string; availableVersion?: string; updateAvailable: boolean; createdAt: string; updatedAt: string };
export type Service = { id: string; applicationId: string; siteId: string; name: string; displayName?: string; regionCode?: string; protocol: "http" | "https" | "tcp" | "udp"; containerPort: number; hostPort: number; endpoint: string; source: "catalog" | "observed"; appProtocol?: string; management: boolean; observedListen?: string; status: string; lastError?: string; createdAt: string; updatedAt: string };
export type Region = { code: string; nameZh: string; prefix: string };
export type RegionSuggestion = { agentId: string; publicAddress: string; regionCode: string; prefix: string; source: string };
export type PublicationKind = "lan_gateway" | "headscale_gateway" | "public_direct" | "public_shared_443" | "cloudflare_tunnel";
export type DNSRecordInstruction = { type: "A" | "AAAA" | "CNAME"; name: string; value: string; proxy: boolean };
export type Publication = { id: string; serviceId: string; kind: PublicationKind; gatewayNodeId?: string; hostname: string; sniHostname?: string; dnsProvider: "manual" | "cloudflare" | "headscale"; dnsRecordId?: string; dnsRecord?: DNSRecordInstruction; tlsEnabled: boolean; certificateExpiresAt?: string; desiredRevision: number; appliedRevision: number; status: "pending" | "applying" | "ready" | "degraded" | "failed" | "stopped"; lastError?: string; accessUrl?: string; createdAt: string; updatedAt: string };
export type Route = { id: string; publicationId: string; siteId: string; serviceId: string; gatewayNodeId: string; hostname: string; protocol: string; upstreams: string[]; tlsEnabled: boolean; status: string; desiredRevision: number; appliedRevision: number; lastError?: string; createdAt: string; updatedAt: string };
export type Integration = { kind: "headscale" | "cloudflare"; mode?: "builtin" | "external" | "oauth"; endpoint?: string; accountId?: string; zoneId?: string; secretSet: boolean; status: "configured" | "failed" | "disabled"; lastError?: string; updatedAt?: string };
export type CloudflareZone = { id: string; name: string; accountId: string; accountName: string };
export type CloudflareOAuthStart = { sessionId: string; authorizationUrl: string; expiresAt: string };
export type CloudflareOAuthPoll = { status: "pending" | "authorized"; zones?: CloudflareZone[] };
export type HeadscaleJoin = { agentId: string; command: string; expiresAt: string };
export type Action = { id: string; taskId: string; agentId: string; kind: string; revision: number; event: "queued" | "claimed" | "lease_expired" | "succeeded" | "failed"; message?: string; createdAt: string };
export type ApplicationCommandKind = "3xui.reality.create" | "3xui.reality.rename" | "3xui.subscription.configure" | "3xui.clients.manage" | "3xui.node.reconcile" | "3xui.controller.manage";
export type ThreeXUIClientInbound = { id: number; serviceId?: string; name: string; displayName?: string; applicationId?: string; nodeId?: string; nodeName?: string; connectHostname?: string; sniHostname?: string; enabled?: boolean; totalBytes?: number; usedBytes?: number; resetDays?: number; nextResetAt?: string; planStatus?: "active" | "resetting" | "failed"; planError?: string };
export type ThreeXUIClient = { email: string; enabled: boolean; totalBytes: number; usedBytes: number; expiryTime: number; resetDays?: number; limitIp: number; inboundIds: number[]; hasSubscription: boolean };
export type ThreeXUIClientAction = "list" | "list_inbounds" | "create" | "update" | "update_inbound" | "set_enabled" | "delete" | "reset_traffic" | "reveal_link" | "reveal_subscription";
export type ThreeXUIClientCommandInput = { applicationId: string; action: ThreeXUIClientAction; serviceId?: string; email?: string; newEmail?: string; inboundId?: number; inboundIds?: number[]; enabled?: boolean; totalBytes?: number; expiryTime?: number; resetDays?: number; limitIp?: number; inboundTotalBytes?: number; inboundResetDays?: number };
export type ApplicationCommand = { id: string; applicationId: string; gatewayNodeId: string; kind: ApplicationCommandKind; state: "pending" | "running" | "succeeded" | "failed"; reconciliationRequired?: boolean; hostname: string; dnsProvider: "manual" | "cloudflare"; target?: string; sniHostname?: string; publicationId?: string; action?: ThreeXUIClientAction | "rename" | "create"; regionCode?: string; displayName?: string; inboundId?: number; inboundTotalBytes?: number; inboundUsedBytes?: number; inboundResetDays?: number; inboundNextResetAt?: string; clientCreated?: boolean; clients?: ThreeXUIClient[]; clientsObserved?: boolean; inbounds?: ThreeXUIClientInbound[]; inboundsObserved?: boolean; subscriptionAvailable?: boolean; error?: string; resultAvailable: boolean; createdAt: string; updatedAt: string };

export type Deployment = {
  id: string;
  agentId: string;
  appKey: string;
  appVersion: string;
  state: "pending" | "running" | "succeeded" | "failed";
  operation: "install" | "upgrade" | "configure" | "uninstall";
  deleteData: boolean;
  accessUrl?: string;
  error?: string;
  reconciliationRequired?: boolean;
  applicationId?: string;
  oneTimeCredentials?: { username: string; password: string };
  createdAt: string;
  updatedAt: string;
};
