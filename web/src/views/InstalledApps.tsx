import { useEffect, useState } from "react";
import { AppWindowIcon } from "lucide-react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { OfficialAppWorkspaceHost } from "@/app-workspaces/OfficialAppWorkspaceHost";
import type { AppWorkspaceProps } from "@/app-workspaces/types";
import { DefaultInstalledAppWorkspace } from "./DefaultInstalledAppWorkspace";
import { PulseWorkspace } from "./PulseWorkspace";
import { IPQualityProvider } from "./IPQuality";
import { LandingProvider } from "./LandingControls";
import { localized } from "./appAccess";
import { copy } from "./shared";
import { threeXUIAppKey, type InstalledAppGroup } from "./installed-apps-model";

type InstalledAppsProps = Omit<AppWorkspaceProps, "group" | "showSite"> & { groups: InstalledAppGroup[] };

export function InstalledApps({ groups, ...props }: InstalledAppsProps) {
  const [selectedID, setSelectedID] = useState(groups[0]?.id ?? "");
  useEffect(() => {
    if (props.managerApplication) setSelectedID("vastora-official/meridian");
  }, [props.managerApplication]);
  const selected = groups.find((group) => group.id === selectedID) ?? groups[0];
  const showSite = new Set(groups.flatMap((group) => group.instances.map((instance) => instance.application.siteId))).size > 1;
  return <Tabs value={selected?.id ?? ""} onValueChange={(value) => { if (typeof value === "string") setSelectedID(value); }} className="apps-chooser min-w-0 gap-4">
    <div className="max-w-full overflow-x-auto pb-1"><TabsList variant="line" aria-label={copy(props.language, "已安装的应用", "Installed applications")}>
      {groups.map((group) => <TabsTrigger value={group.id} key={group.id}><AppWindowIcon aria-hidden="true" />{group.app ? localized(group.app, props.language, "name") : group.instances[0].application.name}<span className="text-xs text-muted-foreground tabular-nums">{group.instances.length}</span></TabsTrigger>)}
    </TabsList></div>
    {groups.map((group) => {
      const legacy = group.appKey === threeXUIAppKey;
      return <TabsContent value={group.id} key={group.id}>
        {group.appKey === "vastora-official/meridian" ? <OfficialAppWorkspaceHost {...props} group={group} showSite={showSite} />
          : group.appKey === "vastora-official/pulse" ? <PulseWorkspace {...props} group={group} showSite={showSite} />
          : <IPQualityProvider enabled={legacy && group.id === selected?.id} agents={props.data.agents}><LandingProvider enabled={legacy && group.id === selected?.id} agents={props.data.agents}><DefaultInstalledAppWorkspace {...props} group={group} showSite={showSite} /></LandingProvider></IPQualityProvider>}
      </TabsContent>;
    })}
  </Tabs>;
}
