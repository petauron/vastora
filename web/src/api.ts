import type { Action, AgentEnrollment, AgentUpdate, AgentView, ApplicationCommand, ApplicationCommandKind, AppView, Application, AssistantConversation, AssistantProvider, AssistantProposal, AssistantRun, CatalogSource, CenterRemoteAccess, CenterRemoteAccessInput, CloudflareOAuthPoll, CloudflareOAuthStart, CloudflareZone, CenterStatus, CenterUpdateStatus, Deployment, Diagnostics, HeadscaleJoin, InitialSetupInput, Integration, NetworkProfile, Organization, Publication, PublicationKind, Region, RegionSuggestion, RegistryCredential, Route, Service, SetupStatus, Site, SiteInput, SystemDomain, SystemDomainSwitchResult, TailscaleFixedEndpoint, TailscaleFixedEndpointInput, ThreeXUIClientCommandInput, ThreeXUIControllerMigration } from "./types";

export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code = "request_failed"
  ) {
    super(message);
  }
}

function csrfToken(): string {
  return document.cookie
    .split("; ")
    .find((value) => value.startsWith("vastora_csrf="))
    ?.split("=")[1] ?? "";
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body) {
    headers.set("Content-Type", "application/json");
    headers.set("X-CSRF-Token", csrfToken());
  }
  const response = await fetch(path, { ...init, headers, credentials: "same-origin" });
  const body = (await response.json().catch(() => ({}))) as T & { error?: string; code?: string };
  if (!response.ok) {
    throw new APIError(body.error ?? "Request failed", response.status, body.code);
  }
  return body;
}

async function download(path: string, fallbackName: string, init: RequestInit = {}): Promise<void> {
  const headers = new Headers(init.headers);
  if (init.body) {
    headers.set("Content-Type", "application/json");
    headers.set("X-CSRF-Token", csrfToken());
  }
  const response = await fetch(path, { ...init, headers, credentials: "same-origin" });
  if (!response.ok) {
    const body = await response.json().catch(() => ({})) as { error?: string; code?: string };
    throw new APIError(body.error ?? "Request failed", response.status, body.code);
  }
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const match = disposition.match(/filename="?([^";]+)"?/i);
  const objectURL = URL.createObjectURL(await response.blob());
  const anchor = document.createElement("a");
  anchor.href = objectURL;
  anchor.download = match?.[1] ?? fallbackName;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(objectURL);
}

export const api = {
  setupStatus: () => request<SetupStatus>("/api/v1/setup/status"),
  setupAdmin: (username: string, password: string) =>
    request<{ administratorConfigured: boolean }>("/api/v1/setup/admin", {
      method: "POST",
      body: JSON.stringify({ username, password })
    }),
  completeSetup: (input: InitialSetupInput) => request<{ site: Site; network: InitialSetupInput["network"] }>("/api/v1/setup/complete", { method: "POST", body: JSON.stringify(input) }),
  login: (username: string, password: string) =>
    request<{ authenticated: boolean }>("/api/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password })
    }),
  logout: () => request<{ authenticated: boolean }>("/api/v1/auth/logout", { method: "POST", body: "{}" }),
  changePassword: (currentPassword: string, newPassword: string) => request<{ changed: boolean }>("/api/v1/auth/password", { method: "PUT", body: JSON.stringify({ currentPassword, newPassword }) }),
  assistantProvider: (signal?: AbortSignal) => request<AssistantProvider>("/api/v1/assistant/provider", { signal }),
  saveAssistantProvider: (input: { apiUrl: string; apiKey: string; model: string; allowPrivate: boolean }) => request<AssistantProvider>("/api/v1/assistant/provider", { method: "PUT", body: JSON.stringify(input) }),
  validateAssistantProvider: () => request<AssistantProvider>("/api/v1/assistant/provider/validate", { method: "POST", body: "{}" }),
  assistantConversations: (signal?: AbortSignal) => request<{ conversations: AssistantConversation[] }>("/api/v1/assistant/conversations", { signal }),
  createAssistantConversation: (title: string) => request<AssistantConversation>("/api/v1/assistant/conversations", { method: "POST", body: JSON.stringify({ title }) }),
  assistantConversation: (id: string, signal?: AbortSignal) => request<AssistantConversation>(`/api/v1/assistant/conversations/${encodeURIComponent(id)}`, { signal }),
  createAssistantMessage: (id: string, content: string) => request<AssistantRun>(`/api/v1/assistant/conversations/${encodeURIComponent(id)}/messages`, { method: "POST", body: JSON.stringify({ content }) }),
  cancelAssistantRun: (id: string) => request<{ cancelled: boolean }>(`/api/v1/assistant/runs/${encodeURIComponent(id)}/cancel`, { method: "POST", body: "{}" }),
  decideAssistantProposal: (id: string, decision: "approve" | "reject", digest: string) => request<AssistantProposal>(`/api/v1/assistant/proposals/${encodeURIComponent(id)}/${decision}`, { method: "POST", body: JSON.stringify({ digest }) }),
  applyAssistantProposal: (id: string, digest: string) => request<Deployment>(`/api/v1/assistant/proposals/${encodeURIComponent(id)}/apply`, { method: "POST", body: JSON.stringify({ digest }) }),
  status: (signal?: AbortSignal) => request<CenterStatus>("/api/v1/status", { signal }),
  centerUpdate: (refresh = false, signal?: AbortSignal) => request<CenterUpdateStatus>(`/api/v1/system/update${refresh ? "?refresh=true" : ""}`, { signal }),
  startCenterUpdate: () => request<CenterUpdateStatus>("/api/v1/system/update", { method: "POST", body: "{}" }),
  systemDomain: (signal?: AbortSignal) => request<SystemDomain>("/api/v1/system/domain", { signal }),
  switchSystemDomain: (zoneId: string) => request<SystemDomainSwitchResult>("/api/v1/system/domain", { method: "POST", body: JSON.stringify({ zoneId, confirm: true }) }),
  retireSystemDomainAliases: (transitionId: string) => request<SystemDomain>(`/api/v1/system/domain/aliases/${encodeURIComponent(transitionId)}/retire`, { method: "POST", body: "{}" }),
  diagnostics: () => request<Diagnostics>("/api/v1/diagnostics"),
  downloadDiagnostics: () => download("/api/v1/diagnostics", `vastora-diagnostics-${new Date().toISOString().slice(0, 10)}.json`),
  downloadBackup: (password: string) => download("/api/v1/backups", "vastora-center.vastora", { method: "POST", body: JSON.stringify({ password }) }),
  sources: (signal?: AbortSignal) => request<{ sources: CatalogSource[] }>("/api/v1/catalog/sources", { signal }),
  apps: (signal?: AbortSignal) => request<{ apps: AppView[] }>("/api/v1/catalog/apps", { signal }),
	registryCredentials: (signal?: AbortSignal) => request<{ credentials: RegistryCredential[] }>("/api/v1/registry-credentials", { signal }),
	createRegistryCredential: (host: string, username: string, token: string) => request<RegistryCredential>("/api/v1/registry-credentials", { method: "POST", body: JSON.stringify({ host, username, token }) }),
	rotateRegistryCredential: (id: string, username: string, token: string) => request<RegistryCredential>(`/api/v1/registry-credentials/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify({ username, token }) }),
	deleteRegistryCredential: (id: string) => request<{ deleted: boolean }>(`/api/v1/registry-credentials/${encodeURIComponent(id)}`, { method: "DELETE", body: "{}" }),
	agents: (signal?: AbortSignal) => request<{ agents: AgentView[] }>("/api/v1/agents", { signal }),
	createAgentEnrollment: (siteId: string, name: string, centerUrl: string, useHeadscale: boolean, gateway: boolean, tunnel: boolean, caCertificatePem = "") => request<AgentEnrollment>("/api/v1/agent-enrollments", { method: "POST", body: JSON.stringify({ siteId, name, centerUrl, useHeadscale, gateway, tunnel, caCertificatePem }) }),
  deployments: (signal?: AbortSignal) => request<{ deployments: Deployment[] }>("/api/v1/deployments", { signal }),
	sites: (signal?: AbortSignal) => request<{ sites: Site[] }>("/api/v1/sites", { signal }),
	organizations: () => request<{ organizations: Organization[] }>("/api/v1/organizations"),
	createSite: (input: SiteInput) => request<Site>("/api/v1/sites", { method: "POST", body: JSON.stringify(input) }),
	updateSite: (site: Site, input: SiteInput) => request<Site>(`/api/v1/sites/${encodeURIComponent(site.id)}`, { method: "PUT", body: JSON.stringify(input) }),
	updateAgent: (agentId: string, name: string, siteId: string) => request<{ updated: boolean }>(`/api/v1/agents/${encodeURIComponent(agentId)}`, { method: "PATCH", body: JSON.stringify({ name, siteId }) }),
	startAgentUpdate: (agentId: string) => request<AgentUpdate>(`/api/v1/agents/${encodeURIComponent(agentId)}/updates`, { method: "POST", body: "{}" }),
	disableAgent: (agentId: string) => request<{ disabled: boolean }>(`/api/v1/agents/${encodeURIComponent(agentId)}`, { method: "DELETE", body: "{}" }),
	applications: (signal?: AbortSignal) => request<{ applications: Application[] }>("/api/v1/applications", { signal }),
	revealStoredThreeXUICredentials: (applicationId: string, currentPassword: string) => request<{ username: string; password: string }>(`/api/v1/applications/${encodeURIComponent(applicationId)}/credentials/reveal`, { method: "POST", body: JSON.stringify({ currentPassword }) }),
	reconcileThreeXUINode: (applicationId: string) => request<ApplicationCommand>(`/api/v1/applications/${encodeURIComponent(applicationId)}/3xui-node/reconcile`, { method: "POST", body: "{}" }),
	threeXUIControllerMigrations: (signal?: AbortSignal) => request<{ migrations: ThreeXUIControllerMigration[] }>("/api/v1/three-x-ui-migrations", { signal }),
	threeXUIControllerMigration: (id: string) => request<ThreeXUIControllerMigration>(`/api/v1/three-x-ui-migrations/${encodeURIComponent(id)}`),
	retryThreeXUIControllerMigrationCleanup: (id: string) => request<ThreeXUIControllerMigration>(`/api/v1/three-x-ui-migrations/${encodeURIComponent(id)}/retry-cleanup`, { method: "POST", body: "{}" }),
	migrateThreeXUIController: (applicationId: string, targetApplicationId: string, allowStaleBackup: boolean) => request<ThreeXUIControllerMigration>(`/api/v1/applications/${encodeURIComponent(applicationId)}/3xui-controller/migrate`, { method: "POST", body: JSON.stringify({ targetApplicationId, confirm: true, allowStaleBackup }) }),
	regions: () => request<{ regions: Region[] }>("/api/v1/regions"),
	agentRegionSuggestion: (agentId: string) => request<RegionSuggestion>(`/api/v1/agents/${encodeURIComponent(agentId)}/region-suggestion`),
	verifyRealityTarget: (applicationId: string, targetHost: string, serverName: string) => request<ApplicationCommand>(`/api/v1/applications/${encodeURIComponent(applicationId)}/reality-targets/verify`, { method: "POST", body: JSON.stringify({ targetHost, serverName }) }),
	createRealityCommand: (input: { applicationId: string; regionCode: string; name: string; clientName?: string; gatewayNodeId: string; hostname: string; dnsProvider: "manual" | "cloudflare"; targetHost: string; serverName: string; inboundTotalBytes: number; inboundResetDay: number; clientTotalBytes?: number; clientResetDays?: number; clientExpiryTime?: number }) => request<ApplicationCommand>("/api/v1/application-commands/reality", { method: "POST", body: JSON.stringify(input) }),
	renameRealityCommand: (serviceId: string, regionCode: string, name: string) => request<ApplicationCommand>("/api/v1/application-commands/reality/rename", { method: "POST", body: JSON.stringify({ serviceId, regionCode, name }) }),
	createSubscriptionCommand: (input: { applicationId: string; gatewayNodeId: string; hostname?: string; kind: "public_direct" | "cloudflare_tunnel"; dnsProvider: "manual" | "cloudflare" }) => request<ApplicationCommand>("/api/v1/application-commands/subscription", { method: "POST", body: JSON.stringify(input) }),
	createThreeXUIClientCommand: (input: ThreeXUIClientCommandInput) => request<ApplicationCommand>("/api/v1/application-commands/clients", { method: "POST", body: JSON.stringify(input) }),
	latestApplicationCommand: (applicationId: string, kind: ApplicationCommandKind) => request<ApplicationCommand>(`/api/v1/applications/${encodeURIComponent(applicationId)}/commands/latest?kind=${encodeURIComponent(kind)}`),
	applicationCommand: (id: string) => request<ApplicationCommand>(`/api/v1/application-commands/${encodeURIComponent(id)}`),
	retryTaskReconciliation: (id: string) => request<{ taskId: string; kind: string; queued: boolean }>(`/api/v1/tasks/${encodeURIComponent(id)}/retry-reconciliation`, { method: "POST", body: "{}" }),
	revealApplicationCommand: (id: string, operationKey: string) => request<{ shareUri: string }>(`/api/v1/application-commands/${encodeURIComponent(id)}/reveal`, { method: "POST", headers: { "Idempotency-Key": operationKey }, body: "{}" }),
	acknowledgeApplicationCommand: (id: string, operationKey: string) => request<{ acknowledged: boolean }>(`/api/v1/application-commands/${encodeURIComponent(id)}/ack`, { method: "POST", headers: { "Idempotency-Key": operationKey }, body: "{}" }),
	services: (signal?: AbortSignal) => request<{ services: Service[] }>("/api/v1/services", { signal }),
	routes: () => request<{ routes: Route[] }>("/api/v1/routes"),
	publications: (signal?: AbortSignal) => request<{ publications: Publication[] }>("/api/v1/publications", { signal }),
	integrations: (signal?: AbortSignal) => request<{ integrations: Integration[] }>("/api/v1/integrations", { signal }),
	actions: (limit = 50, signal?: AbortSignal) => request<{ actions: Action[] }>(`/api/v1/actions?limit=${encodeURIComponent(String(limit))}`, { signal }),
  createDeployment: (agentId: string, appKey: string, config: Record<string, string | boolean | number>, operation: Deployment["operation"] = "install", deleteData = false, role?: "master" | "worker", registryCredentialId?: string, operationKey?: string) => request<Deployment>("/api/v1/deployments", { method: "POST", headers: operationKey ? { "Idempotency-Key": operationKey } : undefined, body: JSON.stringify({ agentId, appKey, config, operation, deleteData, role, registryCredentialId }) }),
  revealDeploymentCredentials: (id: string, operationKey: string) => request<{ username: string; password: string }>(`/api/v1/deployments/${encodeURIComponent(id)}/credentials/reveal`, { method: "POST", headers: { "Idempotency-Key": operationKey }, body: "{}" }),
  acknowledgeDeploymentCredentials: (id: string, operationKey: string) => request<{ acknowledged: boolean }>(`/api/v1/deployments/${encodeURIComponent(id)}/credentials/ack`, { method: "POST", headers: { "Idempotency-Key": operationKey }, body: "{}" }),
  confirmNetworkProfile: (agentId: string, profile: NetworkProfile) => request<NetworkProfile>(`/api/v1/agents/${encodeURIComponent(agentId)}/network-profile`, { method: "PUT", body: JSON.stringify(profile) }),
	createPublication: (input: { serviceId: string; kind: PublicationKind; gatewayNodeId?: string; hostname?: string; sniHostname?: string; dnsProvider: "manual" | "cloudflare" | "headscale"; tlsEnabled?: boolean; confirmHighRisk?: boolean }) => request<Publication>("/api/v1/publications", { method: "POST", body: JSON.stringify(input) }),
	updatePublicationTLS: (id: string, enabled: boolean) => request<Publication>(`/api/v1/publications/${encodeURIComponent(id)}/tls`, { method: "PUT", body: JSON.stringify({ enabled }) }),
  stopPublication: (id: string) => request<{ stopped: boolean }>(`/api/v1/publications/${encodeURIComponent(id)}`, { method: "DELETE", body: "{}" }),
  verifyPublication: (id: string) => request<Publication>(`/api/v1/publications/${encodeURIComponent(id)}/verify`, { method: "POST", body: "{}" }),
  startCloudflareOAuth: () => request<CloudflareOAuthStart>("/api/v1/integrations/cloudflare/oauth/start", { method: "POST", body: "{}" }),
  pollCloudflareOAuth: (sessionId: string) => request<CloudflareOAuthPoll>("/api/v1/integrations/cloudflare/oauth/poll", { method: "POST", body: JSON.stringify({ sessionId }) }),
  completeCloudflareOAuth: (sessionId: string, zoneId: string) => request<Integration>("/api/v1/integrations/cloudflare/oauth/complete", { method: "POST", body: JSON.stringify({ sessionId, zoneId }) }),
  cloudflareZones: () => request<{ zones: CloudflareZone[] }>("/api/v1/integrations/cloudflare/zones"),
	configureSetupDNS: (input: { centerUrl: string; headscaleUrl?: string; publicAddress: string; gatewayAddress: string; natConfirmed: boolean }) => request<{ records: Array<{ id: string; type: "A"; name: string; content: string }> }>("/api/v1/setup/cloudflare/dns", { method: "POST", body: JSON.stringify(input) }),
  verifySetupPublicEntry: (input: { publicAddress: string; gatewayAddress: string; natConfirmed: boolean }) => request<{ status: "ready"; publicAddress: string; gatewayAddress: string; ports: number[] }>("/api/v1/setup/public-entry/verify", { method: "POST", body: JSON.stringify(input) }),
  configureHeadscale: (input: { mode: "builtin" | "external"; url: string; apiKey?: string }) => request<Integration>("/api/v1/integrations/headscale", { method: "PUT", body: JSON.stringify(input) }),
  tailscaleFixedEndpoint: (signal?: AbortSignal) => request<TailscaleFixedEndpoint>("/api/v1/network/tailscale-fixed-endpoint", { signal }),
  configureTailscaleFixedEndpoint: (input: TailscaleFixedEndpointInput) => request<TailscaleFixedEndpoint>("/api/v1/network/tailscale-fixed-endpoint", { method: "PUT", body: JSON.stringify(input) }),
  centerRemoteAccess: (signal?: AbortSignal) => request<CenterRemoteAccess>("/api/v1/network/center-remote-access", { signal }),
  configureCenterRemoteAccess: (input: CenterRemoteAccessInput) => request<CenterRemoteAccess>("/api/v1/network/center-remote-access", { method: "PUT", body: JSON.stringify(input) }),
  createHeadscaleJoin: (agentId: string) => request<HeadscaleJoin>(`/api/v1/agents/${encodeURIComponent(agentId)}/headscale-join`, { method: "POST", body: "{}" }),
  createSource: (source: {
    id: string;
    displayName: string;
    url: string;
    publicKey: string;
    bearerToken: string;
    customCA: string;
    refreshIntervalSeconds: number;
  }) => request<{ id: string }>("/api/v1/catalog/sources", { method: "POST", body: JSON.stringify(source) }),
  updateSource: (id: string, source: Partial<{
    displayName: string;
    url: string;
    publicKey: string;
    bearerToken: string;
    customCA: string;
    refreshIntervalSeconds: number;
    enabled: boolean;
  }>) => request<{ id: string }>(`/api/v1/catalog/sources/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(source) }),
  deleteSource: (id: string) => request<void>(`/api/v1/catalog/sources/${encodeURIComponent(id)}`, { method: "DELETE", body: "{}" }),
  refreshSource: (id: string) => request<{ sourceID: string; apps?: number; notModified: boolean }>(`/api/v1/catalog/sources/${encodeURIComponent(id)}/refresh`, { method: "POST", body: "{}" })
};
