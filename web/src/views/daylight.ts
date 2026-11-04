import type { AppView } from "@/types";
import type { InstalledAppGroup } from "./installed-apps-model";

export const daylightAppKey = "vastora-official/daylight";
export const daylightModes = [
  { key: daylightAppKey, id: "gateway", zh: "受管网关", en: "Managed gateway" },
  { key: "vastora-official/daylight-https", id: "https", zh: "直连 HTTPS", en: "Direct HTTPS" },
] as const;

export function isDaylightAppKey(key: string) {
  return daylightModes.some((mode) => mode.key === key);
}

export function isDaylightCatalogApp(app: AppView) {
  return app.sourceId === "vastora-official" && app.key === `vastora-official/${app.app.id}` && isDaylightAppKey(app.key);
}

// Group presentation only; every instance retains its original recipe and ID.
export function daylightInstalledGroup(groups: InstalledAppGroup[]): InstalledAppGroup | undefined {
  const modes = daylightModes.flatMap((mode) => groups.filter((group) => group.appKey === mode.key));
  if (!modes.length) return undefined;
  return { ...modes[0], id: daylightAppKey, appKey: daylightAppKey, instances: modes.flatMap((group) => group.instances) };
}
