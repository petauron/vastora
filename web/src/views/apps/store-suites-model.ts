import type { AppData, AppView } from "@/types";
import type { Language } from "@/translations";
import { siteRegionCode } from "@/lib/regions";
import { localized } from "../appAccess";
import { applicationLaunchURL, isBackgroundApplication } from "../applicationLaunch";
import { daylightAppKey, daylightInstalledGroup, isDaylightAppKey, isDaylightCatalogApp } from "../daylight";
import {
  publicationNeedsAttention,
  serviceNeedsAttention,
  threeXUIAppKey,
  type InstalledAppGroup,
  type InstalledAppInstance,
} from "../installed-apps-model";
import { copy } from "../shared";
import { planBatchUpgrade, type BatchUpgradePlan, type BatchUpgradeTarget } from "./BatchUpgradeSheet";

export const glivelaControllerKey = "vastora-official/meridian";

export const glivelaNodeCapabilities = [
  { key: "vastora-official/glivela-entry", zh: "接收客户端连接", en: "Accept client connections", shortZh: "入口", shortEn: "Entry" },
  { key: "vastora-official/glivela-node", zh: "提供出口访问", en: "Provide outbound access", shortZh: "出口", shortEn: "Exit" },
  { key: "vastora-official/glivela-dual", zh: "接收连接并提供出口", en: "Accept connections and provide outbound access", shortZh: "入口与出口", shortEn: "Entry & exit" },
] as const;

export const glivelaAppKeys: ReadonlySet<string> = new Set([
  glivelaControllerKey,
  ...glivelaNodeCapabilities.map((item) => item.key),
]);

export function isGlivelaAppKey(key: string) {
  return glivelaAppKeys.has(key);
}

export function isGlivelaCatalogApp(app: AppView) {
  return app.sourceId === "vastora-official" && app.key === `vastora-official/${app.app.id}` && glivelaAppKeys.has(app.key);
}

export function glivelaRoleLabel(appKey: string, language: Language, compact = false) {
  const capability = glivelaNodeCapabilities.find((item) => item.key === appKey);
  if (!capability) return "";
  return compact ? copy(language, capability.shortZh, capability.shortEn) : copy(language, capability.zh, capability.en);
}

export function suiteIdForAppKey(appKey: string) {
  if (isGlivelaAppKey(appKey)) return glivelaControllerKey;
  if (isDaylightAppKey(appKey)) return daylightAppKey;
  return appKey;
}

export function isLegacyThreeXUIController(instance: InstalledAppInstance) {
  const { application } = instance;
  return application.appKey === threeXUIAppKey
    && application.role === "master"
    && Boolean(application.controllerApplicationId)
    && application.id !== application.controllerApplicationId;
}

export function instanceIsOffline(instance: InstalledAppInstance) {
  return Boolean(!instance.agent || !instance.agent.connected || instance.agent.credentialRevoked);
}

export function instanceHasIssue(instance: InstalledAppInstance) {
  const { application } = instance;
  return ["failed", "degraded"].includes(application.status)
    || Boolean(instance.activeChange?.reconciliationRequired)
    || application.nodeSyncStatus === "failed"
    || ["pending", "blocked"].includes(application.adoptionState ?? "")
    || ["unconfigured", "action_required", "rolled_back"].includes(application.nodeSiteStatus ?? "")
    || instance.services.some(serviceNeedsAttention)
    || instance.publications.some(publicationNeedsAttention);
}

export function instanceIsInProgress(instance: InstalledAppInstance) {
  return Boolean(instance.activeChange && !instance.activeChange.reconciliationRequired)
    || ["pending", "deploying"].includes(instance.application.status);
}

export function instanceIsUpdatable(instance: InstalledAppInstance) {
  return Boolean(instance.application.updateAvailable) && !isLegacyThreeXUIController(instance);
}

export function instanceIsRunningHealthy(instance: InstalledAppInstance) {
  return instance.application.status === "running"
    && !instanceIsInProgress(instance)
    && !instanceHasIssue(instance)
    && !instanceIsOffline(instance);
}

export type SuiteStatusFilter = "all" | "attention" | "progress" | "updatable" | "running" | "offline";
export type SuiteSortKey = "priority" | "site" | "name" | "version";

export function instanceMatchesStatus(instance: InstalledAppInstance, filter: SuiteStatusFilter) {
  switch (filter) {
    case "all":
      return true;
    case "attention":
      return instanceHasIssue(instance);
    case "progress":
      return instanceIsInProgress(instance);
    case "updatable":
      return instanceIsUpdatable(instance);
    case "offline":
      return instanceIsOffline(instance);
    case "running":
      return instanceIsRunningHealthy(instance);
  }
}

export function instancePriorityRank(instance: InstalledAppInstance) {
  if (instance.activeChange?.reconciliationRequired) return 0;
  if (instanceHasIssue(instance)) return 1;
  if (instanceIsInProgress(instance)) return 2;
  if (instanceIsUpdatable(instance) && !instanceIsOffline(instance)) return 3;
  if (instanceIsOffline(instance)) return 4;
  if (instanceIsUpdatable(instance)) return 5;
  if (instance.application.status === "running") return 6;
  return 7;
}

export function sortSuiteInstances(instances: InstalledAppInstance[], sort: SuiteSortKey) {
  return [...instances].sort((left, right) => {
    const leftName = left.agent?.name ?? left.application.nodeId;
    const rightName = right.agent?.name ?? right.application.nodeId;
    if (sort === "priority") {
      const rank = instancePriorityRank(left) - instancePriorityRank(right);
      if (rank !== 0) return rank;
      const site = left.siteName.localeCompare(right.siteName);
      if (site !== 0) return site;
      return leftName.localeCompare(rightName);
    }
    if (sort === "site") {
      const site = left.siteName.localeCompare(right.siteName);
      if (site !== 0) return site;
      return leftName.localeCompare(rightName);
    }
    if (sort === "version") {
      const leftVersion = left.application.installedVersion || "";
      const rightVersion = right.application.installedVersion || "";
      const cmp = leftVersion.localeCompare(rightVersion, undefined, { numeric: true });
      if (cmp !== 0) return cmp;
      return leftName.localeCompare(rightName);
    }
    return leftName.localeCompare(rightName);
  });
}

export type SuiteCounts = {
  instances: number;
  hosts: number;
  attention: number;
  progress: number;
  updatableTotal: number;
  updatableOnline: number;
  updatableSkipped: number;
  offline: number;
  running: number;
};

export type StoreSuiteKind = "standard" | "glivela" | "daylight";

export type StoreSuite = {
  id: string;
  kind: StoreSuiteKind;
  appKey: string;
  name: string;
  description: string;
  catalogApp?: AppView;
  catalogApps: AppView[];
  instances: InstalledAppInstance[];
  controllerInstances: InstalledAppInstance[];
  nodeInstances: InstalledAppInstance[];
  legacyInstances: InstalledAppInstance[];
  background: boolean;
  launchUrl?: string;
  canOpenWorkspace: boolean;
  openTargetKey?: string;
  counts: SuiteCounts;
  hasUpdates: boolean;
  hasRecovery: boolean;
  installedVersions: string[];
  availableVersions: string[];
  siteNames: string[];
  searchText: string;
};

export type UpdateSiteSummary = {
  siteId: string;
  siteName: string;
  regionCode: string;
  total: number;
  online: number;
};

export type StoreUpdateEntry = {
  id: string;
  suiteId: string;
  suite: StoreSuite;
  title: string;
  batchTitle: string;
  appKey: string;
  updatableInstances: InstalledAppInstance[];
  plan: BatchUpgradePlan;
  targetVersions: string[];
  installedVersions: string[];
  siteBreakdown: UpdateSiteSummary[];
  singleEligible?: BatchUpgradeTarget;
  manageAriaLabel: string;
  showOpen: boolean;
};

function computeCounts(
  title: string,
  instances: InstalledAppInstance[],
  data: AppData,
  language: Language,
  byNode: boolean,
): SuiteCounts {
  const units: InstalledAppInstance[][] = [];
  if (!byNode) {
    for (const instance of instances) units.push([instance]);
  } else {
    const byHost = new Map<string, InstalledAppInstance[]>();
    for (const instance of instances) {
      const existing = byHost.get(instance.application.nodeId);
      if (existing) existing.push(instance);
      else {
        const list = [instance];
        byHost.set(instance.application.nodeId, list);
        units.push(list);
      }
    }
  }
  const hosts = new Set(instances.map((instance) => instance.application.nodeId)).size;
  const attention = units.filter((unit) => unit.some(instanceHasIssue)).length;
  const offline = units.filter((unit) => unit.some(instanceIsOffline) && !unit.some(instanceHasIssue)).length;
  const progress = units.filter((unit) => unit.some(instanceIsInProgress) && !unit.some(instanceHasIssue)).length;
  const running = units.filter((unit) => unit.every(instanceIsRunningHealthy)).length;
  const updatable = instances.filter(instanceIsUpdatable);
  const plan = updatable.length > 0 ? planBatchUpgrade(title, updatable, data, language) : null;

  return {
    instances: instances.length,
    hosts,
    attention,
    progress,
    updatableTotal: updatable.length,
    updatableOnline: plan?.eligible.length ?? 0,
    updatableSkipped: plan?.skipped.length ?? 0,
    offline,
    running,
  };
}

function uniqueVersions(values: Array<string | undefined>) {
  return [...new Set(values.filter((value): value is string => Boolean(value)).map((value) => `v${value}`))]
    .sort((left, right) => left.localeCompare(right, undefined, { numeric: true }));
}

function buildSearchText(parts: Array<string | undefined>) {
  return parts.filter(Boolean).join(" ").toLocaleLowerCase();
}

export function buildStoreSuites(
  data: AppData,
  groups: InstalledAppGroup[],
  language: Language,
): StoreSuite[] {
  const suites: StoreSuite[] = [];
  const glivelaGroups = groups.filter((group) => isGlivelaAppKey(group.appKey));
  const daylightGroup = daylightInstalledGroup(groups);
  const otherGroups = groups.filter((group) => !isGlivelaAppKey(group.appKey) && !isDaylightAppKey(group.appKey));

  const glivelaCatalog = data.apps.filter(isGlivelaCatalogApp);
  const daylightCatalog = data.apps.filter(isDaylightCatalogApp);

  if (glivelaGroups.length > 0 || glivelaCatalog.length > 0) {
    const controllerGroup = glivelaGroups.find((group) => group.appKey === glivelaControllerKey);
    const legacyInstances = controllerGroup?.instances.filter((instance) => instance.application.legacyGlivela) ?? [];
    const controllerInstances = controllerGroup?.instances.filter((instance) => !instance.application.legacyGlivela) ?? [];
    const nodeInstances = glivelaGroups
      .filter((group) => group.appKey !== glivelaControllerKey)
      .flatMap((group) => group.instances);
    const allInstances = [...controllerInstances, ...nodeInstances];
    const officialApp = glivelaCatalog.find((app) => app.key === glivelaControllerKey) ?? glivelaCatalog[0];
    const counts = computeCounts("Glivela", allInstances, data, language, true);
    const updatable = allInstances.filter(instanceIsUpdatable);
    suites.push({
      id: glivelaControllerKey,
      kind: "glivela",
      appKey: glivelaControllerKey,
      name: "Glivela",
      description: officialApp
        ? localized(officialApp, language, "description")
        : copy(language, "管理控制端和节点端。节点功能在各主机配置中管理。", "Manage controllers and nodes. Configure node capabilities on each host."),
      catalogApp: officialApp,
      catalogApps: glivelaCatalog,
      instances: allInstances,
      controllerInstances,
      nodeInstances,
      legacyInstances,
      background: false,
      launchUrl: undefined,
      canOpenWorkspace: controllerInstances.length > 0,
      openTargetKey: controllerInstances.length > 0 ? glivelaControllerKey : undefined,
      counts,
      hasUpdates: updatable.length > 0,
      hasRecovery: allInstances.some((instance) => Boolean(instance.activeChange?.reconciliationRequired)),
      installedVersions: uniqueVersions(allInstances.map((instance) => instance.application.installedVersion)),
      availableVersions: uniqueVersions(updatable.map((instance) => instance.application.availableVersion || instance.app?.app.version)),
      siteNames: [...new Set(allInstances.map((instance) => instance.siteName))],
      searchText: buildSearchText([
        "Glivela", "控制端", "节点端", "Controller", "Node",
        ...glivelaNodeCapabilities.flatMap((item) => [item.key, item.zh, item.en]),
        ...glivelaCatalog.flatMap((app) => [app.key, app.sourceId, localized(app, language, "name"), localized(app, language, "description")]),
        ...allInstances.flatMap((instance) => [instance.application.name, instance.agent?.name, instance.siteName]),
      ]),
    });
  }

  if (daylightGroup || daylightCatalog.length > 0) {
    const instances = daylightGroup?.instances ?? [];
    const officialApp = daylightCatalog.find((app) => app.key === daylightAppKey) ?? daylightCatalog[0];
    const counts = computeCounts("Daylight", instances, data, language, true);
    const updatable = instances.filter(instanceIsUpdatable);
    suites.push({
      id: daylightAppKey,
      kind: "daylight",
      appKey: daylightAppKey,
      name: "Daylight",
      description: officialApp
        ? localized(officialApp, language, "description")
        : copy(language, "管理各主机的 Daylight 站点。", "Manage Daylight sites on each host."),
      catalogApp: officialApp,
      catalogApps: daylightCatalog,
      instances,
      controllerInstances: [],
      nodeInstances: [],
      legacyInstances: [],
      background: false,
      launchUrl: applicationLaunchURL(data, daylightAppKey),
      canOpenWorkspace: instances.length > 0,
      openTargetKey: instances.length > 0 ? daylightAppKey : undefined,
      counts,
      hasUpdates: updatable.length > 0,
      hasRecovery: instances.some((instance) => Boolean(instance.activeChange?.reconciliationRequired)),
      installedVersions: uniqueVersions(instances.map((instance) => instance.application.installedVersion)),
      availableVersions: uniqueVersions(updatable.map((instance) => instance.application.availableVersion || instance.app?.app.version)),
      siteNames: [...new Set(instances.map((instance) => instance.siteName))],
      searchText: buildSearchText([
        "Daylight", "HTTP", "HTTPS",
        ...daylightCatalog.flatMap((app) => [app.key, app.sourceId, localized(app, language, "name"), localized(app, language, "description")]),
        ...instances.flatMap((instance) => [instance.application.name, instance.agent?.name, instance.siteName]),
      ]),
    });
  }

  for (const group of otherGroups) {
    const app = group.app;
    const name = app ? localized(app, language, "name") : group.instances[0]?.application.name ?? group.appKey;
    const description = app
      ? localized(app, language, "description")
      : copy(language, "已安装 · 已从目录移除", "Installed · unavailable in catalog");
    const background = isBackgroundApplication(app);
    const launchUrl = background ? undefined : applicationLaunchURL(data, group.appKey);
    const counts = computeCounts(name, group.instances, data, language, false);
    const updatable = group.instances.filter(instanceIsUpdatable);
    suites.push({
      id: group.id,
      kind: "standard",
      appKey: group.appKey,
      name,
      description,
      catalogApp: app,
      catalogApps: app ? [app] : [],
      instances: group.instances,
      controllerInstances: [],
      nodeInstances: [],
      legacyInstances: [],
      background,
      launchUrl,
      canOpenWorkspace: !background && !launchUrl,
      openTargetKey: !background && !launchUrl ? group.appKey : undefined,
      counts,
      hasUpdates: updatable.length > 0,
      hasRecovery: group.instances.some((instance) => Boolean(instance.activeChange?.reconciliationRequired)),
      installedVersions: uniqueVersions(group.instances.map((instance) => instance.application.installedVersion)),
      availableVersions: uniqueVersions(updatable.map((instance) => instance.application.availableVersion || instance.app?.app.version)),
      siteNames: [...new Set(group.instances.map((instance) => instance.siteName))],
      searchText: buildSearchText([
        name, group.appKey, app?.sourceId, description,
        ...group.instances.flatMap((instance) => [instance.application.name, instance.agent?.name, instance.siteName]),
      ]),
    });
  }

  return suites;
}

function summarizeSites(
  instances: InstalledAppInstance[],
  eligibleIds: Set<string>,
  data: AppData,
): UpdateSiteSummary[] {
  const siteMap = new Map(data.sites.map((site) => [site.id, site]));
  const summary = new Map<string, UpdateSiteSummary>();
  for (const instance of instances) {
    const siteId = instance.application.siteId;
    const site = siteMap.get(siteId);
    const siteName = site?.name ?? instance.siteName;
    const existing = summary.get(siteId);
    const isOnline = eligibleIds.has(instance.application.id);
    if (existing) {
      existing.total += 1;
      if (isOnline) existing.online += 1;
    } else {
      summary.set(siteId, {
        siteId,
        siteName,
        regionCode: site ? siteRegionCode(site) : siteRegionCode({ name: siteName }),
        total: 1,
        online: isOnline ? 1 : 0,
      });
    }
  }
  return [...summary.values()].sort((left, right) => right.total - left.total || left.siteName.localeCompare(right.siteName));
}

function makeUpdateEntry(
  id: string,
  suite: StoreSuite,
  title: string,
  batchTitle: string,
  updatableInstances: InstalledAppInstance[],
  data: AppData,
  language: Language,
  manageAriaLabel: string,
  showOpen: boolean,
): StoreUpdateEntry {
  const plan = planBatchUpgrade(batchTitle, updatableInstances, data, language);
  const eligibleIds = new Set(plan.eligible.map((item) => item.instance.application.id));
  const targetVersions = uniqueVersions(
    updatableInstances.map((instance) => {
      const catalog = data.apps.find((app) => app.key === instance.application.appKey);
      return catalog?.app.version || instance.application.availableVersion;
    }),
  );
  const installedVersions = uniqueVersions(updatableInstances.map((instance) => instance.application.installedVersion));
  const singleEligible = updatableInstances.length === 1 && plan.eligible.length === 1 && plan.skipped.length === 0
    ? plan.eligible[0]
    : undefined;
  return {
    id,
    suiteId: suite.id,
    suite,
    title,
    batchTitle,
    appKey: suite.appKey,
    updatableInstances,
    plan,
    targetVersions,
    installedVersions,
    siteBreakdown: summarizeSites(updatableInstances, eligibleIds, data),
    singleEligible,
    manageAriaLabel,
    showOpen,
  };
}

export function buildStoreUpdateEntries(
  suites: StoreSuite[],
  data: AppData,
  language: Language,
): StoreUpdateEntry[] {
  const entries: StoreUpdateEntry[] = [];
  for (const suite of suites) {
    if (!suite.hasUpdates) continue;
    if (suite.kind === "glivela") {
      const controllerUpdatable = suite.controllerInstances.filter(instanceIsUpdatable);
      const nodeUpdatable = suite.nodeInstances.filter(instanceIsUpdatable);
      const hasOpen = Boolean(suite.launchUrl || suite.canOpenWorkspace);
      if (controllerUpdatable.length > 0) {
        entries.push(makeUpdateEntry(
          `${suite.id}:controller`,
          suite,
          copy(language, "Glivela 控制端", "Glivela Controller"),
          copy(language, "Glivela 控制端", "Glivela Controller"),
          controllerUpdatable,
          data,
          language,
          copy(language, "管理 Glivela", "Manage Glivela"),
          hasOpen,
        ));
      }
      if (nodeUpdatable.length > 0) {
        const manageLabel = controllerUpdatable.length > 0
          ? copy(language, "管理 Glivela 节点端", "Manage Glivela Node")
          : copy(language, "管理 Glivela", "Manage Glivela");
        entries.push(makeUpdateEntry(
          `${suite.id}:node`,
          suite,
          copy(language, "Glivela 节点端", "Glivela Node"),
          copy(language, "Glivela 节点端", "Glivela Node"),
          nodeUpdatable,
          data,
          language,
          manageLabel,
          hasOpen && controllerUpdatable.length === 0,
        ));
      }
      continue;
    }

    const updatable = suite.instances.filter(instanceIsUpdatable);
    if (updatable.length === 0) continue;
    entries.push(makeUpdateEntry(
      suite.id,
      suite,
      suite.name,
      suite.name,
      updatable,
      data,
      language,
      copy(language, `管理 ${suite.name}`, `Manage ${suite.name}`),
      !suite.background && Boolean(suite.launchUrl || suite.canOpenWorkspace),
    ));
  }

  return entries.sort((left, right) =>
    right.plan.eligible.length - left.plan.eligible.length
    || right.updatableInstances.length - left.updatableInstances.length
    || left.title.localeCompare(right.title, language));
}
