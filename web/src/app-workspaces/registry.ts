import { lazy } from "react";
import { manifest as meridian } from "@/app-modules/meridian/manifest";
import type { AppUIManifest, AppUIModule } from "./types";

function bundledModule(manifest: AppUIManifest, load: () => Promise<AppUIModule>) {
  let pending: Promise<AppUIModule> | undefined;
  const resolve = () => pending ??= load();
  return {
    manifest,
    Workspace: lazy(() => resolve().then((module) => ({ default: module.Workspace }))),
    Manager: lazy(() => resolve().then((module) => ({ default: module.Manager }))),
  };
}

// Exact trusted package identity, never an app-controlled URL or script name.
const modules = [bundledModule(meridian, () => import("@/app-modules/meridian"))];
export function appWorkspace(appKey: string) {
  return modules.find((module) => module.manifest.appKey === appKey);
}
