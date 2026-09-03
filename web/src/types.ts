export type LocalizedText = { en: string; "zh-CN": string };

export type CatalogSource = {
  id: string;
  displayName: string;
  url: string;
  publicKey: string;
  customCASet: boolean;
  bearerTokenSet: boolean;
  enabled: boolean;
  status: "pending" | "healthy" | "stale" | "failed" | "disabled";
  refreshIntervalSeconds: number;
  fetchedAt?: string;
  checkedAt?: string;
  lastError?: string;
};

export type CenterStatus = {
  version: string;
  agentInstallerAvailable: boolean;
  agentConnectionMode: AgentConnectionMode;
  agentConnectUrl: string;
};

export type Screen = "home" | "nodes" | "apps" | "network" | "activity" | "assistant" | "settings";

export type AppData = {
  status: CenterStatus;
  centerUpdate: CenterUpdateStatus;
  sources: CatalogSource[];
  apps: AppView[];
  registryCredentials: RegistryCredential[];
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
  systemDomain: SystemDomain;
  tailscaleFixedEndpoint?: TailscaleFixedEndpoint;
  centerRemoteAccess: CenterRemoteAccess | null;
  centerRemoteAccessError?: string;
};

export type RegistryCredential = {
  id: string;
  host: string;
  username: string;
  tokenSet: boolean;
  createdAt: string;
};

export type SystemEndpointAlias = {
  kind: "center" | "headscale";
  endpoint: string;
  transitionId: string;
  lifecycleState: "active" | "retiring" | "failed";
  retireAfter: string;
  certificateNotAfter?: string;
  lastError?: string;
};
export type SystemDomain = {
  namespace: string;
  centerUrl: string;
  headscaleUrl: string;
  cloudflareZone: string;
  aliases: SystemEndpointAlias[];
  activePublications: number;
  pendingCleanup: number;
  builtinHeadscale: boolean;
  cloudflareOAuthAvailable: boolean;
};
export type SystemDomainSwitchResult = SystemDomain & {
  previousCenterUrl: string;
  previousHeadscaleUrl: string;
  backupCreated: boolean;
};

export type CenterUpdateStatus = {
  currentVersion: string;
  latestVersion?: string;
  updateAvailable: boolean;
  releaseCheckAvailable: boolean;
  automatic: boolean;
  state: "idle" | "queued" | "applying" | "succeeded" | "failed";
  targetVersion?: string;
  phase?: "queued" | "downloading" | "verifying" | "installing" | "validating" | "pulling" | "agent" | "restarting" | "health" | "reconciling" | "finalizing" | "agents" | "completed";
  progress?: number;
  message?: string;
  checkedAt?: string;
  updatedAt?: string;
  error?: string;
  agentRollout?: {
    targetVersion: string;
    total: number;
    updated: number;
    updating: number;
    pending: number;
    failed: number;
    offline: number;
    manual: number;
  };
};

export type AssistantProvider = {
  apiUrl: string;
  model: string;
  apiKeySet: boolean;
  allowPrivate: boolean;
  status: "disabled" | "configured" | "verified" | "failed";
  lastError?: string;
  updatedAt?: string;
};

export type AssistantMessage = {
  id: string;
  runId?: string;
  role: "user" | "assistant" | "system";
  content: string;
  createdAt: string;
};

export type AssistantRun = {
  id: string;
  conversationId: string;
  status: "queued" | "running" | "completed" | "failed" | "cancelled" | "approval_required";
  lastError?: string;
  createdAt: string;
  updatedAt: string;
};

export type AssistantProposal = {
  id: string;
  conversationId: string;
  runId: string;
	kind: "install_application" | "rotate_cpa_credential";
  summary: {
    action?: string;
    agentId?: string;
    agentName?: string;
    appKey?: string;
    appName?: LocalizedText;
    version?: string;
		role?: string;
		credentialTarget?: "management" | "client";
		impact?: string;
    dataRetention?: string;
  };
  digest: string;
  targets: Array<{ kind: string; id: string }>;
  expectedRevision: string;
  policyVersion: string;
  risk: "low" | "medium" | "high";
  status: "pending" | "approved" | "rejected" | "expired" | "applied" | "cancelled";
  expiresAt: string;
	deploymentId?: string;
	executionId?: string;
	createdAt: string;
	updatedAt: string;
};

export type AssistantExecution = {
	id: string;
	kind: AssistantProposal["kind"];
	state: string;
};

export type AssistantConversation = {
  id: string;
  title: string;
  messages: AssistantMessage[];
  runs: AssistantRun[];
  proposals: AssistantProposal[];
  createdAt: string;
  updatedAt: string;
};

export type AgentConnectionMode = "lan" | "headscale" | "public";
export type SetupStatus = {
  administratorConfigured: boolean;
  onboardingComplete: boolean;
  suggestedAgentConnectUrl: string;
  builtinHeadscaleAvailable: boolean;
  cloudflareOAuthAvailable: boolean;
  publicNetworkHelperAvailable: boolean;
  regionLookupAvailable: boolean;
  cloudflareConfigured: boolean;
  cloudflareAccessConfigured: boolean;
  cloudflareTurnstileConfigured: boolean;
  cloudflareZone?: string;
  publicAddressCandidates: NetworkCandidate[];
  gatewayAddressCandidates: NetworkCandidate[];
  observedPublicAddress?: string;
  suggestedGatewayAddress?: string;
  publicAddressDetection?: "direct" | "cloud_mapping_candidate" | "unavailable";
  loginProtection: {
    captchaRequired: boolean;
    turnstileSiteKey?: string;
    maxFailures: number;
    lockoutSeconds: number;
  };
};
export type SiteInput = { name: string; code: string; description: string; timezone: string; domainSuffix: string; gatewayNodes: string[] };
export type InitialSetupInput = {
  site: SiteInput;
  network: { agentConnectionMode: AgentConnectionMode; agentConnectUrl: string };
  headscale?: { mode: "builtin" | "external"; url: string; apiKey?: string; dnsPolicy?: "system" | "custom"; dnsResolvers?: string[] };
  tailscaleFixedEndpoint?: TailscaleFixedEndpointInput;
  centerRemoteAccess?: CenterRemoteAccessInput;
};

export type CenterRemoteAccessInput = {
  enabled: boolean;
  protectionMode?: "access" | "native";
  audienceKind?: "email" | "email_domain";
  audienceValue?: string;
};

export type CenterRemoteAccess = {
  available: boolean;
  enabled: boolean;
  hostname?: string;
  protectionMode?: "access" | "native";
  turnstileSiteKey?: string;
  audienceKind?: "email" | "email_domain";
  audienceValue?: string;
  status: "disabled" | "pending" | "configured" | "failed";
  lastError?: string;
  updatedAt?: string;
};

export type TailscaleFixedEndpointInput = {
  enabled: boolean;
  endpoint: string;
  localAddress: string;
  confirmMapping: boolean;
};

export type TailscaleFixedEndpoint = {
  available: boolean;
  enabled: boolean;
  endpoint: string;
  localAddress: string;
  detectedEndpoint: string;
  detectedLocalAddress: string;
  localAddressCandidates: NetworkCandidate[];
  status: "unavailable" | "disabled" | "configured" | "action_required";
  lastError?: string;
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
  publicEgress?: PublicEgress;
  networkProfile?: NetworkProfile;
  gatewayHealthy: boolean;
  tailscaleOwnership?: "managed" | "external" | "";
  remoteUpdateSupported: boolean;
  update?: AgentUpdate;
};

export type AgentUpdate = {
  id: string;
  targetVersion: string;
  state: "pending" | "running" | "installing" | "succeeded" | "failed";
  lastError?: string;
  updatedAt: string;
};

export type AgentEnrollment = { token: string; siteId: string; centerUrl?: string; installerUrl: string; caCertificatePem?: string; expiresAt: string };

export type NetworkKind = "lan" | "headscale" | "public";
export type NetworkCandidate = { address: string; interface: string; kind: NetworkKind; observedAt: string };
export type PublicEgress = { address: string; bindAddress: string; mode: "direct" | "nat"; observedAt: string };
export type NetworkProfile = {
  serviceAddress: string;
  lanAddress?: string;
  headscaleAddress?: string;
  publicAddress?: string;
  publicBindAddress?: string;
  publicMode?: "direct" | "nat";
  enabledKinds: NetworkKind[];
  directPublic: boolean;
  publicVerifiedAt?: string;
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
    artifacts?: Array<{ name: string; operatingSystem: "linux"; architecture: "amd64" | "arm64"; url: string; sha256: string }>;
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
export type ThreeXUIControllerMigration = { id: string; kind: "replace" | "consolidate"; siteId: string; sourceApplicationId: string; targetApplicationId: string; backupRevision: number; state: "backing_up" | "restoring" | "switching" | "ready" | "failed"; step: "backup" | "restore" | "cleanup" | "switch" | "complete"; lastError?: string; failedWorkerApplicationId?: string; backup?: ThreeXUIBackup; createdAt: string; updatedAt: string };
export type Application = { id: string; name: string; nodeId: string; siteId: string; appKey: string; image: string; status: string; runtime: string; role?: ThreeXUIRole; controllerApplicationId?: string; nodeSyncStatus?: "pending" | "applying" | "ready" | "failed" | "stopped"; nodeSyncError?: string; restorePointState?: "pending" | "ready" | "failed"; restorePointAt?: string; installedVersion?: string; availableVersion?: string; updateAvailable: boolean; createdAt: string; updatedAt: string };
export type ApplicationCredentials = { kind: "three_x_ui"; username: string; password: string } | { kind: "cpa"; managementKey: string; clientApiKey: string };
export type ApplicationCredentialRotation = { id: string; applicationId: string; target: "management" | "client"; state: "preparing" | "pending" | "succeeded" | "failed" | "action_required"; cpaDeploymentId?: string; keeperDeploymentId?: string; lastError?: string; createdAt: string; updatedAt: string };
export type Service = { id: string; applicationId: string; siteId: string; name: string; displayName?: string; regionCode?: string; protocol: "http" | "https" | "tcp" | "udp"; containerPort: number; hostPort: number; endpoint: string; source: "catalog" | "observed"; appProtocol?: string; management: boolean; observedListen?: string; status: string; lastError?: string; guardStatus?: "pending" | "hardening" | "ready" | "action_required"; guardSummary?: string; actionRequiredReason?: string; createdAt: string; updatedAt: string };
export type Region = { code: string; nameZh: string; prefix: string };
export type RegionSuggestion = { agentId: string; publicAddress: string; regionCode: string; prefix: string; source: string };
export type PublicationKind = "lan_gateway" | "headscale_gateway" | "public_direct" | "public_shared_443" | "cloudflare_tunnel";
export type PublicationIngress =
  | { owner: "site_gateway"; entryNodeId: string }
  | { owner: "application_node"; entryNodeId: string }
  | { owner: "tunnel_connector"; entryNodeId: string };
export type PublicationIngressInput =
  | { owner: "site_gateway"; entryNodeId: string }
  | { owner: "application_node" }
  | { owner: "tunnel_connector"; entryNodeId: string };
export type CreatePublicationInput = { serviceId: string; kind: PublicationKind; ingress: PublicationIngressInput; hostname?: string; sniHostname?: string; dnsProvider: "manual" | "cloudflare" | "headscale"; tlsEnabled?: boolean; confirmHighRisk?: boolean };
export type DNSRecordInstruction = { type: "A" | "CNAME"; name: string; value: string; proxy: boolean };
export type Publication = { id: string; serviceId: string; kind: PublicationKind; ingress: PublicationIngress; hostname: string; sniHostname?: string; dnsProvider: "manual" | "cloudflare" | "headscale"; dnsRecordId?: string; dnsRecord?: DNSRecordInstruction; tlsEnabled: boolean; certificateExpiresAt?: string; desiredRevision: number; appliedRevision: number; status: "pending" | "applying" | "ready" | "degraded" | "failed" | "stopped"; lastError?: string; actionRequired?: boolean; accessUrl?: string; createdAt: string; updatedAt: string };
export type Route = { id: string; publicationId: string; siteId: string; serviceId: string; gatewayNodeId: string; hostname: string; protocol: string; upstreams: string[]; tlsEnabled: boolean; status: string; desiredRevision: number; appliedRevision: number; lastError?: string; createdAt: string; updatedAt: string };
export type Integration = { kind: "headscale" | "cloudflare"; mode?: "builtin" | "external" | "oauth"; endpoint?: string; accountId?: string; zoneId?: string; secretSet: boolean; accessManagement?: boolean; turnstileManagement?: boolean; status: "configured" | "failed" | "disabled"; lastError?: string; updatedAt?: string; credentialStatus?: "ready" | "preparing" | "committing"; credentialExpiresAt?: string; dnsPolicy?: "system" | "custom"; dnsResolvers?: string[] };
export type CloudflareZone = { id: string; name: string; accountId: string; accountName: string };
export type CloudflareOAuthStart = { sessionId: string; authorizationUrl: string; expiresAt: string };
export type CloudflareOAuthPoll = { status: "pending" | "authorized"; zones?: CloudflareZone[] };
export type HeadscaleJoin = { agentId: string; command: string; expiresAt: string };
export type Action = { id: string; taskId: string; agentId: string; kind: string; revision: number; event: "queued" | "claimed" | "lease_expired" | "succeeded" | "failed"; message?: string; createdAt: string };
export type ApplicationCommandKind = "3xui.reality.create" | "3xui.reality.verify" | "3xui.reality.harden" | "3xui.reality.rename" | "3xui.subscription.configure" | "3xui.clients.manage" | "3xui.node.reconcile" | "3xui.controller.manage";
export type ThreeXUIClientInbound = { id: number; serviceId?: string; name: string; displayName?: string; applicationId?: string; nodeId?: string; nodeName?: string; connectHostname?: string; sniHostname?: string; enabled?: boolean; totalBytes?: number; usedBytes?: number; resetDay?: number; nextResetAt?: string; planStatus?: "active" | "resetting" | "failed"; planError?: string };
export type ThreeXUIClient = { email: string; enabled: boolean; totalBytes: number; usedBytes: number; expiryTime: number; resetDays?: number; limitIp: number; inboundIds: number[]; hasSubscription: boolean };
export type ThreeXUIClientAction = "list" | "list_inbounds" | "create" | "update" | "update_inbound" | "set_enabled" | "delete" | "reset_traffic" | "reveal_link" | "reveal_subscription";
export type ThreeXUIClientCommandInput = { applicationId: string; action: ThreeXUIClientAction; serviceId?: string; email?: string; newEmail?: string; inboundId?: number; inboundIds?: number[]; enabled?: boolean; totalBytes?: number; expiryTime?: number; resetDays?: number; limitIp?: number; inboundTotalBytes?: number; inboundResetDay?: number };
export type ApplicationCommand = { id: string; applicationId: string; gatewayNodeId: string; kind: ApplicationCommandKind; state: "pending" | "running" | "succeeded" | "failed"; reconciliationRequired?: boolean; hostname: string; dnsProvider: "manual" | "cloudflare"; targetHost?: string; targetIp?: string; serverName?: string; nodeAsn?: number; targetAsn?: number; cdnProvider?: string; tls13?: boolean; x25519?: boolean; h2?: boolean; certificateValid?: boolean; guardStatus?: "pending" | "hardening" | "ready" | "action_required"; publicationId?: string; action?: ThreeXUIClientAction | "rename" | "create" | "verify" | "harden"; regionCode?: string; displayName?: string; inboundId?: number; inboundTotalBytes?: number; inboundUsedBytes?: number; inboundResetDay?: number; inboundNextResetAt?: string; clientCreated?: boolean; clients?: ThreeXUIClient[]; clientsObserved?: boolean; inbounds?: ThreeXUIClientInbound[]; inboundsObserved?: boolean; subscriptionAvailable?: boolean; error?: string; resultAvailable: boolean; createdAt: string; updatedAt: string };

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
  oneTimeCredentialsAvailable?: boolean;
  createdAt: string;
  updatedAt: string;
};
