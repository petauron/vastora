import { useState } from "react";
import { AppWindowIcon } from "lucide-react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { AppWorkspaceHost } from "@/app-workspaces/AppWorkspaceHost";
import { appWorkspace } from "@/app-workspaces/registry";
import type { AppWorkspaceProps } from "@/app-workspaces/types";
import { DefaultInstalledAppWorkspace } from "./DefaultInstalledAppWorkspace";
import { IPQualityProvider } from "./IPQuality";
import { LandingProvider } from "./LandingControls";
import { localized } from "./appAccess";
import { copy } from "./shared";
import { threeXUIAppKey, type InstalledAppGroup } from "./installed-apps-model";

type InstalledAppsProps = Omit<AppWorkspaceProps, "group" | "showSite"> & { groups: InstalledAppGroup[] };

export function InstalledApps({ groups, ...props }: InstalledAppsProps) {
  const [selectedID, setSelectedID] = useState(groups[0]?.id ?? "");
  const selected = groups.find((group) => group.id === selectedID) ?? groups[0];
  const showSite = new Set(groups.flatMap((group) => group.instances.map((instance) => instance.application.siteId))).size > 1;
  return <Tabs value={selected?.id ?? ""} onValueChange={(value) => { if (typeof value === "string") setSelectedID(value); }} className="apps-chooser gap-4">
    <div className="max-w-full overflow-x-auto pb-1"><TabsList variant="line" aria-label={copy(props.language, "已安装的应用", "Installed applications")}>
      {groups.map((group) => <TabsTrigger value={group.id} key={group.id}><AppWindowIcon aria-hidden="true" />{group.app ? localized(group.app, props.language, "name") : group.instances[0].application.name}<span className="text-xs text-muted-foreground tabular-nums">{group.instances.length}</span></TabsTrigger>)}
    </TabsList></div>
    {groups.map((group) => {
      const module = appWorkspace(group.appKey);
      const legacy = group.appKey === threeXUIAppKey;
      return <TabsContent value={group.id} key={group.id}>
        {module ? <AppWorkspaceHost {...props} group={group} showSite={showSite} />
          : <IPQualityProvider enabled={legacy && group.id === selected?.id} agents={props.data.agents}><LandingProvider enabled={legacy && group.id === selected?.id} agents={props.data.agents}><DefaultInstalledAppWorkspace {...props} group={group} showSite={showSite} /></LandingProvider></IPQualityProvider>}
      </TabsContent>;
    })}
  </Tabs>;
}
