import { useState } from "react";
import { ArrowUpCircleIcon, DownloadIcon, Grid2X2Icon, SearchIcon, Settings2Icon } from "lucide-react";
import type { AppData, Application, AppView } from "@/types";
import type { Language } from "@/translations";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { AppIcon } from "@/components/desktop/AppIcon";
import { AppStore, AppStoreCard } from "./AppStore";
import { ApplicationStatus } from "./apps/InstalledApplicationPrimitives";
import { eligibleAppNodes, installBlocker, localized } from "./appAccess";
import { catalogInstallBlocked, copy } from "./shared";
import type { InstalledAppGroup } from "./installed-apps-model";
import { applicationLaunchURL } from "./applicationLaunch";

type Section = "installed" | "all" | "updates";
export function ApplicationStore({ data, groups, language, onInstall, onOpen, onManage, onUpgrade, onSettings }: {
  data: AppData;
  groups: InstalledAppGroup[];
  language: Language;
  onInstall: (app: AppView) => void;
  onOpen: (key: string) => void;
  onManage: (application: Application) => void;
  onUpgrade: (application: Application) => void;
  onSettings: () => void;
}) {
  const [section, setSection] = useState<Section>(() => groups.length ? "installed" : "all");
  const [query, setQuery] = useState("");
  const [managedGroupKey, setManagedGroupKey] = useState<string | null>(null);
  const updates = groups.filter((group) => group.instances.some((instance) => instance.application.updateAvailable));
  const search = query.trim().toLocaleLowerCase();
  const name = (group: InstalledAppGroup) => group.app ? localized(group.app, language, "name") : group.instances[0].application.name;
  const shown = (section === "updates" ? updates : groups).filter((group) => !search || [name(group), group.appKey, group.app ? localized(group.app, language, "description") : ""].some((value) => value.toLocaleLowerCase().includes(search)));
  const matchingCatalog = data.apps.filter((app) => !search || [localized(app, language, "name"), localized(app, language, "description"), app.sourceId].some((value) => value.toLocaleLowerCase().includes(search)));
  const managedGroup = groups.find((group) => group.id === managedGroupKey);
  const managedApp = managedGroup?.app;
  const canInstallMore = managedApp && !catalogInstallBlocked(managedApp) && eligibleAppNodes(data, managedApp.key).length > 0;
  const sections = [
    { id: "installed" as const, icon: DownloadIcon, label: copy(language, "已安装", "Installed"), count: groups.length },
    { id: "all" as const, icon: Grid2X2Icon, label: copy(language, "所有应用", "All apps"), count: data.apps.length },
    { id: "updates" as const, icon: ArrowUpCircleIcon, label: copy(language, "可更新", "Updates"), count: updates.length },
  ];
  const actions = (group: InstalledAppGroup) => {
    const url = applicationLaunchURL(data, group.appKey);
    return <div className="flex flex-wrap gap-2">
    {url ? <Button className="min-h-9" nativeButton={false} render={<a href={url} target="_blank" rel="noreferrer" />} aria-label={copy(language, `在新标签页打开 ${name(group)}`, `Open ${name(group)} in a new tab`)} size="sm" variant="secondary">{copy(language, "打开 ↗", "Open ↗")}</Button> : <Button className="min-h-9" onClick={() => onOpen(group.appKey)} size="sm" variant="secondary">{copy(language, "打开", "Open")}</Button>}
    <Button className="min-h-9" onClick={() => setManagedGroupKey(group.id)} size="sm" variant={group.instances.some((instance) => instance.application.updateAvailable) ? "default" : "outline"}>{group.instances.some((instance) => instance.application.updateAvailable) ? copy(language, "查看更新", "View updates") : copy(language, "管理", "Manage")}</Button>
  </div>;
  };
  return <div className="store-layout">
    <nav aria-label={copy(language, "应用商店分类", "App Store categories")} className="store-navigation">
      <div className="flex gap-1 md:flex-col">{sections.map(({ id, icon: Icon, label, count }) => <Button aria-current={section === id ? "page" : undefined} className="store-navigation-item min-h-11" key={id} onClick={() => setSection(id)} variant="ghost"><Icon /><span>{label}</span><span className="ml-auto text-xs tabular-nums opacity-60">{count}</span></Button>)}</div>
      <Button className="mt-auto min-h-11 justify-start gap-3" onClick={onSettings} variant="ghost"><Settings2Icon /><span>{copy(language, "目录设置", "Catalog settings")}</span></Button>
    </nav>
    <div className="store-content">
      <header className="mb-6 flex flex-wrap items-center justify-between gap-4">
        <div><h1 className="text-xl font-semibold tracking-tight">{sections.find((item) => item.id === section)!.label}</h1><p className="mt-1 text-xs text-muted-foreground" role="status">{copy(language, `${section === "all" ? matchingCatalog.length : shown.length} 个应用`, `${section === "all" ? matchingCatalog.length : shown.length} applications`)}</p></div>
        <InputGroup className="h-10 w-full sm:w-64"><InputGroupAddon><SearchIcon /></InputGroupAddon><InputGroupInput aria-label={copy(language, "搜索应用商店", "Search App Store")} placeholder={copy(language, "搜索应用…", "Search apps…")} type="search" value={query} onChange={(event) => setQuery(event.target.value)} /></InputGroup>
      </header>
      {section === "all" ? <AppStore data={data} language={language} onInstall={onInstall} query={search} renderInstalledActions={(key) => { const group = groups.find((value) => value.appKey === key); return group ? actions(group) : null; }} /> : <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">{shown.map((group) => group.app
        ? <AppStoreCard key={group.id} app={group.app} language={language} installedCount={group.instances.length} canInstall={false} blocker="" onInstall={onInstall} actions={actions(group)} />
        : <Card className="store-app-card" key={group.id}><CardHeader><AppIcon appKey={group.appKey} className="mb-3 size-14" /><CardTitle><h3>{name(group)}</h3></CardTitle></CardHeader><CardContent><p className="text-sm text-muted-foreground">{copy(language, "应用已安装，当前目录中没有此应用。", "Installed app is absent from the current catalog.")}</p></CardContent><CardFooter className="mt-auto flex-wrap justify-between gap-3"><span className="text-xs text-muted-foreground">{copy(language, `${group.instances.length} 台主机`, `${group.instances.length} hosts`)}</span>{actions(group)}</CardFooter></Card>)}</div>}
      {(section === "all" ? !matchingCatalog.length && Boolean(search) : !shown.length) ? <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed px-5 py-14 text-center"><AppIcon appKey="apps" className="size-12" /><p className="text-sm text-muted-foreground">{search ? copy(language, "没有匹配的应用", "No matching apps") : section === "updates" ? copy(language, "当前没有可用的应用更新", "No app updates available") : copy(language, "还没有安装应用", "No apps installed")}</p><Button onClick={() => { if (search) setQuery(""); else setSection("all"); }} variant="outline">{search ? copy(language, "清除搜索", "Clear search") : copy(language, "浏览所有应用", "Browse all apps")}</Button></div> : null}
    </div>
    <Sheet open={Boolean(managedGroup)} onOpenChange={(open) => { if (!open) setManagedGroupKey(null); }}><SheetContent className="sm:max-w-xl">
      {managedGroup ? <><SheetHeader><SheetTitle className="flex items-center gap-3"><AppIcon appKey={managedGroup.appKey} className="size-11" />{name(managedGroup)}</SheetTitle><SheetDescription>{copy(language, `已安装到 ${managedGroup.instances.length} 台主机，选择安装以配置、更新或卸载。`, `Installed on ${managedGroup.instances.length} hosts. Select an installation to configure, update, or uninstall.`)}</SheetDescription></SheetHeader><div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4">
        <div className="divide-y rounded-xl border">{managedGroup.instances.map((instance) => <div className="flex flex-wrap items-center gap-3 p-4" key={instance.application.id}><div className="min-w-0 flex-1"><p className="break-words text-sm font-medium">{instance.agent?.name ?? instance.application.nodeId}</p><p className="mt-1 text-xs text-muted-foreground">v{instance.application.installedVersion} · {instance.siteName}</p></div><ApplicationStatus instance={instance} language={language} onUpgrade={(application) => { setManagedGroupKey(null); onUpgrade(application); }} /><Button onClick={() => { setManagedGroupKey(null); onManage(instance.application); }} variant="outline" size="sm">{copy(language, "管理", "Manage")}</Button></div>)}</div>
        {managedApp ? <div className="flex flex-col gap-2"><Button disabled={!canInstallMore} onClick={() => { setManagedGroupKey(null); onInstall(managedApp); }} variant="outline">{copy(language, "安装到其他主机", "Install on another host")}</Button>{!canInstallMore ? <p className="text-xs text-muted-foreground">{catalogInstallBlocked(managedApp) ? copy(language, "请先在控制面板刷新应用目录。", "Refresh the app catalog in Control Panel first.") : installBlocker(data, managedApp.key, language)}</p> : null}</div> : null}
      </div></> : null}
    </SheetContent></Sheet>
  </div>;
}
