import { api } from "./api";
import type { AppData, CenterStatus, Screen } from "./types";

export type AppDataPatch = Partial<AppData> & { status: CenterStatus };

export function emptyAppData(status: CenterStatus): AppData {
  return {
    status,
    centerUpdate: { currentVersion: status.version, updateAvailable: false, automatic: false, state: "idle" },
    sources: [],
    apps: [],
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

export async function loadScreenData(screen: Screen): Promise<AppDataPatch> {
  const statusPromise = api.status();

  switch (screen) {
    case "home": {
      const [status, centerUpdate, sites, agents, applications, publications, actions] = await Promise.all([
        statusPromise,
        api.centerUpdate(),
        api.sites(),
        api.agents(),
        api.applications(),
        api.publications(),
        api.actions(10)
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
        api.sites(),
        api.agents(),
        api.integrations(),
        api.publications()
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
      const [status, apps, agents, deployments, applications, services, publications, integrations, sites, migrations] = await Promise.all([
        statusPromise,
        api.apps(),
        api.agents(),
        api.deployments(),
        api.applications(),
        api.services(),
        api.publications(),
        api.integrations(),
        api.sites(),
        api.threeXUIControllerMigrations()
      ]);
      return {
        status,
        apps: apps.apps,
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
      const [status, agents, integrations, tailscaleFixedEndpoint] = await Promise.all([
        statusPromise,
        api.agents(),
        api.integrations(),
        api.tailscaleFixedEndpoint()
      ]);
      return { status, agents: agents.agents, integrations: integrations.integrations, tailscaleFixedEndpoint };
    }
    case "activity": {
      const [status, actions, agents] = await Promise.all([
        statusPromise,
        api.actions(100),
        api.agents()
      ]);
      return { status, actions: actions.actions, agents: agents.agents };
    }
    case "settings": {
      const [status, centerUpdate, sources, applications, agents, systemDomain] = await Promise.all([
        statusPromise,
        api.centerUpdate(),
        api.sources(),
        api.applications(),
        api.agents(),
        api.systemDomain()
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
  settings: "/settings"
};

export function screenFromPath(pathname = window.location.pathname): Screen {
  const entry = Object.entries(screenPaths).find(([, path]) => path === pathname);
  return entry?.[0] as Screen | undefined ?? "home";
}

export function pathForScreen(screen: Screen): string {
  return screenPaths[screen];
}
