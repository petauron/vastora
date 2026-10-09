import { useId, useState, type ReactNode } from "react";
import { PackagePlusIcon } from "lucide-react";
import type { AppData, AppView } from "../types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Card, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { eligibleAppNodes, installBlocker, isInstalledApplication, localized } from "./appAccess";
import { AppHostAccessNote, AppIdentityBadge, isOfficialProduct } from "./AppIdentity";
import { catalogInstallBlocked, copy } from "./shared";
import { isBackgroundApplication } from "./applicationLaunch";
import { AppIcon } from "@/components/desktop/AppIcon";

export function AppStore({ data, language, onInstall, query = "", renderInstalledActions, renderInstalledSummary, renderInstalledStatus, renderInstalledDetailActions }: { data: AppData; language: Language; onInstall: (app: AppView) => void; query?: string; renderInstalledActions?: (key: string) => ReactNode; renderInstalledSummary?: (key: string) => ReactNode; renderInstalledStatus?: (key: string) => ReactNode; renderInstalledDetailActions?: (key: string) => ReactNode }) {
  const officialSource = data.sources.find((source) => source.id === "vastora-official");
  const officialUnavailable = officialSource && (officialSource.status === "pending" || officialSource.status === "failed" || officialSource.status === "expired" || !data.apps.some((app) => app.sourceId === officialSource.id));
  const installedCounts = new Map<string, number>();
  for (const application of data.applications) {
    if (isInstalledApplication(application)) {
      installedCounts.set(application.appKey, (installedCounts.get(application.appKey) ?? 0) + 1);
    }
  }

  return <div className="flex flex-col gap-4">
    <h2 className="sr-only">{copy(language, "应用商店", "App Store")}</h2>
    {data.catalogSourcesError || officialUnavailable || data.apps.length === 0 ? <Alert role="status">
      <AlertTitle>{data.catalogSourcesError
        ? copy(language, "暂时无法读取目录状态", "Catalog status is unavailable")
        : officialSource?.status === "pending"
        ? copy(language, "官方目录等待首次验证", "Official catalog awaiting verification")
        : officialSource?.status === "expired"
        ? copy(language, "官方目录需要刷新", "Official catalog needs a refresh")
        : officialSource
        ? copy(language, "官方目录暂不可用", "Official catalog unavailable")
        : copy(language, "应用目录暂不可用", "App catalog unavailable")}</AlertTitle>
      <AlertDescription>{data.catalogSourcesError
        ? copy(language, "请刷新页面重试。已安装应用仍可管理。", "Refresh the page to retry. Installed apps remain manageable.")
        : copy(language, "请到控制面板刷新应用目录，验证完成后即可安装。已安装应用不受影响。", "Refresh the app catalog in Control Panel before installing. Installed apps are unaffected.")}</AlertDescription>
    </Alert> : null}
    <div className="store-app-grid">
      {data.apps.filter((app) => !query || [localized(app, language, "name"), localized(app, language, "description"), app.sourceId, app.key].some((text) => text.toLocaleLowerCase().includes(query))).map((app) => {
        const canInstall = eligibleAppNodes(data, app.key).length > 0;
        return <AppStoreCard key={app.key} app={app} language={language}
          status={renderInstalledStatus?.(app.key)} detailActions={renderInstalledDetailActions?.(app.key)} installedCount={installedCounts.get(app.key) ?? 0} summary={renderInstalledSummary?.(app.key)} canInstall={canInstall}
          blocker={canInstall ? "" : installBlocker(data, app.key, language)} onInstall={onInstall} actions={installedCounts.has(app.key) ? renderInstalledActions?.(app.key) : undefined} />;
      })}
    </div>
  </div>;
}

export function AppStoreTile({ appKey, name, language, meta, action, onDetails }: {
  appKey: string; name: string; language: Language; meta: ReactNode; action: ReactNode; onDetails: () => void;
}) {
  const id = useId();
  return <Card aria-labelledby={id} className="store-app-card min-w-0" role="article">
    <CardHeader>
      <AppIcon appKey={appKey} className="size-12" />
      <CardTitle><h3 id={id}><button className="store-app-title" onClick={onDetails} title={name} aria-label={copy(language, `查看 ${name} 详情`, `View ${name} details`)}>{name}</button></h3></CardTitle>
      <CardDescription className="store-app-meta">{meta}</CardDescription>
    </CardHeader>
    <CardFooter>{action}</CardFooter>
  </Card>;
}

export function AppStoreCard({ app, language, installedCount, canInstall: nodeAvailable, blocker: nodeBlocker, onInstall, actions, summary, status, detailActions }: {
  app: AppView;
  language: Language;
  installedCount: number;
  canInstall: boolean;
  blocker: string;
  onInstall: (app: AppView) => void;
  actions?: ReactNode;
  summary?: ReactNode;
  status?: ReactNode;
  detailActions?: ReactNode;
}) {
  const id = useId();
  const [detailsOpen, setDetailsOpen] = useState(false);
  const name = localized(app, language, "name");
  const catalogBlocked = catalogInstallBlocked(app);
  const canInstall = nodeAvailable && !catalogBlocked;
  const blocker = catalogBlocked
    ? app.installBlockedReason || copy(language, "请先在控制面板刷新应用目录，再安装或升级。已安装应用不受影响。", "Refresh the app catalog in Control Panel before installing or upgrading. Installed apps are unaffected.")
    : nodeBlocker;
  const officialCatalog = app.sourceId === "vastora-official" && app.key === `vastora-official/${app.app.id}`;
  const installAction = <Button aria-label={copy(language, `安装 ${name}`, `Install ${name}`)} aria-describedby={blocker ? `${id}-blocker` : undefined} disabled={!canInstall} onClick={() => { setDetailsOpen(false); onInstall(app); }} size="sm" variant="secondary"><PackagePlusIcon aria-hidden="true" data-icon="inline-start" />{copy(language, "安装", "Install")}</Button>;
  const installation = installedCount ? copy(language, `已安装到 ${installedCount} 个节点`, `Installed on ${installedCount} node(s)`) : copy(language, "尚未安装", "Not installed");
  return <>
    <AppStoreTile appKey={app.key} name={name} language={language} onDetails={() => setDetailsOpen(true)} action={actions ?? installAction} meta={<>
      {!officialCatalog ? <span aria-label={copy(language, "目录来源", "Catalog source")} title={app.sourceId}>{app.sourceId} · </span> : null}
      {!actions && blocker ? <span id={`${id}-blocker`} title={blocker}>{blocker}</span> : status ?? <span>{installedCount ? installation : localized(app, language, "description")}</span>}
    </>} />
    <Sheet open={detailsOpen} onOpenChange={setDetailsOpen}><SheetContent className="sm:max-w-xl">
      <SheetHeader><SheetTitle className="flex items-center gap-3"><AppIcon appKey={app.key} className="size-12" />{name}<AppIdentityBadge app={app} language={language} /></SheetTitle><SheetDescription>{localized(app, language, "description")}</SheetDescription></SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-5 pb-5">
        {summary}
        <dl className="mac-settings-group">
          <div className="mac-settings-row"><dt>{copy(language, "目录版本", "Catalog version")}</dt><dd>v{app.app.version} · r{app.app.packageRevision ?? 0}</dd></div>
          <div className="mac-settings-row"><dt>{copy(language, "目录来源", "Catalog source")}</dt><dd>{officialCatalog ? copy(language, "官方目录", "Official catalog") : copy(language, `第三方目录 · ${app.sourceId}`, `Third-party catalog · ${app.sourceId}`)}</dd></div>
          <div className="mac-settings-row"><dt>{copy(language, "类型", "Type")}</dt><dd>{app.app.hostAccess ? copy(language, "主机应用", "Host app") : copy(language, "容器应用", "Container app")}</dd></div>
          <div className="mac-settings-row"><dt>{copy(language, "安装情况", "Installations")}</dt><dd>{installation}</dd></div>
        </dl>
        {isBackgroundApplication(app) ? <p className="text-xs text-muted-foreground">{copy(language, "后台服务 · 在应用商店管理", "Background service · manage in App Store")}</p> : null}
        {app.app.hostAccess && !isOfficialProduct(app) ? <AppHostAccessNote app={app} language={language} /> : null}
        {blocker ? <p className="text-sm text-muted-foreground">{blocker}</p> : null}
      </div>
      <SheetFooter><div className="flex flex-wrap gap-2" onClick={(event) => { if ((event.target as HTMLElement).closest("button:not(:disabled), a")) setDetailsOpen(false); }}>{detailActions ?? actions ?? installAction}</div></SheetFooter>
    </SheetContent></Sheet>
  </>;
}
