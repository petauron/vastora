import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  ArrowUpRightIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  DownloadIcon,
  Grid2X2Icon,
  PackagePlusIcon,
  RefreshCwIcon,
  SearchIcon,
  Settings2Icon,
} from "lucide-react";
import type { AppData, Application, AppView, Deployment } from "@/types";
import type { Language } from "@/translations";
import { AppIcon } from "@/components/desktop/AppIcon";
import { Button } from "@/components/ui/button";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { AppStore, AppStoreTile } from "./AppStore";
import { eligibleAppNodes, localized } from "./appAccess";
import { daylightAppKey, isDaylightCatalogApp } from "./daylight";
import type { InstalledAppGroup, InstalledAppInstance } from "./installed-apps-model";
import { catalogInstallBlocked, copy } from "./shared";
import { StoreInstalledHome } from "./apps/StoreInstalledHome";
import { SuiteDetailView } from "./apps/SuiteDetailView";
import {
  buildStoreSuites,
  buildStoreUpdateEntries,
  isGlivelaCatalogApp,
  type StoreSuite,
  type SuiteStatusFilter,
} from "./apps/store-suites-model";

type Section = "installed" | "all";

type StoreRoute =
  | { kind: "section"; section: Section }
  | { kind: "detail"; suiteId: string; origin: Section; initialStatus?: SuiteStatusFilter; initialQuery?: string; detailKey: number };

export type StoreNavigationTarget = {
  suiteId: string;
  statusFilter?: SuiteStatusFilter;
  query?: string;
  nonce: number;
};

export function ApplicationStore({
  data,
  groups,
  language,
  onInstall,
  onOpen,
  onManage,
  onUpgrade,
  onBatchUpgrade,
  onRecoverTask,
  onRefresh,
  refreshing = false,
  submittingUpgradeID,
  activeTasksControl,
  navigationTarget,
  onSettings,
}: {
  data: AppData;
  groups: InstalledAppGroup[];
  language: Language;
  onInstall: (app: AppView) => void;
  onOpen: (key: string) => void;
  onManage: (application: Application) => void;
  onUpgrade: (application: Application) => void;
  onBatchUpgrade?: (title: string, instances: InstalledAppInstance[], initialSelectedIds?: string[]) => void;
  onRecoverTask?: (deployment: Deployment) => void;
  onRefresh?: () => void;
  refreshing?: boolean;
  submittingUpgradeID?: string | null;
  activeTasksControl?: ReactNode;
  navigationTarget?: StoreNavigationTarget | null;
  onSettings: () => void;
}) {
  const [navState, setNavState] = useState<{ entries: StoreRoute[]; index: number; nonce: number }>(() => ({
    entries: [{ kind: "section", section: groups.length > 0 ? "installed" : "all" }],
    index: 0,
    nonce: 1,
  }));
  const [query, setQuery] = useState("");

  const currentRoute = navState.entries[navState.index] ?? navState.entries[0];
  const activeSection: Section = currentRoute.kind === "section" ? currentRoute.section : currentRoute.origin;
  const search = query.trim().toLocaleLowerCase();

  const allSuites = useMemo(() => buildStoreSuites(data, groups, language), [data, groups, language]);
  const suiteByID = useMemo(() => new Map(allSuites.map((suite) => [suite.id, suite])), [allSuites]);
  const installedSuites = useMemo(
    () => allSuites.filter((suite) => suite.instances.length > 0 || suite.legacyInstances.length > 0),
    [allSuites],
  );
  const updateEntries = useMemo(
    () => buildStoreUpdateEntries(installedSuites, data, language),
    [installedSuites, data, language],
  );
  const updatableSuiteCount = useMemo(
    () => new Set(updateEntries.map((entry) => entry.suiteId)).size,
    [updateEntries],
  );

  const filteredInstalledSuites = useMemo(
    () => (search ? installedSuites.filter((suite) => suite.searchText.includes(search)) : installedSuites),
    [installedSuites, search],
  );
  const filteredUpdateEntries = useMemo(
    () => (search ? updateEntries.filter((entry) => entry.suite.searchText.includes(search) || entry.title.toLocaleLowerCase().includes(search)) : updateEntries),
    [updateEntries, search],
  );

  const glivelaCatalog = data.apps.filter(isGlivelaCatalogApp);
  const daylightCatalog = data.apps.filter(isDaylightCatalogApp);
  const otherCatalog = data.apps.filter((app) => !isGlivelaCatalogApp(app) && !isDaylightCatalogApp(app));
  const totalCatalogCount = otherCatalog.length + Number(glivelaCatalog.length > 0) + Number(daylightCatalog.length > 0);
  const matchingOtherCatalog = otherCatalog.filter((app) =>
    !search || [localized(app, language, "name"), localized(app, language, "description"), app.sourceId, app.key]
      .some((value) => value.toLocaleLowerCase().includes(search)),
  );
  const glivelaSuite = suiteByID.get("vastora-official/meridian");
  const daylightSuite = suiteByID.get(daylightAppKey);
  const showGlivelaInCatalog = glivelaCatalog.length > 0 && Boolean(glivelaSuite) && (!search || glivelaSuite!.searchText.includes(search));
  const showDaylightInCatalog = daylightCatalog.length > 0 && Boolean(daylightSuite) && (!search || daylightSuite!.searchText.includes(search));
  const catalogShownCount = matchingOtherCatalog.length + Number(showGlivelaInCatalog) + Number(showDaylightInCatalog);

  const pushRoute = (next: StoreRoute) => {
    setNavState((current) => {
      const head = current.entries[current.index];
      if (
        head
        && head.kind === next.kind
        && (head.kind === "section"
          ? next.kind === "section" && head.section === next.section
          : next.kind === "detail"
            && head.suiteId === next.suiteId
            && head.initialStatus === next.initialStatus
            && head.initialQuery === next.initialQuery)
      ) {
        return current;
      }
      const sliced = current.entries.slice(0, current.index + 1);
      const nextEntries = [...sliced, next];
      return {
        entries: nextEntries,
        index: nextEntries.length - 1,
        nonce: current.nonce + 1,
      };
    });
  };

  const navigateSection = (section: Section) => {
    pushRoute({ kind: "section", section });
  };

  const openDetail = (suiteId: string, statusFilter?: SuiteStatusFilter, initialQuery?: string, originOverride?: Section) => {
    setNavState((current) => {
      const nextNonce = current.nonce + 1;
      const head = current.entries[current.index];
      const fallbackOrigin: Section = head ? (head.kind === "detail" ? head.origin : head.section) : "installed";
      const nextRoute: StoreRoute = {
        kind: "detail",
        suiteId,
        origin: originOverride ?? fallbackOrigin,
        initialStatus: statusFilter,
        initialQuery,
        detailKey: nextNonce,
      };
      const sliced = current.entries.slice(0, current.index + 1);
      const nextEntries = [...sliced, nextRoute];
      return {
        entries: nextEntries,
        index: nextEntries.length - 1,
        nonce: nextNonce,
      };
    });
  };

  useEffect(() => {
    if (!navigationTarget) return;
    setNavState((current) => {
      const nextNonce = current.nonce + 1;
      const sliced = current.entries.slice(0, current.index + 1);
      const nextEntries: StoreRoute[] = [
        ...sliced,
        {
          kind: "detail",
          suiteId: navigationTarget.suiteId,
          origin: "installed",
          initialStatus: navigationTarget.statusFilter,
          initialQuery: navigationTarget.query,
          detailKey: nextNonce,
        },
      ];
      return {
        entries: nextEntries,
        index: nextEntries.length - 1,
        nonce: nextNonce,
      };
    });
  }, [navigationTarget]);

  const canGoBack = navState.index > 0;
  const canGoForward = navState.index < navState.entries.length - 1;
  const activeDetailSuite = currentRoute.kind === "detail" ? suiteByID.get(currentRoute.suiteId) : undefined;

  const catalogSuiteStatusText = (suite: StoreSuite) => {
    if (suite.counts.attention > 0) {
      return <span className="text-destructive">{copy(language, `${suite.counts.attention} 台需处理`, `${suite.counts.attention} need attention`)}</span>;
    }
    if (suite.counts.progress > 0) {
      return <span>{copy(language, `${suite.counts.progress} 台处理中`, `${suite.counts.progress} in progress`)}</span>;
    }
    if (suite.counts.updatableTotal > 0) {
      return <span className="text-success">{copy(language, `${suite.counts.updatableTotal} 台可更新`, `${suite.counts.updatableTotal} updatable`)}</span>;
    }
    if (suite.counts.hosts > 0) {
      return <span>{copy(language, `运行中 · ${suite.counts.hosts} 台主机`, `Running · ${suite.counts.hosts} hosts`)}</span>;
    }
    return <span>{copy(language, "尚未安装", "Not installed")}</span>;
  };

  const catalogSuiteActions = (suite: StoreSuite, compact = false) => {
    const actionSize = compact ? "sm" : "default";
    const hasOpen = !suite.background && Boolean(suite.launchUrl || suite.canOpenWorkspace);
    const manageButton = (
      <Button
        aria-label={copy(language, `管理 ${suite.name}`, `Manage ${suite.name}`)}
        onClick={() => openDetail(suite.id, undefined, undefined, "all")}
        size={actionSize}
        variant={hasOpen ? "outline" : "default"}
      >
        {copy(language, "管理", "Manage")}
      </Button>
    );
    const openButton = hasOpen
      ? suite.launchUrl
        ? (
          <Button
            aria-label={copy(language, `在新标签页打开 ${suite.name}`, `Open ${suite.name} in a new tab`)}
            nativeButton={false}
            render={<a href={suite.launchUrl} rel="noreferrer" target="_blank" />}
            size={actionSize}
            variant="default"
          >
            {copy(language, "打开", "Open")}
            <ArrowUpRightIcon aria-hidden="true" data-icon="inline-end" />
          </Button>
        )
        : suite.openTargetKey
          ? <Button onClick={() => onOpen(suite.openTargetKey!)} size={actionSize} variant="default">{copy(language, "打开", "Open")}</Button>
          : null
      : null;

    return <div className="flex flex-wrap gap-2">{manageButton}{openButton}</div>;
  };

  const glivelaCatalogTile = showGlivelaInCatalog && glivelaSuite ? (
    <AppStoreTile
      action={
        <div className="flex flex-wrap gap-2">
          <Button
            aria-label={copy(language, "管理 Glivela", "Manage Glivela")}
            onClick={() => openDetail(glivelaSuite.id, undefined, undefined, "all")}
            size="sm"
            variant={glivelaSuite.canOpenWorkspace ? "outline" : "default"}
          >
            {copy(language, "管理", "Manage")}
          </Button>
          {glivelaSuite.canOpenWorkspace && glivelaSuite.openTargetKey ? (
            <Button
              aria-label={copy(language, "打开 Glivela", "Open Glivela")}
              onClick={() => onOpen(glivelaSuite.openTargetKey!)}
              size="sm"
            >
              {copy(language, "打开", "Open")}
            </Button>
          ) : null}
        </div>
      }
      appKey={glivelaSuite.appKey}
      language={language}
      meta={
        glivelaSuite.instances.length > 0
          ? catalogSuiteStatusText(glivelaSuite)
          : copy(language, "控制端与节点端 · 尚未安装", "Controller & nodes · Not installed")
      }
      name="Glivela"
      onDetails={() => openDetail(glivelaSuite.id, undefined, undefined, "all")}
    />
  ) : null;

  const daylightCatalogTile = showDaylightInCatalog && daylightSuite ? (() => {
    const officialDaylight = daylightSuite.catalogApp;
    const canInstall = Boolean(officialDaylight && !catalogInstallBlocked(officialDaylight) && eligibleAppNodes(data, officialDaylight.key).length > 0);
    return (
      <AppStoreTile
        action={
          daylightSuite.instances.length > 0 ? catalogSuiteActions(daylightSuite, true) : (
            <div className="flex flex-wrap gap-2">
              {officialDaylight ? (
                <Button
                  aria-label={copy(language, "安装 Daylight", "Install Daylight")}
                  disabled={!canInstall}
                  onClick={() => onInstall(officialDaylight)}
                  size="sm"
                  variant="secondary"
                >
                  <PackagePlusIcon aria-hidden="true" data-icon="inline-start" />
                  {copy(language, "安装", "Install")}
                </Button>
              ) : null}
              <Button
                aria-label={copy(language, "管理 Daylight", "Manage Daylight")}
                onClick={() => openDetail(daylightSuite.id, undefined, undefined, "all")}
                size="sm"
                variant="outline"
              >
                {copy(language, "管理", "Manage")}
              </Button>
            </div>
          )
        }
        appKey={daylightSuite.appKey}
        language={language}
        meta={daylightSuite.instances.length > 0 ? catalogSuiteStatusText(daylightSuite) : daylightSuite.description}
        name="Daylight"
        onDetails={() => openDetail(daylightSuite.id, undefined, undefined, "all")}
      />
    );
  })() : null;

  return (
    <div className="store-shell">
      {/* Synology-style Unified Top Command Bar */}
      <div className="store-topbar">
        <div className="store-topbar-nav">
          <Button
            aria-label={copy(language, "返回上一页", "Go back")}
            disabled={!canGoBack}
            onClick={() => setNavState((current) => ({ ...current, index: Math.max(0, current.index - 1) }))}
            size="icon-sm"
            variant="ghost"
          >
            <ChevronLeftIcon aria-hidden="true" />
          </Button>
          <Button
            aria-label={copy(language, "前进", "Go forward")}
            disabled={!canGoForward}
            onClick={() => setNavState((current) => ({ ...current, index: Math.min(current.entries.length - 1, current.index + 1) }))}
            size="icon-sm"
            variant="ghost"
          >
            <ChevronRightIcon aria-hidden="true" />
          </Button>
          {onRefresh ? (
            <Button
              aria-label={copy(language, "刷新应用商店", "Refresh App Store")}
              disabled={refreshing}
              onClick={onRefresh}
              size="icon-sm"
              variant="ghost"
            >
              <RefreshCwIcon aria-hidden="true" className={refreshing ? "animate-spin motion-reduce:animate-none" : undefined} />
            </Button>
          ) : null}
        </div>

        <InputGroup className="store-topbar-search">
          <InputGroupAddon><SearchIcon aria-hidden="true" /></InputGroupAddon>
          <InputGroupInput
            aria-label={copy(language, "搜索应用商店", "Search App Store")}
            onChange={(event) => {
              const nextValue = event.target.value;
              setQuery(nextValue);
              if (nextValue.trim() && currentRoute.kind === "detail") {
                navigateSection(activeSection);
              }
            }}
            placeholder={copy(language, "搜索套件、节点或站点…", "Search apps, hosts, or sites…")}
            type="search"
            value={query}
          />
        </InputGroup>

        <div className="store-topbar-actions">
          {activeTasksControl}
          <Button
            aria-label={copy(language, "目录设置", "Catalog settings")}
            className="store-catalog-settings shrink-0"
            onClick={onSettings}
            size="sm"
            variant="outline"
          >
            <Settings2Icon aria-hidden="true" data-icon="inline-start" />
            <span>{copy(language, "目录设置", "Catalog settings")}</span>
          </Button>
        </div>
      </div>

      {/* Main Split: 2-Item Sidebar + Content / Workbench */}
      <div className="store-layout">
        <nav aria-label={copy(language, "应用商店分类", "App Store categories")} className="store-navigation">
          <div className="flex gap-1 md:flex-col">
            <Button
              aria-current={activeSection === "installed" ? "page" : undefined}
              className="store-navigation-item"
              onClick={() => navigateSection("installed")}
              variant="ghost"
            >
              <DownloadIcon aria-hidden="true" />
              <span>{copy(language, "已安装", "Installed")}</span>
              {updatableSuiteCount > 0 ? (
                <span
                  aria-label={copy(language, `${updatableSuiteCount} 个套件有可用更新`, `${updatableSuiteCount} apps have updates`)}
                  className="store-nav-badge-update ml-auto tabular-nums"
                >
                  {updatableSuiteCount}
                </span>
              ) : (
                <span className="ml-auto text-xs tabular-nums opacity-60">{installedSuites.length}</span>
              )}
            </Button>

            <Button
              aria-current={activeSection === "all" ? "page" : undefined}
              className="store-navigation-item"
              onClick={() => navigateSection("all")}
              variant="ghost"
            >
              <Grid2X2Icon aria-hidden="true" />
              <span>{copy(language, "所有应用", "All apps")}</span>
              <span className="ml-auto text-xs tabular-nums opacity-60">{totalCatalogCount}</span>
            </Button>
          </div>
        </nav>

        <div className="store-content">
          {currentRoute.kind === "detail" ? (
            activeDetailSuite ? (
              <SuiteDetailView
                data={data}
                initialQuery={currentRoute.initialQuery}
                initialStatus={currentRoute.initialStatus}
                key={`${activeDetailSuite.id}:${currentRoute.detailKey}`}
                language={language}
                onBatchUpgrade={onBatchUpgrade}
                onInstall={onInstall}
                onManage={onManage}
                onOpen={onOpen}
                onRecoverTask={onRecoverTask}
                onUpgrade={onUpgrade}
                submittingUpgradeID={submittingUpgradeID}
                suite={activeDetailSuite}
              />
            ) : (
              <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed px-5 py-14 text-center">
                <AppIcon appKey="apps" className="size-12" />
                <p className="text-sm text-muted-foreground">{copy(language, "该套件已移除或暂不可用", "This app is no longer installed or available")}</p>
                <Button onClick={() => navigateSection("installed")} variant="outline">
                  {copy(language, "返回已安装", "Back to Installed")}
                </Button>
              </div>
            )
          ) : currentRoute.section === "installed" ? (
            <StoreInstalledHome
              data={data}
              installedSuites={filteredInstalledSuites}
              language={language}
              onBatchUpgrade={onBatchUpgrade}
              onBrowseAll={() => navigateSection("all")}
              onClearSearch={() => setQuery("")}
              onManageInstance={onManage}
              onOpenApp={onOpen}
              onOpenDetail={(suiteId, statusFilter) => openDetail(suiteId, statusFilter, undefined, "installed")}
              onRecoverTask={onRecoverTask}
              onUpgrade={onUpgrade}
              search={search}
              submittingUpgradeID={submittingUpgradeID}
              updateEntries={filteredUpdateEntries}
            />
          ) : (
            <div className="flex flex-col gap-5">
              <header className="flex flex-wrap items-baseline justify-between gap-2">
                <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
                  <h1 className="text-base font-semibold tracking-tight">{copy(language, "所有应用", "All apps")}</h1>
                  <p className="text-xs text-muted-foreground tabular-nums" role="status">
                    {copy(language, `${catalogShownCount} 个应用`, `${catalogShownCount} applications`)}
                  </p>
                </div>
              </header>

              <AppStore
                data={data}
                extraCard={<>{glivelaCatalogTile}{daylightCatalogTile}</>}
                hideApp={(app) => isGlivelaCatalogApp(app) || isDaylightCatalogApp(app)}
                language={language}
                onInstall={onInstall}
                query={search}
                renderInstalledActions={(key) => {
                  const suite = suiteByID.get(key);
                  return suite ? catalogSuiteActions(suite, true) : null;
                }}
                renderInstalledDetailActions={(key) => {
                  const suite = suiteByID.get(key);
                  return suite ? catalogSuiteActions(suite, false) : null;
                }}
                renderInstalledStatus={(key) => {
                  const suite = suiteByID.get(key);
                  return suite ? catalogSuiteStatusText(suite) : null;
                }}
                renderInstalledSummary={(key) => {
                  const suite = suiteByID.get(key);
                  if (!suite) return null;
                  const versionText = suite.installedVersions.length === 1
                    ? suite.installedVersions[0]
                    : suite.installedVersions.length > 1
                      ? copy(language, `${suite.installedVersions.length} 种版本`, `${suite.installedVersions.length} versions`)
                      : "";
                  return (
                    <p className="text-xs text-muted-foreground tabular-nums">
                      {copy(language, `已安装到 ${suite.counts.hosts} 台主机`, `Installed on ${suite.counts.hosts} host(s)`)}
                      {versionText ? ` · ${versionText}` : ""}
                    </p>
                  );
                }}
              />

              {!catalogShownCount && Boolean(search) ? (
                <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed px-5 py-14 text-center">
                  <AppIcon appKey="apps" className="size-12" />
                  <p className="text-sm text-muted-foreground">{copy(language, "没有匹配的应用", "No matching apps")}</p>
                  <Button onClick={() => setQuery("")} variant="outline">{copy(language, "清除搜索", "Clear search")}</Button>
                </div>
              ) : null}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
