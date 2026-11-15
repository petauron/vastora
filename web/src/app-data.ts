import { APIError, api } from "./api";
import type { AppData, CenterStatus, Screen } from "./types";

export type AppDataPatch = Partial<AppData> & { status: CenterStatus };

// A failed application inventory must not turn a healthy Center into an outage.
export class ApplicationPageDataError extends Error {
  constructor(readonly shared: AppDataPatch, readonly originalError: unknown) {
    super("Application page data is unavailable");
  }
}

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
async function loadDesktopData(signal?: AbortSignal, allowUnavailable = false): Promise<AppDataPatch> {
  const applicationData = Promise.allSettled([
    api.apps(signal), api.applications(signal), api.services(signal), api.publications(signal)
  ]);
  const [status, agents, results] = await Promise.all([
    api.status(signal), api.agents(signal), applicationData
  ]);
  signal?.throwIfAborted();
  // Check every result before degrading: an earlier 500 must not hide a 401
  // from another endpoint. Required page data still fails normally.
  for (const result of results) {
    if (result.status === "rejected" && (result.reason instanceof APIError && result.reason.status === 401 || result.reason instanceof Error && result.reason.name === "AbortError")) throw result.reason;
  }
  const [apps, applications, services, publications] = results;
  if (apps.status === "rejected" || applications.status === "rejected" || services.status === "rejected" || publications.status === "rejected") {
    if (!allowUnavailable) {
      for (const result of results) if (result.status === "rejected") throw result.reason;
    }
    // Omit failed display data rather than replacing it with fake empty lists.
    // Existing data is retained but unavailable for shortcuts until refreshed.
    return { status, agents: agents.agents, desktopDataUnavailable: true };
  }
  return { status, apps: apps.value.apps, agents: agents.agents, applications: applications.value.applications, services: services.value.services, publications: publications.value.publications, desktopDataUnavailable: false };
}

export async function loadScreenData(screen: Screen, signal?: AbortSignal): Promise<AppDataPatch> {
  const desktop = loadDesktopData(signal, screen === "activity" || screen === "nodes" || screen === "apps");
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
      const auxiliary = Promise.allSettled([api.integrations(signal)]);
      const [shared, sites, results] = await Promise.all([
        desktop, api.sites(signal), auxiliary
      ]);
      signal?.throwIfAborted();
      for (const result of results) {
        if (result.status === "rejected" && (result.reason instanceof APIError && result.reason.status === 401 || result.reason instanceof Error && result.reason.name === "AbortError")) throw result.reason;
      }
      const [integrations] = results;
      // Keep failed auxiliary data out of the patch, not fake empty inventories.
      // Node controls fence dependent actions until a complete refresh succeeds.
      return {
        ...shared, sites: sites.sites,
        ...(integrations.status === "fulfilled" ? { integrations: integrations.value.integrations } : {}),
        nodeAuxiliaryDataUnavailable: results.some((result) => result.status === "rejected")
      };
    }
    case "apps": {
      const pageData = Promise.allSettled([
        api.registryCredentials(signal),
        api.deployments(signal),
        api.integrations(signal),
        api.sites(signal),
        api.threeXUIControllerMigrations(signal),
        loadCenterRemoteAccess(signal),
        loadCatalogSources(signal)
      ]);
      const [shared, results] = await Promise.all([desktop, pageData]);
      signal?.throwIfAborted();
      for (const result of results) {
        if (result.status === "rejected" && (result.reason instanceof APIError && result.reason.status === 401 || result.reason instanceof Error && result.reason.name === "AbortError")) throw result.reason;
      }
      const [registryCredentials, deployments, integrations, sites, migrations, remoteAccess, catalogSources] = results;
      if (shared.desktopDataUnavailable || registryCredentials.status === "rejected" || deployments.status === "rejected" || integrations.status === "rejected" || sites.status === "rejected" || migrations.status === "rejected" || remoteAccess.status === "rejected" || catalogSources.status === "rejected") {
        const failure = results.find((result) => result.status === "rejected");
        throw new ApplicationPageDataError(shared, failure?.reason ?? new Error("Application inventory is unavailable"));
      }
      return {
        ...shared,
        registryCredentials: registryCredentials.value.credentials,
        deployments: deployments.value.deployments,
        integrations: integrations.value.integrations,
        sites: sites.value.sites,
        threeXUIControllerMigrations: migrations.value.migrations,
        ...remoteAccess.value,
        ...catalogSources.value
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
