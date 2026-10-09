import { OfficialAppWorkspaceHost } from "@/app-workspaces/OfficialAppWorkspaceHost";
import type { AppWorkspaceProps } from "@/app-workspaces/types";
import { DefaultInstalledAppWorkspace } from "./DefaultInstalledAppWorkspace";
import { PulseWorkspace } from "./PulseWorkspace";
import { IPQualityProvider } from "./IPQuality";
import { LandingProvider } from "./LandingControls";
import { threeXUIAppKey } from "./installed-apps-model";

export function InstalledApps(props: Omit<AppWorkspaceProps, "showSite">) {
  const { group } = props;
  const showSite = new Set(group.instances.map((instance) => instance.application.siteId)).size > 1;
  const legacy = group.appKey === threeXUIAppKey;
  if (group.appKey === "vastora-official/meridian") return <OfficialAppWorkspaceHost {...props} showSite={showSite} />;
  if (group.appKey === "vastora-official/pulse") return <PulseWorkspace {...props} showSite={showSite} />;
  return <IPQualityProvider enabled={legacy} agents={props.data.agents}><LandingProvider enabled={legacy} agents={props.data.agents}><DefaultInstalledAppWorkspace {...props} showSite={showSite} /></LandingProvider></IPQualityProvider>;
}
