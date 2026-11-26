import { useState } from "react";
import { CircleArrowUpIcon } from "lucide-react";
import type { AppData, Application, AppView } from "@/types";
import type { Language } from "@/translations";
import { AppIcon } from "@/components/desktop/AppIcon";
import { Button } from "@/components/ui/button";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { AppStoreTile } from "./AppStore";
import { AppIdentityBadge } from "./AppIdentity";
import { AppInstallationSummary } from "./apps/AppInstallationSummary";
import { planBatchUpgrade } from "./apps/BatchUpgradeSheet";
import { AccessStatus, ApplicationStatus } from "./apps/InstalledApplicationPrimitives";
import { eligibleAppNodes, installBlocker, localized } from "./appAccess";
import type { InstalledAppGroup, InstalledAppInstance } from "./installed-apps-model";
import { catalogInstallBlocked, copy } from "./shared";
import { daylightAppKey, daylightInstalledGroup, isDaylightCatalogApp } from "./daylight";

export function daylightMatches(query: string, data: AppData, groups: InstalledAppGroup[], language: Language) {
  return !query || ["Daylight", 
    ...data.apps.filter(isDaylightCatalogApp).flatMap((app) => [localized(app, language, "name"), localized(app, language, "description")]),
    ...groups.flatMap((group) => group.instances.flatMap((instance) => [instance.application.name, instance.agent?.name ?? ""])),
  ].some((value) => value.toLocaleLowerCase().includes(query));
}

export function DaylightStoreCard({ data, groups, language, onInstall, onOpen, onManage, onUpgrade, onBatchUpgrade, submittingUpgradeID }: {
  data: AppData;
  groups: InstalledAppGroup[];
  language: Language;
  onInstall: (app: AppView) => void;
  onOpen: (key: string) => void;
  onManage: (application: Application) => void;
  onUpgrade: (application: Application) => void;
  onBatchUpgrade?: (title: string, instances: InstalledAppInstance[]) => void;
  submittingUpgradeID?: string | null;
}) {
  const [open, setOpen] = useState(false);
  const officialApp = data.apps.find((app) => app.key === daylightAppKey && isDaylightCatalogApp(app));
  const group = daylightInstalledGroup(groups);
  const instances = [...(group?.instances ?? [])].sort((left, right) =>
    (left.agent?.name ?? left.application.nodeId).localeCompare(right.agent?.name ?? right.application.nodeId));
  const batchPlan = group && onBatchUpgrade && instances.length > 1 ? planBatchUpgrade("Daylight", instances, data, language) : null;
  const canInstall = Boolean(officialApp && !catalogInstallBlocked(officialApp) && eligibleAppNodes(data, officialApp.key).length);
  const blocker = officialApp && !canInstall ? catalogInstallBlocked(officialApp)
    ? officialApp.installBlockedReason || copy(language, "请先在控制面板刷新应用目录。", "Refresh the app catalog in Control Panel first.")
    : installBlocker(data, officialApp.key, language) : "";
  return <>
    <AppStoreTile appKey={daylightAppKey} name="Daylight" language={language} onDetails={() => setOpen(true)}
      meta={group ? <AppInstallationSummary group={group} language={language} compact byNode /> : copy(language, "尚未安装", "Not installed")}
      action={<div className="flex flex-wrap gap-2"><Button aria-label={copy(language, "管理 Daylight", "Manage Daylight")} onClick={() => setOpen(true)} size="sm" variant={group ? "outline" : "default"}>{copy(language, "管理", "Manage")}</Button>{group ? <Button aria-label={copy(language, "打开 Daylight", "Open Daylight")} onClick={() => onOpen(daylightAppKey)} size="sm">{copy(language, "打开", "Open")}</Button> : null}</div>} />
    <Sheet open={open} onOpenChange={setOpen}><SheetContent className="sm:max-w-xl">
      <SheetHeader><SheetTitle className="flex items-center gap-3"><AppIcon appKey={daylightAppKey} className="size-11" />Daylight{officialApp ? <AppIdentityBadge app={officialApp} language={language} /> : null}</SheetTitle><SheetDescription>{copy(language, "管理各主机的 Daylight 站点。", "Manage Daylight sites on each host.")}</SheetDescription></SheetHeader>
      <SheetBody className="gap-5">
        {group ? <><div className="flex flex-wrap items-start justify-between gap-3"><AppInstallationSummary group={group} language={language} byNode />{batchPlan && batchPlan.eligible.length > 0 ? <Button onClick={() => { setOpen(false); onBatchUpgrade?.("Daylight", instances); }} size="sm"><CircleArrowUpIcon aria-hidden="true" data-icon="inline-start" />{copy(language, `全部更新 (${batchPlan.eligible.length})`, `Update all (${batchPlan.eligible.length})`)}</Button> : null}</div><div className="divide-y rounded-xl border">{instances.map((instance) => <div className="flex flex-wrap items-center gap-3 p-4" key={instance.application.id}>
          <div className="min-w-0 flex-1"><p className="break-words text-sm font-medium">{instance.agent?.name ?? instance.application.nodeId}</p><p className="mt-1 text-xs text-muted-foreground">v{instance.application.installedVersion} · {instance.siteName}</p></div>
          <ApplicationStatus instance={instance} language={language} onUpgrade={(application) => { setOpen(false); onUpgrade(application); }} submittingUpgradeID={submittingUpgradeID} />
          <AccessStatus application={instance.application} nodeConnected={instance.agent?.connected ?? false} services={instance.services} publications={instance.publications} language={language} threeXUI={false} />
          <Button aria-label={copy(language, `管理 Daylight：${instance.agent?.name ?? instance.application.nodeId}`, `Manage Daylight: ${instance.agent?.name ?? instance.application.nodeId}`)} onClick={() => { setOpen(false); onManage(instance.application); }} size="sm" variant="outline">{copy(language, "管理", "Manage")}</Button>
        </div>)}</div></> : <p className="text-sm text-muted-foreground">{copy(language, "尚未安装", "Not installed")}</p>}
        {officialApp ? <div className="flex flex-col gap-2">
          <p className="text-xs text-muted-foreground">{copy(language, "目录版本", "Catalog version")} v{officialApp.app.version} · r{officialApp.app.packageRevision ?? 0}</p>
          <Button aria-describedby={blocker ? "daylight-install-blocker" : undefined} aria-label={copy(language, "安装 Daylight", "Install Daylight")} disabled={!canInstall} onClick={() => { setOpen(false); onInstall(officialApp); }} size="sm" variant="outline">{group ? copy(language, "安装到其他主机", "Install on another host") : copy(language, "安装", "Install")}</Button>
          {blocker ? <p className="text-xs text-muted-foreground" id="daylight-install-blocker">{blocker}</p> : null}
        </div> : null}
      </SheetBody>
    </SheetContent></Sheet>
  </>;
}
