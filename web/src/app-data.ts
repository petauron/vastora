import { APIError, api } from "./api";
import type { AppData, CenterStatus, Screen } from "./types";

export type AppDataPatch = Partial<AppData> & { status: CenterStatus };

export function emptyAppData(status: CenterStatus): AppData {
  return {
    status,
    centerUpdate: { currentVersion: status.version, updateAvailable: false, releaseCheckAvailable: false, automatic: false, state: "idle" },
    sources: [],
    apps: [],
    registryCredentials: [],
    agents: [],
    deployments: [],
    organizations: [],
    sites: [],
    applications: [],
    services: [],
    publications: [],
    routes: [],
    integrations: [],
    actions: [],
    threeXUIControllerMigrations: [],
    meridian: { cutover: { state: "not_required", subscriptionAuthority: "meridian", expectedAccounts: 0, importedAccounts: 0, expectedCredentials: 0, importedCredentials: 0, expectedEndpoints: 0, readyEndpoints: 0, retiredEndpoints: 0, expectedRoutes: 0, readyRoutes: 0, blockedRoutes: 0, pendingDeployments: 0, failedDeployments: 0, updatedAt: "", complete: false }, endpoints: [], accounts: [], grants: [] },
    systemDomain: { namespace: "", centerUrl: status.agentConnectUrl, headscaleUrl: "", cloudflareZone: "", aliases: [], activePublications: 0, pendingCleanup: 0, builtinHeadscale: false, cloudflareOAuthAvailable: false },
    centerRemoteAccess: null
  };
}

async function loadCenterRemoteAccess(signal?: AbortSignal) {
  try {
    return { centerRemoteAccess: await api.centerRemoteAccess(signal), centerRemoteAccessError: undefined };
  } catch (error) {
    if (signal?.aborted || (error instanceof APIError && error.status === 401)) throw error;
    return {
      centerRemoteAccess: null,
      centerRemoteAccessError: error instanceof Error ? error.message : "Center remote access status request failed"
    };
  }
}

async function loadCatalogSources(signal?: AbortSignal): Promise<Pick<AppData, "sources" | "catalogSourcesError">> {
  try {
    const { sources } = await api.sources(signal);
    return { sources, catalogSourcesError: undefined };
  } catch (error) {
    if (signal?.aborted || (error instanceof APIError && error.status === 401)) throw error;
    // Do not present source status cached from another screen as current. App
    // manifests keep their own server-provided install restrictions, and source
    // status failures must not prevent managing already installed applications.
    return { sources: [], catalogSourcesError: "Catalog source status is unavailable" };
  }
}

// Desktop shortcuts, launch targets and the status widget belong to the shell.
// Load them once per refresh, including direct visits to application windows.
async function loadDesktopData(signal?: AbortSignal): Promise<AppDataPatch> {
  const [status, apps, agents, applications, services, publications] = await Promise.all([
    api.status(signal), api.apps(signal), api.agents(signal), api.applications(signal), api.services(signal), api.publications(signal)
  ]);
  return { status, apps: apps.apps, agents: agents.agents, applications: applications.applications, services: services.services, publications: publications.publications };
}

export async function loadScreenData(screen: Screen, signal?: AbortSignal): Promise<AppDataPatch> {
  const desktop = loadDesktopData(signal);
  switch (screen) {
    case "home":
    case "assistant":
      return desktop;
    case "overview": {
      const [shared, centerUpdate, sites, actions] = await Promise.all([
        desktop, api.centerUpdate(false, signal), api.sites(signal), api.actions(10, signal)
      ]);
      return { ...shared, centerUpdate, sites: sites.sites, actions: actions.actions };
    }
    case "nodes": {
      const [shared, sites, integrations, meridian] = await Promise.all([
        desktop, api.sites(signal), api.integrations(signal), api.meridian(signal)
      ]);
      return { ...shared, sites: sites.sites, integrations: integrations.integrations, meridian };
    }
    case "apps": {
      const [shared, registryCredentials, deployments, integrations, sites, migrations, meridian, remoteAccess, catalogSources] = await Promise.all([
        desktop,
        api.registryCredentials(signal),
        api.deployments(signal),
        api.integrations(signal),
        api.sites(signal),
        api.threeXUIControllerMigrations(signal),
        api.meridian(signal),
        loadCenterRemoteAccess(signal),
        loadCatalogSources(signal)
      ]);
      return {
        ...shared,
        registryCredentials: registryCredentials.credentials,
        deployments: deployments.deployments,
        integrations: integrations.integrations,
        sites: sites.sites,
        threeXUIControllerMigrations: migrations.migrations,
        meridian,
        ...remoteAccess,
        ...catalogSources
      };
    }
    case "network": {
      const [shared, integrations, tailscaleFixedEndpoint, centerRemoteAccess] = await Promise.all([
        desktop, api.integrations(signal), api.tailscaleFixedEndpoint(signal), api.centerRemoteAccess(signal)
      ]);
      return { ...shared, integrations: integrations.integrations, tailscaleFixedEndpoint, centerRemoteAccess, centerRemoteAccessError: undefined };
    }
    case "activity": {
      const [shared, actions] = await Promise.all([desktop, api.actions(100, signal)]);
      return { ...shared, actions: actions.actions };
    }
    case "settings": {
      const [shared, centerUpdate, sources, systemDomain] = await Promise.all([
        desktop, api.centerUpdate(false, signal), api.sources(signal), api.systemDomain(signal)
      ]);
      return { ...shared, centerUpdate, sources: sources.sources, catalogSourcesError: undefined, systemDomain };
    }
  }
}

const screenPaths: Record<Screen, string> = {
  home: "/",
  overview: "/overview",
  nodes: "/nodes",
  apps: "/apps",
  network: "/network",
  activity: "/activity",
  assistant: "/assistant",
  settings: "/settings"
};

export function screenFromPath(pathname = window.location.pathname): Screen {
  const entry = Object.entries(screenPaths).find(([, path]) => path === pathname);
  return entry?.[0] as Screen | undefined ?? "home";
}

export function pathForScreen(screen: Screen): string {
  return screenPaths[screen];
}
