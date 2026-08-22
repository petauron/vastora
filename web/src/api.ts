import type { Action, AgentEnrollment, AgentView, ApplicationCommand, ApplicationCommandKind, AppView, Application, CatalogSource, CloudflareOAuthPoll, CloudflareOAuthStart, CenterStatus, Deployment, Diagnostics, HeadscaleJoin, InitialSetupInput, Integration, NetworkProfile, Organization, Publication, PublicationKind, Route, Service, SetupStatus, Site, SiteInput, ThreeXUIClientCommandInput } from "./types";

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
  status: () => request<CenterStatus>("/api/v1/status"),
  diagnostics: () => request<Diagnostics>("/api/v1/diagnostics"),
  downloadDiagnostics: () => download("/api/v1/diagnostics", `vastora-diagnostics-${new Date().toISOString().slice(0, 10)}.json`),
  downloadBackup: (password: string) => download("/api/v1/backups", "vastora-center.vastora", { method: "POST", body: JSON.stringify({ password }) }),
  sources: () => request<{ sources: CatalogSource[] }>("/api/v1/catalog/sources"),
  apps: () => request<{ apps: AppView[] }>("/api/v1/catalog/apps"),
	agents: () => request<{ agents: AgentView[] }>("/api/v1/agents"),
	createAgentEnrollment: (siteId: string, name: string, centerUrl: string, useHeadscale: boolean, gateway: boolean, tunnel: boolean) => request<AgentEnrollment>("/api/v1/agent-enrollments", { method: "POST", body: JSON.stringify({ siteId, name, centerUrl, useHeadscale, gateway, tunnel }) }),
  deployments: () => request<{ deployments: Deployment[] }>("/api/v1/deployments"),
	sites: () => request<{ sites: Site[] }>("/api/v1/sites"),
	organizations: () => request<{ organizations: Organization[] }>("/api/v1/organizations"),
	createSite: (input: SiteInput) => request<Site>("/api/v1/sites", { method: "POST", body: JSON.stringify(input) }),
	updateSite: (site: Site, input: SiteInput) => request<Site>(`/api/v1/sites/${encodeURIComponent(site.id)}`, { method: "PUT", body: JSON.stringify(input) }),
	updateAgent: (agentId: string, name: string, siteId: string) => request<{ updated: boolean }>(`/api/v1/agents/${encodeURIComponent(agentId)}`, { method: "PATCH", body: JSON.stringify({ name, siteId }) }),
	disableAgent: (agentId: string) => request<{ disabled: boolean }>(`/api/v1/agents/${encodeURIComponent(agentId)}`, { method: "DELETE", body: "{}" }),
	applications: () => request<{ applications: Application[] }>("/api/v1/applications"),
	createRealityCommand: (input: { applicationId: string; name: string; gatewayNodeId: string; hostname: string; dnsProvider: "manual" | "cloudflare"; target?: string; sniHostname?: string }) => request<ApplicationCommand>("/api/v1/application-commands/reality", { method: "POST", body: JSON.stringify(input) }),
	createSubscriptionCommand: (input: { applicationId: string; gatewayNodeId: string; hostname: string; kind: "public_direct" | "cloudflare_tunnel"; dnsProvider: "manual" | "cloudflare" }) => request<ApplicationCommand>("/api/v1/application-commands/subscription", { method: "POST", body: JSON.stringify(input) }),
	createThreeXUIClientCommand: (input: ThreeXUIClientCommandInput) => request<ApplicationCommand>("/api/v1/application-commands/clients", { method: "POST", body: JSON.stringify(input) }),
	latestApplicationCommand: (applicationId: string, kind: ApplicationCommandKind) => request<ApplicationCommand>(`/api/v1/applications/${encodeURIComponent(applicationId)}/commands/latest?kind=${encodeURIComponent(kind)}`),
	applicationCommand: (id: string) => request<ApplicationCommand>(`/api/v1/application-commands/${encodeURIComponent(id)}`),
	revealApplicationCommand: (id: string) => request<{ shareUri: string }>(`/api/v1/application-commands/${encodeURIComponent(id)}/reveal`, { method: "POST", body: "{}" }),
	services: () => request<{ services: Service[] }>("/api/v1/services"),
	routes: () => request<{ routes: Route[] }>("/api/v1/routes"),
	publications: () => request<{ publications: Publication[] }>("/api/v1/publications"),
	integrations: () => request<{ integrations: Integration[] }>("/api/v1/integrations"),
	actions: (limit = 50) => request<{ actions: Action[] }>(`/api/v1/actions?limit=${encodeURIComponent(String(limit))}`),
  createDeployment: (agentId: string, appKey: string, config: Record<string, string | boolean | number>, operation: Deployment["operation"] = "install", deleteData = false) => request<Deployment>("/api/v1/deployments", { method: "POST", body: JSON.stringify({ agentId, appKey, config, operation, deleteData }) }),
  confirmNetworkProfile: (agentId: string, profile: NetworkProfile) => request<NetworkProfile>(`/api/v1/agents/${encodeURIComponent(agentId)}/network-profile`, { method: "PUT", body: JSON.stringify(profile) }),
	createPublication: (input: { serviceId: string; kind: PublicationKind; gatewayNodeId?: string; hostname: string; sniHostname?: string; dnsProvider: "manual" | "cloudflare" | "headscale"; tlsEnabled?: boolean; confirmHighRisk?: boolean }) => request<Publication>("/api/v1/publications", { method: "POST", body: JSON.stringify(input) }),
  stopPublication: (id: string) => request<{ stopped: boolean }>(`/api/v1/publications/${encodeURIComponent(id)}`, { method: "DELETE", body: "{}" }),
  verifyPublication: (id: string) => request<Publication>(`/api/v1/publications/${encodeURIComponent(id)}/verify`, { method: "POST", body: "{}" }),
  startCloudflareOAuth: () => request<CloudflareOAuthStart>("/api/v1/integrations/cloudflare/oauth/start", { method: "POST", body: "{}" }),
  pollCloudflareOAuth: (sessionId: string) => request<CloudflareOAuthPoll>("/api/v1/integrations/cloudflare/oauth/poll", { method: "POST", body: JSON.stringify({ sessionId }) }),
  completeCloudflareOAuth: (sessionId: string, zoneId: string) => request<Integration>("/api/v1/integrations/cloudflare/oauth/complete", { method: "POST", body: JSON.stringify({ sessionId, zoneId }) }),
  configureSetupDNS: (input: { centerUrl: string; headscaleUrl?: string; publicAddress: string }) => request<{ records: Array<{ id: string; type: "A" | "AAAA"; name: string; content: string }> }>("/api/v1/setup/cloudflare/dns", { method: "POST", body: JSON.stringify(input) }),
  configureHeadscale: (input: { mode: "builtin" | "external"; url: string; apiKey?: string }) => request<Integration>("/api/v1/integrations/headscale", { method: "PUT", body: JSON.stringify(input) }),
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
  refreshSource: (id: string) => request<{ sourceID: string; apps?: number; notModified: boolean }>(`/api/v1/catalog/sources/${encodeURIComponent(id)}/refresh`, { method: "POST", body: "{}" })
};
