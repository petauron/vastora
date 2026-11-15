import type { AppView } from "@/types";
import type { InstalledAppGroup } from "./installed-apps-model";

export const daylightAppKey = "vastora-official/daylight";
const daylightAppKeys: ReadonlySet<string> = new Set([daylightAppKey, "vastora-official/daylight-https"]);

export function isDaylightAppKey(key: string) {
  return daylightAppKeys.has(key);
}

export function isDaylightCatalogApp(app: AppView) {
  return app.sourceId === "vastora-official" && app.key === `vastora-official/${app.app.id}` && isDaylightAppKey(app.key);
}

// Group presentation only; every instance retains its original recipe and ID.
export function daylightInstalledGroup(groups: InstalledAppGroup[]): InstalledAppGroup | undefined {
  const modes = groups.filter((group) => isDaylightAppKey(group.appKey));
  if (!modes.length) return undefined;
  return { ...modes[0], id: daylightAppKey, appKey: daylightAppKey, instances: modes.flatMap((group) => group.instances) };
}
