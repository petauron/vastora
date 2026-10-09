import type { AppData } from "@/types";
import type { Language } from "@/translations";
import { isInstalledApplication, localized, pulsePrivateAccess, secureDashboardURL } from "./appAccess";

export type DesktopApplication = { key: string; name: string; count: number; url?: string };

export function desktopApplications(data: AppData, language: Language): DesktopApplication[] {
  const catalog = new Map(data.apps.map((app) => [app.key, app]));
  const groups = new Map<string, DesktopApplication>();
  for (const application of data.applications.filter(isInstalledApplication)) {
    const group = groups.get(application.appKey);
    if (group) group.count++;
    else {
      const app = catalog.get(application.appKey);
      groups.set(application.appKey, { key: application.appKey, name: app ? localized(app, language, "name") : application.name, count: 1, url: applicationLaunchURL(data, application.appKey) });
    }
  }
  return [...groups.values()].sort((a, b) => a.name.localeCompare(b.name, language));
}

// A single ready management entry can open directly. Multiple installations or
// web entries need the application's workspace so the user can choose a host.
export function applicationLaunchURL(data: AppData, appKey: string): string | undefined {
  if (appKey === "vastora-official/meridian" || appKey === "vastora-official/3x-ui") return undefined;
  if (appKey === "vastora-official/pulse") {
    const publication = pulsePrivateAccess(data);
    return publication && !publication.actionRequired && !publication.lastError ? secureDashboardURL(publication.accessUrl) : undefined;
  }
  const applications = data.applications.filter((app) => app.appKey === appKey && app.installedVersion && app.status === "running");
  if (applications.length !== 1) return undefined;
  const services = new Set(data.services.filter((service) => service.applicationId === applications[0].id && service.management && ["http", "https"].includes(service.protocol) && ["ready", "publishing"].includes(service.status)).map((service) => service.id));
  const entries = data.publications.filter((publication) => services.has(publication.serviceId) && publication.status === "ready" && !publication.actionRequired && !publication.lastError);
  return entries.length === 1 ? secureDashboardURL(entries[0].accessUrl) : undefined;
}
