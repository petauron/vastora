import type { Action, AgentView, AppView, Application, CatalogSource, DashboardStatus, Deployment, HeadscaleJoin, Integration, NetworkProfile, Organization, Publication, PublicationKind, Route, Service, Site } from "./types";

export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number
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
  const body = (await response.json().catch(() => ({}))) as T & { error?: string };
  if (!response.ok) {
    throw new APIError(body.error ?? "Request failed", response.status);
  }
  return body;
}

export const api = {
  setupStatus: () => request<{ configured: boolean }>("/api/v1/setup/status"),
  setupAdmin: (username: string, password: string) =>
    request<{ configured: boolean }>("/api/v1/setup/admin", {
      method: "POST",
      body: JSON.stringify({ username, password })
    }),
  login: (username: string, password: string) =>
    request<{ authenticated: boolean }>("/api/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password })
    }),
  logout: () => request<{ authenticated: boolean }>("/api/v1/auth/logout", { method: "POST", body: "{}" }),
  status: () => request<DashboardStatus>("/api/v1/status"),
  sources: () => request<{ sources: CatalogSource[] }>("/api/v1/catalog/sources"),
  apps: () => request<{ apps: AppView[] }>("/api/v1/catalog/apps"),
  agents: () => request<{ agents: AgentView[] }>("/api/v1/agents"),
  deployments: () => request<{ deployments: Deployment[] }>("/api/v1/deployments"),
	sites: () => request<{ sites: Site[] }>("/api/v1/sites"),
	organizations: () => request<{ organizations: Organization[] }>("/api/v1/organizations"),
	createSite: (input: { name: string; code: string; description: string; domainSuffix: string; gatewayNodes: string[] }) => request<Site>("/api/v1/sites", { method: "POST", body: JSON.stringify(input) }),
	updateSite: (site: Site, input: { name: string; code: string; description: string; domainSuffix: string; gatewayNodes: string[] }) => request<Site>(`/api/v1/sites/${encodeURIComponent(site.id)}`, { method: "PUT", body: JSON.stringify(input) }),
	assignAgentSite: (agentId: string, siteId: string) => request<{ updated: boolean }>(`/api/v1/agents/${encodeURIComponent(agentId)}`, { method: "PATCH", body: JSON.stringify({ siteId }) }),
	applications: () => request<{ applications: Application[] }>("/api/v1/applications"),
	services: () => request<{ services: Service[] }>("/api/v1/services"),
	routes: () => request<{ routes: Route[] }>("/api/v1/routes"),
	publications: () => request<{ publications: Publication[] }>("/api/v1/publications"),
	integrations: () => request<{ integrations: Integration[] }>("/api/v1/integrations"),
	actions: () => request<{ actions: Action[] }>("/api/v1/actions"),
  createDeployment: (agentId: string, appKey: string, config: Record<string, string | boolean | number>, operation = "install", deleteData = false) => request<Deployment>("/api/v1/deployments", { method: "POST", body: JSON.stringify({ agentId, appKey, config, operation, deleteData }) }),
  confirmNetworkProfile: (agentId: string, profile: NetworkProfile) => request<NetworkProfile>(`/api/v1/agents/${encodeURIComponent(agentId)}/network-profile`, { method: "PUT", body: JSON.stringify(profile) }),
  createPublication: (input: { serviceId: string; kind: PublicationKind; gatewayNodeId?: string; hostname: string; dnsProvider: "manual" | "cloudflare" | "headscale"; confirmHighRisk?: boolean }) => request<Publication>("/api/v1/publications", { method: "POST", body: JSON.stringify(input) }),
  stopPublication: (id: string) => request<{ stopped: boolean }>(`/api/v1/publications/${encodeURIComponent(id)}`, { method: "DELETE", body: "{}" }),
  verifyPublication: (id: string) => request<Publication>(`/api/v1/publications/${encodeURIComponent(id)}/verify`, { method: "POST", body: "{}" }),
  configureCloudflare: (input: { accountId: string; zoneId: string; apiToken: string }) => request<Integration>("/api/v1/integrations/cloudflare", { method: "PUT", body: JSON.stringify(input) }),
  configureHeadscale: (input: { mode: "builtin" | "external"; url: string; apiKey: string }) => request<Integration>("/api/v1/integrations/headscale", { method: "PUT", body: JSON.stringify(input) }),
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
