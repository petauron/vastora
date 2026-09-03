import type { AgentView, AppData, Application, AppView, Deployment, Publication, Service, ThreeXUIControllerMigration } from "../types";
import { isInstalledApplication } from "./appAccess";

export const threeXUIAppKey = "vastora-official/3x-ui";

export type InstalledAppInstance = {
  application: Application;
  controller?: Application;
  agent?: AgentView;
  siteName: string;
  app?: AppView;
  services: Service[];
  publications: Publication[];
  realityServices: Service[];
  realityPublications: Publication[];
  activeChange?: Deployment;
  deployment?: Deployment;
  locked: boolean;
};

export type InstalledAppGroup = {
  id: string;
  appKey: string;
  app?: AppView;
  controller?: InstalledAppInstance;
  convergence?: ThreeXUIControllerMigration;
  legacyControllers: InstalledAppInstance[];
  instances: InstalledAppInstance[];
};

export function publicationNeedsAttention(publication: Publication) {
  return Boolean(publication.actionRequired || publication.lastError || publication.status === "failed" || publication.status === "degraded");
}

export function serviceNeedsAttention(service: Service) {
  return Boolean(service.lastError || service.actionRequiredReason || service.guardStatus === "action_required" || service.status === "failed" || service.status === "degraded");
}

export function canCreateRealityNode(instance: InstalledAppInstance) {
  return instance.application.appKey === threeXUIAppKey && !instance.locked && Boolean(instance.controller)
    && instance.realityServices.length === 0
    && (instance.application.id === instance.controller?.id || instance.application.role === "worker" && instance.application.nodeSyncStatus === "ready");
}

function indexBy<T>(values: T[], key: (value: T) => string) {
  const index = new Map<string, T[]>();
  for (const value of values) {
    const id = key(value);
    const group = index.get(id);
    if (group) group.push(value);
    else index.set(id, [value]);
  }
  return index;
}

export function installedAppGroups(data: Pick<AppData, "applications" | "apps" | "agents" | "sites" | "services" | "publications" | "deployments" | "threeXUIControllerMigrations">): InstalledAppGroup[] {
  const applications = data.applications.filter(isInstalledApplication);
  const applicationsByID = new Map(applications.map((application) => [application.id, application]));
  const apps = new Map(data.apps.map((app) => [app.key, app]));
  const agents = new Map(data.agents.map((agent) => [agent.id, agent]));
  const sites = new Map(data.sites.map((site) => [site.id, site]));
  const servicesByApplication = indexBy(data.services.filter((service) => service.status !== "stopped"), (service) => service.applicationId);
  const publicationsByService = indexBy(data.publications.filter((publication) => publication.status !== "stopped" || publication.actionRequired), (publication) => publication.serviceId);
  const deploymentsByApplication = indexBy(data.deployments.filter((deployment) => Boolean(deployment.applicationId)), (deployment) => deployment.applicationId!);
  const instances = new Map<string, InstalledAppInstance>();

  for (const application of applications) {
    const referencedController = application.controllerApplicationId ? applicationsByID.get(application.controllerApplicationId) : undefined;
    const controller = application.appKey === threeXUIAppKey && referencedController?.appKey === threeXUIAppKey
      && referencedController.role === "master" ? referencedController : undefined;
    const services = servicesByApplication.get(application.id) ?? [];
    const realityServices = services.filter((service) => service.appProtocol === "vless/tcp/reality");
    const deployments = deploymentsByApplication.get(application.id) ?? [];
    const activeChange = deployments.find((deployment) => deployment.state === "pending" || deployment.state === "running" || deployment.reconciliationRequired);
    instances.set(application.id, {
      application,
      controller,
      agent: agents.get(application.nodeId),
      siteName: sites.get(application.siteId)?.name ?? application.siteId,
      app: apps.get(application.appKey),
      services,
      publications: services.flatMap((service) => publicationsByService.get(service.id) ?? []),
      realityServices,
      realityPublications: realityServices.flatMap((service) => publicationsByService.get(service.id) ?? []),
      activeChange,
      deployment: deployments.find((deployment) => deployment.state === "succeeded" && deployment.operation !== "uninstall"),
      locked: Boolean(activeChange) || application.status !== "running",
    });
  }

  const groups = new Map<string, InstalledAppGroup>();
  for (const instance of instances.values()) {
    const { application, app } = instance;
    const threeXUI = application.appKey === threeXUIAppKey;
    const controller = instance.controller ? instances.get(instance.controller.id) : undefined;
    const id = threeXUI ? threeXUIAppKey : application.appKey;
    const group = groups.get(id);
    if (group) group.instances.push(instance);
    else groups.set(id, { id, appKey: application.appKey, app, controller, legacyControllers: [], instances: [instance] });
  }

  return [...groups.values()].map((group) => {
    const controller = group.appKey === threeXUIAppKey
      ? group.instances.map((instance) => instance.controller).find((application): application is Application => Boolean(application))
      : undefined;
    const controllerInstance = controller ? instances.get(controller.id) : group.controller;
    const legacyControllers = group.appKey === threeXUIAppKey
      ? group.instances.filter((instance) => instance.application.role === "master" && instance.application.id !== controllerInstance?.application.id)
      : [];
    const legacyControllerIDs = new Set(legacyControllers.map((instance) => instance.application.id));
    const convergence = group.appKey === threeXUIAppKey
      ? data.threeXUIControllerMigrations.find((migration) => migration.kind === "consolidate" && migration.state !== "ready" && legacyControllerIDs.has(migration.sourceApplicationId))
      : undefined;
    return {
      ...group,
      controller: controllerInstance,
      convergence,
      legacyControllers,
      instances: [...group.instances].sort((left, right) => {
        const controllerOrder = Number(right.application.id === controllerInstance?.application.id) - Number(left.application.id === controllerInstance?.application.id);
        const attentionOrder = Number(right.publications.some(publicationNeedsAttention) || right.services.some(serviceNeedsAttention)) - Number(left.publications.some(publicationNeedsAttention) || left.services.some(serviceNeedsAttention));
        return controllerOrder || attentionOrder || (left.agent?.name ?? left.application.nodeId).localeCompare(right.agent?.name ?? right.application.nodeId);
      }),
    };
  }).sort((left, right) => Number(Boolean(right.controller)) - Number(Boolean(left.controller)) || left.appKey.localeCompare(right.appKey) || left.id.localeCompare(right.id));
}
