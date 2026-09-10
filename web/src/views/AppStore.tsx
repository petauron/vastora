import { useId } from "react";
import { PackagePlusIcon } from "lucide-react";
import type { AppData, AppView } from "../types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { eligibleAppNodes, installBlocker, isInstalledApplication, localized } from "./appAccess";
import { AppHostAccessNote, AppIdentityBadge, isOfficialProduct } from "./AppIdentity";
import { copy } from "./shared";

export function AppStore({ data, language, onInstall }: { data: AppData; language: Language; onInstall: (app: AppView) => void }) {
  const installedCounts = new Map<string, number>();
  for (const application of data.applications) {
    if (isInstalledApplication(application)) {
      installedCounts.set(application.appKey, (installedCounts.get(application.appKey) ?? 0) + 1);
    }
  }

  return <div className="flex flex-col gap-4">
    <h2 className="sr-only">{copy(language, "应用商店", "App Store")}</h2>
    <p className="text-sm text-muted-foreground">{copy(language, "选择应用安装到节点，访问入口可在安装后添加。", "Choose an app for your nodes. Add access points after installation.")}</p>
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

export function AppStoreCard({ app, language, installedCount, canInstall, blocker, onInstall }: {
  app: AppView;
  language: Language;
  installedCount: number;
  canInstall: boolean;
  blocker: string;
  onInstall: (app: AppView) => void;
}) {
  const id = useId();
  const name = localized(app, language, "name");
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
