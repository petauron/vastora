import { useState } from "react";
import type { AppData, Application, AppView } from "@/types";
import type { Language } from "@/translations";
import { AppIcon } from "@/components/desktop/AppIcon";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { AppStoreTile } from "./AppStore";
import { AppIdentityBadge } from "./AppIdentity";
import { AppInstallationSummary } from "./apps/AppInstallationSummary";
import { AccessStatus, ApplicationStatus } from "./apps/InstalledApplicationPrimitives";
import { eligibleAppNodes, installBlocker, localized } from "./appAccess";
import type { InstalledAppGroup } from "./installed-apps-model";
import { catalogInstallBlocked, copy } from "./shared";
import { daylightAppKey, daylightModes, isDaylightCatalogApp } from "./daylight";

export function daylightMatches(query: string, data: AppData, groups: InstalledAppGroup[], language: Language) {
  return !query || ["Daylight", ...daylightModes.flatMap((mode) => [mode.key, mode.zh, mode.en]),
    ...data.apps.filter(isDaylightCatalogApp).flatMap((app) => [localized(app, language, "name"), localized(app, language, "description")]),
    ...groups.flatMap((group) => group.instances.flatMap((instance) => [instance.application.name, instance.agent?.name ?? ""])),
  ].some((value) => value.toLocaleLowerCase().includes(query));
}

export function DaylightStoreCard({ data, groups, language, onInstall, onOpen, onManage, onUpgrade }: {
  data: AppData;
  groups: InstalledAppGroup[];
  language: Language;
  onInstall: (app: AppView) => void;
  onOpen: (key: string) => void;
  onManage: (application: Application) => void;
  onUpgrade: (application: Application) => void;
}) {
  const [open, setOpen] = useState(false);
  const officialApp = data.apps.find(isDaylightCatalogApp);
  return <>
    <AppStoreTile appKey={daylightAppKey} name="Daylight" language={language} onDetails={() => setOpen(true)}
      meta={groups.length ? <>{daylightModes.map((mode) => {
        const group = groups.find((value) => value.appKey === mode.key);
        return group ? <span className="block" key={mode.key}>{copy(language, mode.zh, mode.en)} · <AppInstallationSummary group={group} language={language} compact /></span> : null;
      })}</> : copy(language, "选择受管网关或直连 HTTPS", "Choose managed gateway or direct HTTPS")}
      action={<div className="flex flex-wrap gap-2"><Button aria-label={copy(language, "管理 Daylight", "Manage Daylight")} onClick={() => setOpen(true)} size="sm" variant={groups.length ? "outline" : "default"}>{copy(language, "管理", "Manage")}</Button>{groups.length ? <Button aria-label={copy(language, "打开 Daylight", "Open Daylight")} onClick={() => onOpen(daylightAppKey)} size="sm">{copy(language, "打开", "Open")}</Button> : null}</div>} />
    <Sheet open={open} onOpenChange={setOpen}><SheetContent className="sm:max-w-xl">
      <SheetHeader><SheetTitle className="flex items-center gap-3"><AppIcon appKey={daylightAppKey} className="size-11" />Daylight{officialApp ? <AppIdentityBadge app={officialApp} language={language} /> : null}</SheetTitle><SheetDescription>{copy(language, "按发布方式管理各主机的站点。", "Manage each host's website by publication mode.")}</SheetDescription></SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 pb-4">{daylightModes.map((mode) => {
        const group = groups.find((value) => value.appKey === mode.key);
        const app = data.apps.find((value) => value.key === mode.key && isDaylightCatalogApp(value));
        const canInstall = Boolean(app && !catalogInstallBlocked(app) && eligibleAppNodes(data, app.key).length);
        const blocker = app && !canInstall ? catalogInstallBlocked(app)
          ? app.installBlockedReason || copy(language, "请先在控制面板刷新应用目录。", "Refresh the app catalog in Control Panel first.")
          : installBlocker(data, app.key, language) : "";
        const label = copy(language, mode.zh, mode.en);
        return <section aria-labelledby={`daylight-mode-${mode.id}`} className="flex flex-col gap-3" key={mode.key}>
          <div><h3 className="font-medium" id={`daylight-mode-${mode.id}`}>{label}</h3>{app ? <p className="mt-1 text-xs text-muted-foreground">{copy(language, "目录版本", "Catalog version")} v{app.app.version} · r{app.app.packageRevision ?? 0}</p> : null}</div>
          {group ? <><AppInstallationSummary group={group} language={language} /><div className="divide-y rounded-xl border">{group.instances.map((instance) => <div className="flex flex-wrap items-center gap-3 p-4" key={instance.application.id}>
            <div className="min-w-0 flex-1"><p className="break-words text-sm font-medium">{instance.agent?.name ?? instance.application.nodeId}</p><p className="mt-1 text-xs text-muted-foreground">v{instance.application.installedVersion} · {instance.siteName}</p></div>
            <ApplicationStatus instance={instance} language={language} onUpgrade={(application) => { setOpen(false); onUpgrade(application); }} />
            <AccessStatus services={instance.services} publications={instance.publications} language={language} threeXUI={false} />
            <Button aria-label={copy(language, `管理${label}：${instance.agent?.name ?? instance.application.nodeId}`, `Manage ${label}: ${instance.agent?.name ?? instance.application.nodeId}`)} onClick={() => { setOpen(false); onManage(instance.application); }} size="sm" variant="outline">{copy(language, "管理", "Manage")}</Button>
          </div>)}</div></> : <p className="text-sm text-muted-foreground">{copy(language, "尚未安装", "Not installed")}</p>}
          {app ? <div className="flex flex-col gap-2"><Button aria-describedby={blocker ? `daylight-install-${mode.id}-blocker` : undefined} aria-label={copy(language, `安装${label}`, `Install ${label}`)} disabled={!canInstall} onClick={() => { setOpen(false); onInstall(app); }} size="sm" variant="outline">{group ? copy(language, "安装到其他主机", "Install on another host") : copy(language, "安装", "Install")}</Button>{blocker ? <p className="text-xs text-muted-foreground" id={`daylight-install-${mode.id}-blocker`}>{blocker}</p> : null}</div> : null}
        </section>;
      })}</div>
    </SheetContent></Sheet>
  </>;
}
