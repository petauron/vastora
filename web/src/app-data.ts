import { api } from "./api";
import type { AppData, CenterStatus, Screen } from "./types";

export type AppDataPatch = Partial<AppData> & { status: CenterStatus };

export function emptyAppData(status: CenterStatus): AppData {
  return {
    status,
    centerUpdate: { currentVersion: status.version, updateAvailable: false, automatic: false, state: "idle" },
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
    systemDomain: { namespace: "", centerUrl: status.agentConnectUrl, headscaleUrl: "", cloudflareZone: "", aliases: [], activePublications: 0, pendingCleanup: 0, builtinHeadscale: false, cloudflareOAuthAvailable: false }
  };
}

export async function loadScreenData(screen: Screen, signal?: AbortSignal): Promise<AppDataPatch> {
  const statusPromise = api.status(signal);

  switch (screen) {
    case "home": {
      const [status, centerUpdate, sites, agents, applications, publications, actions] = await Promise.all([
        statusPromise,
        api.centerUpdate(false, signal),
        api.sites(signal),
        api.agents(signal),
        api.applications(signal),
        api.publications(signal),
        api.actions(10, signal)
      ]);
      return {
        status,
        centerUpdate,
        sites: sites.sites,
        agents: agents.agents,
        applications: applications.applications,
        publications: publications.publications,
        actions: actions.actions
      };
    }
    case "nodes": {
      const [status, sites, agents, integrations, publications] = await Promise.all([
        statusPromise,
        api.sites(signal),
        api.agents(signal),
        api.integrations(signal),
        api.publications(signal)
      ]);
      return {
        status,
        sites: sites.sites,
        agents: agents.agents,
        integrations: integrations.integrations,
        publications: publications.publications
      };
    }
    case "apps": {
      const [status, apps, registryCredentials, agents, deployments, applications, services, publications, integrations, sites, migrations] = await Promise.all([
        statusPromise,
        api.apps(signal),
        api.registryCredentials(signal),
        api.agents(signal),
        api.deployments(signal),
        api.applications(signal),
        api.services(signal),
        api.publications(signal),
        api.integrations(signal),
        api.sites(signal),
        api.threeXUIControllerMigrations(signal)
      ]);
      return {
        status,
        apps: apps.apps,
        registryCredentials: registryCredentials.credentials,
        agents: agents.agents,
        deployments: deployments.deployments,
        applications: applications.applications,
        services: services.services,
        publications: publications.publications,
        integrations: integrations.integrations,
        sites: sites.sites,
        threeXUIControllerMigrations: migrations.migrations
      };
    }
    case "network": {
      const [status, agents, integrations, tailscaleFixedEndpoint, centerRemoteAccess] = await Promise.all([
        statusPromise,
        api.agents(signal),
        api.integrations(signal),
        api.tailscaleFixedEndpoint(signal),
        api.centerRemoteAccess(signal)
      ]);
      return { status, agents: agents.agents, integrations: integrations.integrations, tailscaleFixedEndpoint, centerRemoteAccess };
    }
    case "activity": {
      const [status, actions, agents] = await Promise.all([
        statusPromise,
        api.actions(100, signal),
        api.agents(signal)
      ]);
      return { status, actions: actions.actions, agents: agents.agents };
    }
    case "assistant": {
      const status = await statusPromise;
      return { status };
    }
    case "settings": {
      const [status, centerUpdate, sources, applications, agents, systemDomain] = await Promise.all([
        statusPromise,
        api.centerUpdate(false, signal),
        api.sources(signal),
        api.applications(signal),
        api.agents(signal),
        api.systemDomain(signal)
      ]);
      return {
        status,
        centerUpdate,
        sources: sources.sources,
        applications: applications.applications,
        agents: agents.agents,
        systemDomain
      };
    }
  }
}

const screenPaths: Record<Screen, string> = {
  home: "/",
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
