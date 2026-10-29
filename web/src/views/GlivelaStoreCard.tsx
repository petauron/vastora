import { useState } from "react";
import type { AppData, Application, AppView } from "@/types";
import type { Language } from "@/translations";
import { AppIcon } from "@/components/desktop/AppIcon";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { AppStoreTile } from "./AppStore";
import { AppIdentityBadge } from "./AppIdentity";
import { AppInstallationSummary } from "./apps/AppInstallationSummary";
import { ApplicationStatus } from "./apps/InstalledApplicationPrimitives";
import { eligibleAppNodes, installBlocker, localized } from "./appAccess";
import type { InstalledAppGroup } from "./installed-apps-model";
import { catalogInstallBlocked, copy } from "./shared";

const roles = [
  { key: "vastora-official/meridian", id: "controller", zh: "控制端", en: "Controller" },
  { key: "vastora-official/glivela-entry", id: "entry", zh: "线路端", en: "Entry" },
  { key: "vastora-official/glivela-node", id: "landing", zh: "落地端", en: "Landing" },
] as const;

export const glivelaAppKeys: ReadonlySet<string> = new Set(roles.map((role) => role.key));

export function isGlivelaCatalogApp(app: AppView) {
  return app.sourceId === "vastora-official" && app.key === `vastora-official/${app.app.id}` && glivelaAppKeys.has(app.key);
}

export function glivelaMatches(query: string, data: AppData, groups: InstalledAppGroup[], language: Language) {
  if (!query) return true;
  const names = roles.flatMap((role) => [role.key, role.zh, role.en]);
  const catalog = data.apps.filter(isGlivelaCatalogApp);
  const installed = groups.filter((group) => glivelaAppKeys.has(group.appKey));
  return ["Glivela", ...names,
    ...catalog.flatMap((app) => [localized(app, language, "name"), localized(app, language, "description")]),
    ...installed.flatMap((group) => group.instances.map((instance) => instance.application.name)),
  ].some((value) => value.toLocaleLowerCase().includes(query));
}

export function GlivelaStoreCard({ data, groups, language, onInstall, onOpen, onManage, onUpgrade }: {
  data: AppData;
  groups: InstalledAppGroup[];
  language: Language;
  onInstall: (app: AppView) => void;
  onOpen: (key: string) => void;
  onManage: (application: Application) => void;
  onUpgrade: (application: Application) => void;
}) {
  const [open, setOpen] = useState(false);
  const officialApp = data.apps.find(isGlivelaCatalogApp);
  const controller = groups.find((group) => group.appKey === roles[0].key);
  const installedRoles = roles.flatMap((role) => {
    const group = groups.find((value) => value.appKey === role.key);
    return group ? [{ role, group }] : [];
  });
  const openController = () => { setOpen(false); onOpen(roles[0].key); };

  return <>
    <AppStoreTile appKey={roles[0].key} name="Glivela" language={language} onDetails={() => setOpen(true)}
      meta={installedRoles.length ? <>{installedRoles.map(({ role, group }) => <span className="block" key={role.key}>
        {copy(language, role.zh, role.en)} · <AppInstallationSummary group={group} language={language} compact />
      </span>)}</> : copy(language, "选择控制端、线路端或落地端", "Choose a controller, entry, or landing")}
      action={<div className="flex flex-wrap gap-2">
        <Button aria-label={copy(language, "管理 Glivela", "Manage Glivela")} onClick={() => setOpen(true)} size="sm" variant={controller ? "outline" : "default"}>{copy(language, "管理", "Manage")}</Button>
        {controller ? <Button aria-label={copy(language, "打开 Glivela", "Open Glivela")} onClick={openController} size="sm">{copy(language, "打开", "Open")}</Button> : null}
      </div>} />
    <Sheet open={open} onOpenChange={setOpen}><SheetContent className="sm:max-w-xl">
      <SheetHeader>
        <SheetTitle className="flex items-center gap-3"><AppIcon appKey={roles[0].key} className="size-11" />Glivela{officialApp ? <AppIdentityBadge app={officialApp} language={language} /> : null}</SheetTitle>
        <SheetDescription>{copy(language, "分别查看和管理控制端、线路端与落地端。", "View and manage controller, entry, and landing installations separately.")}</SheetDescription>
      </SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 pb-4">
        {roles.map((role) => {
          const label = copy(language, role.zh, role.en);
          const group = groups.find((value) => value.appKey === role.key);
          const app = data.apps.find((value) => value.key === role.key && isGlivelaCatalogApp(value));
          const canInstall = Boolean(app && !catalogInstallBlocked(app) && eligibleAppNodes(data, app.key).length);
          const blocker = app && !canInstall
            ? catalogInstallBlocked(app)
              ? app.installBlockedReason || copy(language, "请先在控制面板刷新应用目录。", "Refresh the app catalog in Control Panel first.")
              : installBlocker(data, app.key, language)
            : "";
          return <section aria-labelledby={`glivela-role-${role.id}`} className="flex flex-col gap-3" key={role.key}>
            <div className="flex flex-wrap items-start justify-between gap-2">
              <div><h3 className="font-medium" id={`glivela-role-${role.id}`}>{label}</h3>
                {app ? <p className="mt-1 text-xs text-muted-foreground">{copy(language, "目录版本", "Catalog version")} v{app.app.version} · r{app.app.packageRevision ?? 0}</p> : <p className="mt-1 text-xs text-muted-foreground">{copy(language, "目录暂未提供此角色", "This role is unavailable in the catalog")}</p>}
              </div>
              {role.id === "controller" && controller ? <Button onClick={openController} size="sm" variant="outline">{copy(language, "打开 Glivela", "Open Glivela")}</Button> : null}
            </div>
            {group ? <>
              <AppInstallationSummary group={group} language={language} />
              <div className="divide-y rounded-xl border">{group.instances.map((instance) => <div className="flex flex-wrap items-center gap-3 p-4" key={instance.application.id}>
                <div className="min-w-0 flex-1"><p className="break-words text-sm font-medium">{instance.agent?.name ?? instance.application.nodeId}</p><p className="mt-1 text-xs text-muted-foreground">v{instance.application.installedVersion} · {instance.siteName}</p></div>
                <ApplicationStatus instance={instance} language={language} onUpgrade={(application) => { setOpen(false); onUpgrade(application); }} />
                <Button aria-label={copy(language, `管理${label}：${instance.agent?.name ?? instance.application.nodeId}`, `Manage ${label}: ${instance.agent?.name ?? instance.application.nodeId}`)} onClick={() => { setOpen(false); onManage(instance.application); }} variant="outline" size="sm">{copy(language, "管理", "Manage")}</Button>
              </div>)}</div>
            </> : <p className="text-sm text-muted-foreground">{copy(language, "尚未安装", "Not installed")}</p>}
            {app ? <div className="flex flex-col gap-2">
              <Button aria-describedby={blocker ? `glivela-install-${role.id}-blocker` : undefined} aria-label={copy(language, `安装${label}`, `Install ${label}`)} disabled={!canInstall} onClick={() => { setOpen(false); onInstall(app); }} size="sm" variant="outline">{group ? copy(language, "安装到其他主机", "Install on another host") : copy(language, "安装", "Install")}</Button>
              {blocker ? <p className="text-xs text-muted-foreground" id={`glivela-install-${role.id}-blocker`}>{blocker}</p> : null}
            </div> : null}
          </section>;
        })}
      </div>
    </SheetContent></Sheet>
  </>;
}
