import { Suspense } from "react";
import { Spinner } from "@/components/ui/spinner";
import { IPQualityProvider } from "@/views/IPQuality";
import { LandingProvider } from "@/views/LandingControls";
import { appWorkspace } from "./registry";
import type { AppUICapability, AppWorkspaceProps } from "./types";

export function AppWorkspaceHost(props: AppWorkspaceProps) {
  const module = appWorkspace(props.group.appKey);
  if (!module) return null;
  const capabilities = new Set<AppUICapability>(module.manifest.pages.flatMap((page) => [...page.capabilities]));
  // These select shared data providers, not authorization grants. The same
  // authenticated Center endpoints enforce permissions for every operation.
  return <IPQualityProvider enabled={capabilities.has("ip-quality")} agents={props.data.agents}>
    <LandingProvider enabled={capabilities.has("landing")} agents={props.data.agents}>
      <Suspense fallback={<Spinner />}><module.Workspace {...props} /></Suspense>
    </LandingProvider>
  </IPQualityProvider>;
}
