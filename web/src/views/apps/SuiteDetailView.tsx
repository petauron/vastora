import { useMemo, useState } from "react";
import {
  ArrowUpRightIcon,
  CircleArrowUpIcon,
  PackagePlusIcon,
  SearchIcon,
  ShieldAlertIcon,
} from "lucide-react";
import type { AppData, Application, AppView, Deployment } from "@/types";
import type { Language } from "@/translations";
import { siteLabel, siteRegionCode } from "@/lib/regions";
import { AppIcon } from "@/components/desktop/AppIcon";
import { SelectControl } from "@/components/SelectControl";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { AppHostAccessNote, AppIdentityBadge, isOfficialProduct } from "../AppIdentity";
import { RegionFlag } from "../RegionFlag";
import { eligibleAppNodes, installBlocker } from "../appAccess";
import { isBackgroundApplication } from "../applicationLaunch";
import type { InstalledAppInstance } from "../installed-apps-model";
import { catalogInstallBlocked, copy } from "../shared";
import { planBatchUpgrade } from "./BatchUpgradeSheet";
import { AccessStatus, ApplicationPrimaryStatus, ApplicationUpdate } from "./InstalledApplicationPrimitives";
import { isSameVersionRevisionUpgrade } from "./versionDisplay";
import {
  glivelaControllerKey,
  glivelaNodeCapabilities,
  glivelaRoleLabel,
  instanceHasIssue,
  instanceIsInProgress,
  instanceIsOffline,
  instanceIsRunningHealthy,
  instanceIsUpdatable,
  instanceMatchesStatus,
  isGlivelaCatalogApp,
  sortSuiteInstances,
  type StoreSuite,
  type SuiteSortKey,
  type SuiteStatusFilter,
} from "./store-suites-model";

export function SuiteDetailView({
  suite,
  data,
  language,
  initialStatus = "all",
  initialQuery = "",
  onInstall,
  onOpen,
  onManage,
  onUpgrade,
  onBatchUpgrade,
  onRecoverTask,
  submittingUpgradeID,
}: {
  suite: StoreSuite;
  data: AppData;
  language: Language;
  initialStatus?: SuiteStatusFilter;
  initialQuery?: string;
  onInstall: (app: AppView) => void;
  onOpen: (key: string) => void;
  onManage: (application: Application) => void;
  onUpgrade: (application: Application) => void;
  onBatchUpgrade?: (title: string, instances: InstalledAppInstance[], initialSelectedIds?: string[]) => void;
  onRecoverTask?: (deployment: Deployment) => void;
  submittingUpgradeID?: string | null;
}) {
  const [statusFilter, setStatusFilter] = useState<SuiteStatusFilter>(initialStatus);
  const [siteFilter, setSiteFilter] = useState("all");
  const [query, setQuery] = useState(initialQuery);
  const [sort, setSort] = useState<SuiteSortKey>("priority");
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [nodePackageKey, setNodePackageKey] = useState<string>(glivelaNodeCapabilities[0].key);

  const siteByID = useMemo(() => new Map(data.sites.map((site) => [site.id, site])), [data.sites]);
  const primaryApp = suite.catalogApp;
  const officialCatalog = Boolean(primaryApp && primaryApp.sourceId === "vastora-official" && primaryApp.key === `vastora-official/${primaryApp.app.id}`);

  const tableInstances = suite.kind === "glivela" ? suite.nodeInstances : suite.instances;
  const tableBatchTitle = suite.kind === "glivela"
    ? `Glivela ${copy(language, "节点端", "Node")}`
    : suite.name;
  const tableBatchPlan = useMemo(
    () => planBatchUpgrade(tableBatchTitle, tableInstances, data, language),
    [tableBatchTitle, tableInstances, data, language],
  );
  const eligibleIdSet = useMemo(
    () => new Set(tableBatchPlan.eligible.map((item) => item.instance.application.id)),
    [tableBatchPlan.eligible],
  );
  const skippedReasonByID = useMemo(
    () => new Map(tableBatchPlan.skipped.map((item) => [item.instance.application.id, item.reason])),
    [tableBatchPlan.skipped],
  );

  // Standard / Daylight install availability.
  const canInstallStandard = Boolean(
    suite.kind !== "glivela"
    && primaryApp
    && !catalogInstallBlocked(primaryApp)
    && eligibleAppNodes(data, primaryApp.key).length > 0,
  );
  const standardBlocker = suite.kind !== "glivela" && primaryApp && !canInstallStandard
    ? catalogInstallBlocked(primaryApp)
      ? primaryApp.installBlockedReason || copy(language, "请先在控制面板刷新应用目录。", "Refresh the app catalog in Control Panel first.")
      : installBlocker(data, primaryApp.key, language)
    : "";

  // Glivela controller / node install availability.
  const glivelaControllerApp = suite.kind === "glivela"
    ? data.apps.find((app) => app.key === glivelaControllerKey && isGlivelaCatalogApp(app))
    : undefined;
  const canInstallController = Boolean(
    glivelaControllerApp
    && !catalogInstallBlocked(glivelaControllerApp)
    && eligibleAppNodes(data, glivelaControllerApp.key).length > 0,
  );
  const controllerBlocker = glivelaControllerApp && !canInstallController
    ? catalogInstallBlocked(glivelaControllerApp)
      ? glivelaControllerApp.installBlockedReason || copy(language, "请先在控制面板刷新应用目录。", "Refresh the app catalog in Control Panel first.")
      : installBlocker(data, glivelaControllerApp.key, language)
    : "";

  const glivelaNodeApp = suite.kind === "glivela"
    ? data.apps.find((app) => app.key === nodePackageKey && isGlivelaCatalogApp(app))
    : undefined;
  const canInstallNode = Boolean(
    glivelaNodeApp
    && !catalogInstallBlocked(glivelaNodeApp)
    && eligibleAppNodes(data, glivelaNodeApp.key).length > 0,
  );
  const nodeBlocker = glivelaNodeApp && !canInstallNode
    ? catalogInstallBlocked(glivelaNodeApp)
      ? glivelaNodeApp.installBlockedReason || copy(language, "请先在控制面板刷新应用目录。", "Refresh the app catalog in Control Panel first.")
      : installBlocker(data, glivelaNodeApp.key, language)
    : "";

  const statusCounts = useMemo(() => ({
    all: tableInstances.length,
    attention: tableInstances.filter(instanceHasIssue).length,
    progress: tableInstances.filter(instanceIsInProgress).length,
    updatable: tableInstances.filter(instanceIsUpdatable).length,
    running: tableInstances.filter(instanceIsRunningHealthy).length,
    offline: tableInstances.filter(instanceIsOffline).length,
  }), [tableInstances]);

  const availableSites = useMemo(() => {
    const seen = new Map<string, { id: string; name: string; count: number }>();
    for (const instance of tableInstances) {
      const siteId = instance.application.siteId;
      const siteName = siteByID.get(siteId)?.name ?? instance.siteName;
      const existing = seen.get(siteId);
      if (existing) existing.count += 1;
      else seen.set(siteId, { id: siteId, name: siteName, count: 1 });
    }
    return [...seen.values()].sort((left, right) => left.name.localeCompare(right.name, language));
  }, [tableInstances, siteByID, language]);

  const effectiveSiteFilter = siteFilter !== "all" && availableSites.some((site) => site.id === siteFilter)
    ? siteFilter
    : "all";

  const filteredInstances = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase();
    const matched = tableInstances.filter((instance) => {
      if (!instanceMatchesStatus(instance, statusFilter)) return false;
      if (effectiveSiteFilter !== "all" && instance.application.siteId !== effectiveSiteFilter) return false;
      if (!normalizedQuery) return true;
      const hostName = instance.agent?.name ?? instance.application.nodeId;
      const role = glivelaRoleLabel(instance.application.appKey, language);
      const daylightMode = instance.application.appKey === "vastora-official/daylight-https" ? "https"
        : instance.application.appKey === "vastora-official/daylight" ? "http" : "";
      return [
        hostName,
        instance.siteName,
        instance.application.installedVersion,
        instance.application.availableVersion,
        role,
        daylightMode,
      ].some((value) => value?.toLocaleLowerCase().includes(normalizedQuery));
    });
    return sortSuiteInstances(matched, sort);
  }, [tableInstances, statusFilter, effectiveSiteFilter, query, sort, language]);

  const effectiveSelectedIds = selectedIds.filter((id) => eligibleIdSet.has(id));
  const visibleEligibleIds = filteredInstances
    .map((instance) => instance.application.id)
    .filter((id) => eligibleIdSet.has(id));
  const allVisibleSelected = visibleEligibleIds.length > 0
    && visibleEligibleIds.every((id) => effectiveSelectedIds.includes(id));

  const toggleAllVisible = () => {
    if (allVisibleSelected) {
      const visibleSet = new Set(visibleEligibleIds);
      setSelectedIds(effectiveSelectedIds.filter((id) => !visibleSet.has(id)));
    } else {
      setSelectedIds([...new Set([...effectiveSelectedIds, ...visibleEligibleIds])]);
    }
  };

  const toggleOne = (id: string, checked: boolean) => {
    setSelectedIds(checked
      ? [...effectiveSelectedIds.filter((item) => item !== id), id]
      : effectiveSelectedIds.filter((item) => item !== id));
  };

  const filterChips: Array<{ id: SuiteStatusFilter; label: string; count: number; tone?: string }> = [
    { id: "all", label: copy(language, "全部", "All"), count: statusCounts.all },
    { id: "attention", label: copy(language, "需处理", "Attention"), count: statusCounts.attention, tone: "text-destructive" },
    { id: "progress", label: copy(language, "处理中", "Active"), count: statusCounts.progress },
    { id: "updatable", label: copy(language, "可更新", "Updatable"), count: statusCounts.updatable, tone: "text-success" },
    { id: "running", label: copy(language, "运行中", "Running"), count: statusCounts.running },
    { id: "offline", label: copy(language, "离线", "Offline"), count: statusCounts.offline },
  ];

  const showCheckboxes = tableBatchPlan.eligible.length > 0;
  const showRoleCol = suite.kind === "glivela";
  const showSiteTlsCol = suite.kind === "daylight";
  const colSpan = 4 + Number(showCheckboxes) + Number(showRoleCol) + Number(showSiteTlsCol);
  const hasOpen = !suite.background && Boolean(suite.launchUrl || suite.canOpenWorkspace);

  return (
    <div className="store-detail-workbench flex flex-col gap-5">
      {/* Hero Header */}
      <section className="store-detail-hero">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex min-w-0 flex-1 items-start gap-3.5">
            <AppIcon appKey={suite.appKey} className="size-12 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <h1 className="text-lg font-semibold tracking-tight">{suite.name}</h1>
                {primaryApp ? <AppIdentityBadge app={primaryApp} language={language} /> : null}
                {primaryApp ? (
                  <Badge className="tabular-nums" variant="secondary">v{primaryApp.app.version}</Badge>
                ) : null}
              </div>
              {suite.description ? (
                <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{suite.description}</p>
              ) : null}
              <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground tabular-nums">
                <span>
                  {copy(
                    language,
                    `已安装 ${suite.counts.hosts}/${data.agents.length} 台主机`,
                    `Installed on ${suite.counts.hosts}/${data.agents.length} hosts`,
                  )}
                </span>
                {suite.siteNames.length > 0 ? (
                  <span>
                    · {copy(language, `覆盖 ${suite.siteNames.length} 个站点`, `${suite.siteNames.length} site(s)`)}
                  </span>
                ) : null}
                {suite.installedVersions.length > 0 ? (
                  <span>
                    · {suite.installedVersions.length === 1
                      ? copy(language, `版本 ${suite.installedVersions[0]}`, `Version ${suite.installedVersions[0]}`)
                      : copy(language, `${suite.installedVersions.length} 个版本并存`, `${suite.installedVersions.length} versions`)}
                  </span>
                ) : null}
              </div>
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            {suite.kind !== "glivela" && primaryApp ? (
              <Button
                aria-describedby={standardBlocker ? "suite-install-blocker" : undefined}
                aria-label={copy(language, `安装 ${suite.name}`, `Install ${suite.name}`)}
                disabled={!canInstallStandard}
                onClick={() => onInstall(primaryApp)}
                size="sm"
                variant="outline"
              >
                <PackagePlusIcon aria-hidden="true" data-icon="inline-start" />
                {suite.instances.length > 0
                  ? copy(language, "安装到其他主机", "Install on another host")
                  : copy(language, "安装到主机", "Install on host")}
              </Button>
            ) : null}
            {suite.kind !== "glivela" && tableBatchPlan.eligible.length > 0 && onBatchUpgrade ? (
              <Button
                className="store-btn-update"
                onClick={() => onBatchUpgrade(tableBatchTitle, tableInstances)}
                size="sm"
              >
                <CircleArrowUpIcon aria-hidden="true" data-icon="inline-start" />
                {copy(language, `全部更新 (${tableBatchPlan.eligible.length})`, `Update all (${tableBatchPlan.eligible.length})`)}
              </Button>
            ) : null}
            {hasOpen ? (
              suite.launchUrl ? (
                <Button
                  aria-label={copy(language, `在新标签页打开 ${suite.name}`, `Open ${suite.name} in a new tab`)}
                  nativeButton={false}
                  render={<a href={suite.launchUrl} rel="noreferrer" target="_blank" />}
                  size="sm"
                  variant="default"
                >
                  {copy(language, "打开", "Open")}
                  <ArrowUpRightIcon aria-hidden="true" data-icon="inline-end" />
                </Button>
              ) : suite.openTargetKey ? (
                <Button
                  aria-label={copy(language, `打开 ${suite.name}`, `Open ${suite.name}`)}
                  onClick={() => onOpen(suite.openTargetKey!)}
                  size="sm"
                  variant="default"
                >
                  {copy(language, "打开", "Open")}
                </Button>
              ) : null
            ) : null}
          </div>
        </div>

        {standardBlocker ? (
          <p className="mt-3 text-xs text-muted-foreground" id="suite-install-blocker">{standardBlocker}</p>
        ) : null}

        {primaryApp ? (
          <details className="mt-3 border-t border-border/60 pt-2.5">
            <summary className="cursor-pointer text-xs font-medium text-muted-foreground hover:text-foreground">
              {copy(language, "应用详情与权限说明", "App details & permissions")}
            </summary>
            <div className="mt-3 flex flex-col gap-3">
              <dl className="mac-settings-group">
                <div className="mac-settings-row">
                  <dt>{copy(language, "目录版本", "Catalog version")}</dt>
                  <dd>v{primaryApp.app.version} · r{primaryApp.app.packageRevision ?? 0}</dd>
                </div>
                <div className="mac-settings-row">
                  <dt>{copy(language, "目录来源", "Catalog source")}</dt>
                  <dd>{officialCatalog ? copy(language, "官方目录", "Official catalog") : copy(language, `第三方目录 · ${primaryApp.sourceId}`, `Third-party catalog · ${primaryApp.sourceId}`)}</dd>
                </div>
                <div className="mac-settings-row">
                  <dt>{copy(language, "类型", "Type")}</dt>
                  <dd>{primaryApp.app.hostAccess ? copy(language, "主机应用", "Host app") : copy(language, "容器应用", "Container app")}</dd>
                </div>
              </dl>
              {isBackgroundApplication(primaryApp) ? (
                <p className="text-xs text-muted-foreground">{copy(language, "后台服务 · 在应用商店管理", "Background service · manage in App Store")}</p>
              ) : null}
              {primaryApp.app.hostAccess && !isOfficialProduct(primaryApp) ? (
                <AppHostAccessNote app={primaryApp} language={language} />
              ) : null}
            </div>
          </details>
        ) : null}
      </section>

      {/* Glivela Controller Band */}
      {suite.kind === "glivela" ? (
        <section aria-labelledby="glivela-part-controller" className="store-detail-section">
          <div className="store-detail-section-header">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-sm font-semibold" id="glivela-part-controller">{copy(language, "控制端", "Controller")}</h2>
              <Badge className="tabular-nums" variant="secondary">
                {suite.controllerInstances.length > 0
                  ? copy(language, `${suite.controllerInstances.length} 台`, `${suite.controllerInstances.length}`)
                  : copy(language, "尚未安装", "Not installed")}
              </Badge>
              {glivelaControllerApp ? (
                <span className="text-xs text-muted-foreground tabular-nums">
                  {copy(language, "目录版本", "Catalog")} v{glivelaControllerApp.app.version} · r{glivelaControllerApp.app.packageRevision ?? 0}
                </span>
              ) : null}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {glivelaControllerApp ? (
                <Button
                  aria-describedby={controllerBlocker ? "glivela-install-controller-blocker" : undefined}
                  aria-label={copy(language, "安装控制端", "Install Controller")}
                  disabled={!canInstallController}
                  onClick={() => onInstall(glivelaControllerApp)}
                  size="sm"
                  variant="outline"
                >
                  <PackagePlusIcon aria-hidden="true" data-icon="inline-start" />
                  {suite.controllerInstances.length > 0
                    ? copy(language, "安装到其他主机", "Install on another host")
                    : copy(language, "安装控制端", "Install controller")}
                </Button>
              ) : null}
            </div>
          </div>

          {controllerBlocker ? (
            <p className="border-b px-4 py-2 text-xs text-muted-foreground" id="glivela-install-controller-blocker">{controllerBlocker}</p>
          ) : null}

          {suite.controllerInstances.length > 0 ? (
            <div className="divide-y">
              {suite.controllerInstances.map((instance) => {
                const hostName = instance.agent?.name ?? instance.application.nodeId;
                const site = siteByID.get(instance.application.siteId);
                const regionCode = site ? siteRegionCode(site) : siteRegionCode({ name: instance.siteName });
                const targetVersion = instance.application.availableVersion || instance.app?.app.version;
                const targetRevision = instance.application.availableVersion
                  ? (instance.application.availablePackageRevision ?? (instance.app?.app.version === instance.application.availableVersion ? instance.app?.app.packageRevision : undefined))
                  : instance.app?.app.packageRevision;
                const sameVersionRevision = isSameVersionRevisionUpgrade({
                  fromVersion: instance.application.installedVersion,
                  toVersion: targetVersion,
                  fromRevision: instance.application.installedPackageRevision,
                  toRevision: targetRevision,
                });
                return (
                  <div className="flex flex-wrap items-center gap-3 px-4 py-3" key={instance.application.id}>
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm font-medium">{hostName}</p>
                      <p className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground tabular-nums">
                        {regionCode ? <RegionFlag code={regionCode} language={language} /> : null}
                        <span>{instance.siteName}</span>
                        <span aria-hidden="true">·</span>
                        <span>v{instance.application.installedVersion}</span>
                        {instance.application.updateAvailable && targetVersion ? (
                          sameVersionRevision ? (
                            <span className="font-medium text-success">(r{instance.application.installedPackageRevision} → r{targetRevision})</span>
                          ) : (
                            <span className="font-medium text-success">→ v{targetVersion}</span>
                          )
                        ) : null}
                      </p>
                    </div>
                    <div className="flex flex-wrap items-center gap-2">
                      <ApplicationPrimaryStatus instance={instance} language={language} />
                      {instance.agent && !instance.agent.connected ? (
                        <span className="text-xs text-destructive">{copy(language, "节点离线", "Node offline")}</span>
                      ) : null}
                      {instance.activeChange?.reconciliationRequired && onRecoverTask ? (
                        <Button onClick={() => onRecoverTask(instance.activeChange!)} size="sm" variant="outline">
                          {copy(language, "核对恢复", "Recover")}
                        </Button>
                      ) : null}
                      <ApplicationUpdate
                        instance={instance}
                        language={language}
                        onUpgrade={onUpgrade}
                        submitting={submittingUpgradeID === instance.application.id}
                      />
                      <Button
                        aria-label={copy(language, `管理控制端：${hostName}`, `Manage Controller: ${hostName}`)}
                        onClick={() => onManage(instance.application)}
                        size="sm"
                        variant="outline"
                      >
                        {copy(language, "管理", "Manage")}
                      </Button>
                    </div>
                  </div>
                );
              })}
            </div>
          ) : (
            <p className="px-4 py-4 text-xs text-muted-foreground">
              {copy(language, "尚未安装 Glivela 控制端。", "Glivela Controller is not installed yet.")}
            </p>
          )}
        </section>
      ) : null}

      {/* Fleet Table Section (Standard, Daylight, or Glivela Nodes) */}
      <section aria-labelledby="suite-fleet-heading" className="store-detail-section">
        <div className="store-detail-section-header">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-sm font-semibold" id="suite-fleet-heading">
              {suite.kind === "glivela"
                ? copy(language, "节点端主机", "Node hosts")
                : copy(language, "已安装主机", "Installed hosts")}
            </h2>
            <span className="text-xs text-muted-foreground tabular-nums">
              {copy(language, `${tableInstances.length} 个实例`, `${tableInstances.length} installation(s)`)}
            </span>
          </div>

          {suite.kind === "glivela" ? (
            <div className="flex flex-wrap items-center gap-2">
              <div className="w-52 max-w-full">
                <SelectControl
                  aria-label={copy(language, "新节点功能", "New node capabilities")}
                  id="glivela-node-capabilities"
                  onValueChange={setNodePackageKey}
                  options={glivelaNodeCapabilities.map((item) => ({ value: item.key, label: copy(language, item.zh, item.en) }))}
                  size="sm"
                  value={nodePackageKey}
                />
              </div>
              {glivelaNodeApp ? (
                <Button
                  aria-describedby={nodeBlocker ? "glivela-install-node-blocker" : undefined}
                  aria-label={copy(language, "安装节点端", "Install Node")}
                  disabled={!canInstallNode}
                  onClick={() => onInstall(glivelaNodeApp)}
                  size="sm"
                  variant="outline"
                >
                  <PackagePlusIcon aria-hidden="true" data-icon="inline-start" />
                  {copy(language, "安装节点端", "Install node")}
                </Button>
              ) : null}
              {tableBatchPlan.eligible.length > 0 && onBatchUpgrade ? (
                <Button
                  className="store-btn-update"
                  onClick={() => onBatchUpgrade(tableBatchTitle, tableInstances)}
                  size="sm"
                >
                  <CircleArrowUpIcon aria-hidden="true" data-icon="inline-start" />
                  {copy(language, `全部更新 (${tableBatchPlan.eligible.length})`, `Update all (${tableBatchPlan.eligible.length})`)}
                </Button>
              ) : null}
            </div>
          ) : null}
        </div>

        {suite.kind === "glivela" && (nodeBlocker || !glivelaNodeApp) ? (
          <div className="border-b px-4 py-2 text-xs text-muted-foreground">
            {glivelaNodeApp ? (
              <p id="glivela-install-node-blocker">
                {copy(language, `目录版本 v${glivelaNodeApp.app.version} · r${glivelaNodeApp.app.packageRevision ?? 0} · `, `Catalog v${glivelaNodeApp.app.version} · r${glivelaNodeApp.app.packageRevision ?? 0} · `)}
                {nodeBlocker}
              </p>
            ) : (
              <p>{copy(language, "目录暂未提供所选功能", "The selected capability is unavailable in the catalog")}</p>
            )}
          </div>
        ) : null}

        {tableInstances.length > 0 ? (
          <>
            {/* Filter & Search Toolbar */}
            <div className="store-workbench-toolbar">
              <div aria-label={copy(language, "按状态筛选", "Filter by status")} className="flex flex-wrap items-center gap-1.5" role="group">
                {filterChips.map((chip) => {
                  if (chip.id !== "all" && chip.count === 0 && statusFilter !== chip.id) return null;
                  const active = statusFilter === chip.id;
                  return (
                    <button
                      aria-pressed={active}
                      className={`store-filter-chip ${active ? "store-filter-chip-active" : ""}`}
                      key={chip.id}
                      onClick={() => setStatusFilter(chip.id)}
                      type="button"
                    >
                      <span className={!active ? chip.tone : undefined}>{chip.label}</span>
                      <span className="tabular-nums opacity-75">{chip.count}</span>
                    </button>
                  );
                })}
              </div>

              <div className="flex flex-wrap items-center gap-2">
                <InputGroup className="w-full sm:w-56">
                  <InputGroupAddon><SearchIcon aria-hidden="true" /></InputGroupAddon>
                  <InputGroupInput
                    aria-label={copy(language, "筛选主机", "Filter hosts")}
                    onChange={(event) => setQuery(event.target.value)}
                    placeholder={copy(language, "搜索节点、站点或版本…", "Filter hosts, sites, versions…")}
                    type="search"
                    value={query}
                  />
                </InputGroup>

                {availableSites.length > 1 ? (
                  <div className="w-40">
                    <SelectControl
                      aria-label={copy(language, "按站点筛选", "Filter by site")}
                      onValueChange={setSiteFilter}
                      options={[
                        { value: "all", label: copy(language, "全部站点", "All sites") },
                        ...availableSites.map((site) => ({
                          value: site.id,
                          label: `${siteLabel({ name: site.name })} (${site.count})`,
                        })),
                      ]}
                      size="sm"
                      value={effectiveSiteFilter}
                    />
                  </div>
                ) : null}

                {tableInstances.length > 1 ? (
                  <div className="w-40">
                    <SelectControl
                      aria-label={copy(language, "排序方式", "Sort order")}
                      onValueChange={(value) => setSort(value as SuiteSortKey)}
                      options={[
                        { value: "priority", label: copy(language, "异常与可更新优先", "Issues & updates first") },
                        { value: "site", label: copy(language, "按站点排序", "Sort by site") },
                        { value: "name", label: copy(language, "按节点名排序", "Sort by host") },
                        { value: "version", label: copy(language, "旧版本优先", "Oldest version first") },
                      ]}
                      size="sm"
                      value={sort}
                    />
                  </div>
                ) : null}
              </div>
            </div>

            {/* Inline Bulk Selection Bar */}
            {effectiveSelectedIds.length > 0 && onBatchUpgrade ? (
              <div className="store-bulk-bar" role="status">
                <span className="text-xs font-medium tabular-nums">
                  {copy(language, `已勾选 ${effectiveSelectedIds.length} 台可更新主机`, `${effectiveSelectedIds.length} updatable host(s) selected`)}
                </span>
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    className="store-btn-update"
                    onClick={() => onBatchUpgrade(tableBatchTitle, tableInstances, effectiveSelectedIds)}
                    size="xs"
                  >
                    <CircleArrowUpIcon aria-hidden="true" data-icon="inline-start" />
                    {copy(language, `更新所选 (${effectiveSelectedIds.length})`, `Update selected (${effectiveSelectedIds.length})`)}
                  </Button>
                  <Button onClick={() => setSelectedIds([])} size="xs" variant="ghost">
                    {copy(language, "清除选择", "Clear")}
                  </Button>
                </div>
              </div>
            ) : null}

            {/* Host Fleet Table */}
            <div className="overflow-x-auto">
              <Table className="store-fleet-table">
                <TableHeader>
                  <TableRow>
                    {showCheckboxes ? (
                      <TableHead className="w-10 pl-4">
                        <Checkbox
                          aria-label={copy(language, "全选当前筛选中的可更新主机", "Select all updatable hosts in view")}
                          checked={allVisibleSelected}
                          disabled={visibleEligibleIds.length === 0}
                          onCheckedChange={toggleAllVisible}
                        />
                      </TableHead>
                    ) : null}
                    <TableHead className={showCheckboxes ? "" : "pl-4"}>{copy(language, "主机 / 站点", "Host / Site")}</TableHead>
                    {showRoleCol ? <TableHead>{copy(language, "节点功能", "Capability")}</TableHead> : null}
                    {showSiteTlsCol ? <TableHead>{copy(language, "站点与证书", "Site & TLS")}</TableHead> : null}
                    <TableHead>{copy(language, "版本", "Version")}</TableHead>
                    <TableHead>{copy(language, "状态", "Status")}</TableHead>
                    <TableHead className="pr-4 text-right">{copy(language, "操作", "Actions")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {filteredInstances.length > 0 ? filteredInstances.map((instance) => {
                    const hostName = instance.agent?.name ?? instance.application.nodeId;
                    const site = siteByID.get(instance.application.siteId);
                    const regionCode = site ? siteRegionCode(site) : siteRegionCode({ name: instance.siteName });
                    const isEligible = eligibleIdSet.has(instance.application.id);
                    const isSelected = effectiveSelectedIds.includes(instance.application.id);
                    const skipReason = skippedReasonByID.get(instance.application.id);
                    const targetVersion = instance.app?.app.version || instance.application.availableVersion;
                    const targetRevision = instance.app?.app.version
                      ? (instance.app.app.packageRevision ?? (instance.application.availableVersion === instance.app.app.version ? instance.application.availablePackageRevision : undefined))
                      : instance.application.availablePackageRevision;
                    const sameVersionRevision = isSameVersionRevisionUpgrade({
                      fromVersion: instance.application.installedVersion,
                      toVersion: targetVersion,
                      fromRevision: instance.application.installedPackageRevision,
                      toRevision: targetRevision,
                    });
                    const role = showRoleCol ? glivelaRoleLabel(instance.application.appKey, language) : "";
                    const daylightMode = showSiteTlsCol
                      ? (instance.application.appKey === "vastora-official/daylight-https" ? "HTTPS" : "HTTP")
                      : "";
                    const manageAriaLabel = suite.kind === "glivela"
                      ? copy(language, `管理节点端：${hostName}${role ? ` · ${role}` : ""}`, `Manage Node: ${hostName}${role ? ` · ${role}` : ""}`)
                      : suite.kind === "daylight"
                        ? copy(language, `管理 Daylight：${hostName}`, `Manage Daylight: ${hostName}`)
                        : copy(language, `管理 ${hostName} 的应用`, `Manage application on ${hostName}`);

                    return (
                      <TableRow
                        data-application-id={instance.application.id}
                        data-state={isSelected ? "selected" : undefined}
                        key={instance.application.id}
                      >
                        {showCheckboxes ? (
                          <TableCell className="w-10 pl-4">
                            <Checkbox
                              aria-label={copy(language, `勾选 ${hostName}${role ? ` · ${role}` : ""}`, `Select ${hostName}${role ? ` · ${role}` : ""}`)}
                              checked={isSelected}
                              disabled={!isEligible}
                              onCheckedChange={(checked) => toggleOne(instance.application.id, Boolean(checked))}
                              title={isEligible ? undefined : skipReason || copy(language, "已是最新", "Up to date")}
                            />
                          </TableCell>
                        ) : null}
                        <TableCell className={showCheckboxes ? "min-w-44" : "min-w-44 pl-4"}>
                          <p className="truncate font-medium text-foreground" title={hostName}>{hostName}</p>
                          <p className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
                            {regionCode ? <RegionFlag code={regionCode} language={language} /> : null}
                            <span className="truncate">{instance.siteName}</span>
                          </p>
                        </TableCell>
                        {showRoleCol ? (
                          <TableCell>
                            <Badge variant="secondary">{role || "—"}</Badge>
                          </TableCell>
                        ) : null}
                        {showSiteTlsCol ? (
                          <TableCell>
                            <div className="flex flex-col items-start gap-1">
                              <AccessStatus
                                application={instance.application}
                                language={language}
                                nodeConnected={instance.agent?.connected ?? false}
                                publications={instance.publications}
                                services={instance.services}
                                threeXUI={false}
                              />
                              <Badge className="text-[10px]" variant="outline">{daylightMode}</Badge>
                            </div>
                          </TableCell>
                        ) : null}
                        <TableCell className="whitespace-nowrap tabular-nums">
                          <span className="text-foreground">v{instance.application.installedVersion}</span>
                          {instance.application.updateAvailable && targetVersion ? (
                            sameVersionRevision ? (
                              <span className="block text-xs font-medium text-success">(r{instance.application.installedPackageRevision} → r{targetRevision})</span>
                            ) : (
                              <span className="block text-xs font-medium text-success">→ v{targetVersion}</span>
                            )
                          ) : (
                            <span className="block text-xs text-muted-foreground">{copy(language, "已是最新", "Up to date")}</span>
                          )}
                        </TableCell>
                        <TableCell>
                          <div className="flex flex-col items-start gap-1">
                            <ApplicationPrimaryStatus instance={instance} language={language} />
                            {instance.agent && !instance.agent.connected ? (
                              <span className="text-xs text-destructive">{copy(language, "节点离线", "Node offline")}</span>
                            ) : instance.agent?.credentialRevoked ? (
                              <span className="text-xs text-destructive">{copy(language, "节点凭据已撤销", "Credential revoked")}</span>
                            ) : instance.application.adoptionState === "pending" || instance.application.adoptionState === "blocked" ? (
                              <span className="text-xs text-warning">{copy(language, "需完成资源接管", "Adoption required")}</span>
                            ) : skipReason && instance.application.updateAvailable && !instance.activeChange ? (
                              <span className="text-xs text-muted-foreground">{skipReason}</span>
                            ) : null}
                          </div>
                        </TableCell>
                        <TableCell className="w-[1%] whitespace-nowrap pr-4 text-right">
                          <div className="flex flex-wrap items-center justify-end gap-1.5">
                            {instance.activeChange?.reconciliationRequired && onRecoverTask ? (
                              <Button onClick={() => onRecoverTask(instance.activeChange!)} size="sm" variant="outline">
                                {copy(language, "核对恢复", "Recover")}
                              </Button>
                            ) : null}
                            <ApplicationUpdate
                              instance={instance}
                              language={language}
                              onUpgrade={onUpgrade}
                              submitting={submittingUpgradeID === instance.application.id}
                            />
                            <Button
                              aria-label={manageAriaLabel}
                              onClick={() => onManage(instance.application)}
                              size="sm"
                              variant="outline"
                            >
                              {copy(language, "管理", "Manage")}
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    );
                  }) : (
                    <TableRow>
                      <TableCell className="h-24 text-center text-muted-foreground" colSpan={colSpan}>
                        <div className="flex flex-col items-center gap-2 py-2">
                          <p className="text-sm">{copy(language, "没有符合当前筛选条件的主机", "No hosts match the current filters")}</p>
                          <Button
                            onClick={() => { setStatusFilter("all"); setSiteFilter("all"); setQuery(""); }}
                            size="xs"
                            variant="outline"
                          >
                            {copy(language, "重置筛选", "Reset filters")}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            </div>
          </>
        ) : (
          <div className="px-4 py-8 text-center text-sm text-muted-foreground">
            {suite.kind === "glivela"
              ? copy(language, "尚未安装 Glivela 节点端。选择上方节点功能并点击「安装节点端」。", "No Glivela nodes installed yet. Choose a node capability above to install.")
              : copy(language, "尚未在任何主机安装此应用。", "This app is not installed on any host yet.")}
          </div>
        )}
      </section>

      {/* Glivela Legacy Installations Review */}
      {suite.kind === "glivela" && suite.legacyInstances.length > 0 ? (
        <section className="flex flex-col gap-2.5 rounded-xl border border-warning/40 bg-muted/25 p-4">
          <div className="flex items-center gap-2">
            <ShieldAlertIcon aria-hidden="true" className="size-4 text-warning" />
            <h2 className="text-sm font-medium">{copy(language, "旧安装待核对", "Legacy installations need review")}</h2>
          </div>
          <p className="text-xs text-muted-foreground">
            {copy(language, "保留原安装记录；核对接管和停止状态后再处理。", "Existing records are preserved. Verify handover and stopped execution before proceeding.")}
          </p>
          <div className="divide-y rounded-lg border bg-card">
            {suite.legacyInstances.map((instance) => {
              const hostName = instance.agent?.name ?? instance.application.nodeId;
              return (
                <div className="flex flex-wrap items-center justify-between gap-2 px-3 py-2 text-sm" key={instance.application.id}>
                  <span className="tabular-nums">{hostName} · v{instance.application.installedVersion}</span>
                  <Button onClick={() => onManage(instance.application)} size="xs" variant="outline">
                    {copy(language, "管理", "Manage")}
                  </Button>
                </div>
              );
            })}
          </div>
        </section>
      ) : null}
    </div>
  );
}
