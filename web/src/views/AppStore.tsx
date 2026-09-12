import { useId } from "react";
import { PackagePlusIcon } from "lucide-react";
import type { AppData, AppView } from "../types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { eligibleAppNodes, installBlocker, isInstalledApplication, localized } from "./appAccess";
import { AppHostAccessNote, AppIdentityBadge, isOfficialProduct } from "./AppIdentity";
import { catalogInstallBlocked, copy } from "./shared";

export function AppStore({ data, language, onInstall }: { data: AppData; language: Language; onInstall: (app: AppView) => void }) {
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
    <p className="text-sm text-muted-foreground">{copy(language, "选择应用安装到节点，访问入口可在安装后添加。", "Choose an app for your nodes. Add access points after installation.")}</p>
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
        : copy(language, "请到设置中刷新应用目录，验证完成后即可安装。已安装应用不受影响。", "Refresh the app catalog in Settings before installing. Installed apps are unaffected.")}</AlertDescription>
    </Alert> : null}
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
      {data.apps.map((app) => {
        const canInstall = eligibleAppNodes(data, app.key).length > 0;
        return <AppStoreCard key={app.key} app={app} language={language}
          installedCount={installedCounts.get(app.key) ?? 0} canInstall={canInstall}
          blocker={canInstall ? "" : installBlocker(data, app.key, language)} onInstall={onInstall} />;
      })}
    </div>
  </div>;
}

export function AppStoreCard({ app, language, installedCount, canInstall: nodeAvailable, blocker: nodeBlocker, onInstall }: {
  app: AppView;
  language: Language;
  installedCount: number;
  canInstall: boolean;
  blocker: string;
  onInstall: (app: AppView) => void;
}) {
  const id = useId();
  const name = localized(app, language, "name");
  const catalogBlocked = catalogInstallBlocked(app);
  const canInstall = nodeAvailable && !catalogBlocked;
  const blocker = catalogBlocked
    ? copy(language, "请先在设置中刷新应用目录，再安装或升级。已安装应用不受影响。", "Refresh the app catalog in Settings before installing or upgrading. Installed apps are unaffected.")
    : nodeBlocker;
  return <Card aria-labelledby={`${id}-name`} className="min-w-0" role="article">
    <CardHeader className="gap-2">
      <CardTitle className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <h3 className="min-w-0 break-words" id={`${id}-name`}>{name}</h3>
        <AppIdentityBadge app={app} language={language} />
      </CardTitle>
      <CardDescription className="break-words">{localized(app, language, "description")}</CardDescription>
    </CardHeader>
    <CardContent className="flex flex-col gap-2">
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
        <span className="break-all">v{app.app.version}</span>
        <span aria-hidden="true">·</span>
        <span>{app.app.hostAccess ? copy(language, "主机应用", "Host app") : copy(language, "容器应用", "Container app")}</span>
      </p>
      <p aria-label={copy(language, "目录来源", "Catalog source")} className="break-all text-xs text-muted-foreground">{app.sourceId === "vastora-official" && app.key === `vastora-official/${app.app.id}`
        ? copy(language, "官方目录", "Official catalog")
        : copy(language, `第三方目录 · ${app.sourceId}`, `Third-party catalog · ${app.sourceId}`)}</p>
      {app.app.hostAccess && !isOfficialProduct(app) ? <AppHostAccessNote app={app} language={language} /> : null}
    </CardContent>
    <CardFooter className="mt-auto flex-col items-stretch gap-2 py-3">
      {blocker ? <p className="text-xs leading-5 text-muted-foreground" id={`${id}-blocker`}>{blocker}</p> : null}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">{installedCount
          ? copy(language, `已安装到 ${installedCount} 个节点`, `Installed on ${installedCount} node(s)`)
          : copy(language, "尚未安装", "Not installed")}</span>
        <Button aria-label={copy(language, `安装 ${name}`, `Install ${name}`)}
          aria-describedby={blocker ? `${id}-blocker` : undefined} disabled={!canInstall}
          onClick={() => onInstall(app)} size="sm" variant="secondary">
          <PackagePlusIcon aria-hidden="true" data-icon="inline-start" />{copy(language, "安装", "Install")}
        </Button>
      </div>
    </CardFooter>
  </Card>;
}
