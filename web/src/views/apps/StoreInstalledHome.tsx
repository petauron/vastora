import { useId, useState } from "react";
import {
  ArrowUpRightIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ChevronUpIcon,
  CircleAlertIcon,
  CircleArrowUpIcon,
} from "lucide-react";
import type { AppData, Application, Deployment } from "@/types";
import type { Language } from "@/translations";
import { AppIcon } from "@/components/desktop/AppIcon";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { RegionFlag } from "../RegionFlag";
import type { InstalledAppInstance } from "../installed-apps-model";
import { copy } from "../shared";
import { planBatchUpgrade } from "./BatchUpgradeSheet";
import {
  glivelaRoleLabel,
  type StoreSuite,
  type StoreUpdateEntry,
  type SuiteStatusFilter,
} from "./store-suites-model";

export function StoreInstalledHome({
  data,
  installedSuites,
  updateEntries,
  language,
  search,
  onClearSearch,
  onBrowseAll,
  onOpenDetail,
  onOpenApp,
  onManageInstance,
  onUpgrade,
  onBatchUpgrade,
  onRecoverTask,
  submittingUpgradeID,
}: {
  data: AppData;
  installedSuites: StoreSuite[];
  updateEntries: StoreUpdateEntry[];
  language: Language;
  search: string;
  onClearSearch: () => void;
  onBrowseAll: () => void;
  onOpenDetail: (suiteId: string, statusFilter?: SuiteStatusFilter) => void;
  onOpenApp: (key: string) => void;
  onManageInstance: (application: Application) => void;
  onUpgrade: (application: Application) => void;
  onBatchUpgrade?: (title: string, instances: InstalledAppInstance[], initialSelectedIds?: string[]) => void;
  onRecoverTask?: (deployment: Deployment) => void;
  submittingUpgradeID?: string | null;
}) {
  const [expandedEntries, setExpandedEntries] = useState<Record<string, boolean>>({});
  const updatableSuiteIds = new Set(updateEntries.map((entry) => entry.suiteId));
  // Synology-style deduplication: suites with available updates appear only in the top Updates section.
  const upToDateSuites = installedSuites.filter((suite) => !updatableSuiteIds.has(suite.id));

  const allUpdatableInstances = updateEntries.flatMap((entry) => entry.updatableInstances);
  const allBatchTitle = copy(language, "全部可用套件", "all available apps");
  const allBatchPlan = allUpdatableInstances.length > 0 && onBatchUpgrade
    ? planBatchUpgrade(allBatchTitle, allUpdatableInstances, data, language)
    : null;
  const totalSkipped = allBatchPlan?.skipped.length ?? updateEntries.reduce((sum, entry) => sum + entry.plan.skipped.length, 0);
  const totalEligible = allBatchPlan?.eligible.length ?? updateEntries.reduce((sum, entry) => sum + entry.plan.eligible.length, 0);

  const toggleEntry = (id: string) => {
    setExpandedEntries((current) => ({ ...current, [id]: !current[id] }));
  };

  if (updateEntries.length === 0 && upToDateSuites.length === 0) {
    return (
      <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed px-5 py-14 text-center">
        <AppIcon appKey="apps" className="size-12" />
        <p className="text-sm text-muted-foreground">
          {search ? copy(language, "没有匹配的已安装应用", "No matching installed apps") : copy(language, "还没有安装应用", "No apps installed")}
        </p>
        <Button onClick={search ? onClearSearch : onBrowseAll} variant="outline">
          {search ? copy(language, "清除搜索", "Clear search") : copy(language, "浏览所有应用", "Browse all apps")}
        </Button>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-7">
      {updateEntries.length > 0 ? (
        <section aria-labelledby="store-available-updates-heading" className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
              <h2 className="text-sm font-semibold text-foreground" id="store-available-updates-heading">
                {copy(language, "可用更新", "Available updates")}
              </h2>
              <span className="text-xs text-muted-foreground tabular-nums" role="status">
                {copy(
                  language,
                  `${updateEntries.length} 项 · ${totalEligible} 台在线可更新${totalSkipped > 0 ? ` · ${totalSkipped} 台自动跳过` : ""}`,
                  `${updateEntries.length} item(s) · ${totalEligible} online ready${totalSkipped > 0 ? ` · ${totalSkipped} skipped` : ""}`,
                )}
              </span>
            </div>
            {allBatchPlan && allBatchPlan.eligible.length > 0 ? (
              <Button
                className="store-btn-update"
                onClick={() => onBatchUpgrade?.(allBatchTitle, allUpdatableInstances)}
                size="sm"
              >
                <CircleArrowUpIcon aria-hidden="true" data-icon="inline-start" />
                {copy(language, `更新全部 (${allBatchPlan.eligible.length})`, `Update all (${allBatchPlan.eligible.length})`)}
              </Button>
            ) : null}
          </div>

          <div className="store-updates-list">
            {updateEntries.map((entry) => (
              <StoreUpdateRow
                entry={entry}
                expanded={Boolean(expandedEntries[entry.id])}
                key={entry.id}
                language={language}
                onBatchUpgrade={onBatchUpgrade}
                onManageInstance={onManageInstance}
                onOpenApp={onOpenApp}
                onOpenDetail={onOpenDetail}
                onRecoverTask={onRecoverTask}
                onToggleExpand={() => toggleEntry(entry.id)}
                onUpgrade={onUpgrade}
                submittingUpgradeID={submittingUpgradeID}
              />
            ))}
          </div>
        </section>
      ) : null}

      <section aria-labelledby="store-installed-grid-heading" className="flex flex-col gap-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
            <h2 className="text-sm font-semibold text-foreground" id="store-installed-grid-heading">
              {copy(language, "已安装", "Installed")}
            </h2>
            <span className="text-xs text-muted-foreground tabular-nums">
              {updateEntries.length > 0
                ? copy(language, `${upToDateSuites.length} 个已是最新`, `${upToDateSuites.length} up to date`)
                : copy(language, `${upToDateSuites.length} 个应用`, `${upToDateSuites.length} applications`)}
            </span>
          </div>
        </div>

        {upToDateSuites.length > 0 ? (
          <div className="store-installed-grid">
            {upToDateSuites.map((suite) => (
              <StoreInstalledCard
                key={suite.id}
                language={language}
                onOpenApp={onOpenApp}
                onOpenDetail={onOpenDetail}
                suite={suite}
              />
            ))}
          </div>
        ) : (
          <p className="rounded-xl border border-dashed px-4 py-5 text-center text-xs text-muted-foreground">
            {copy(language, "所有已安装套件均有可用更新，已在上方「可用更新」中列出。", "All installed apps have updates listed under Available updates above.")}
          </p>
        )}
      </section>
    </div>
  );
}

function StoreUpdateRow({
  entry,
  expanded,
  language,
  onToggleExpand,
  onOpenDetail,
  onOpenApp,
  onManageInstance,
  onUpgrade,
  onBatchUpgrade,
  onRecoverTask,
  submittingUpgradeID,
}: {
  entry: StoreUpdateEntry;
  expanded: boolean;
  language: Language;
  onToggleExpand: () => void;
  onOpenDetail: (suiteId: string, statusFilter?: SuiteStatusFilter) => void;
  onOpenApp: (key: string) => void;
  onManageInstance: (application: Application) => void;
  onUpgrade: (application: Application) => void;
  onBatchUpgrade?: (title: string, instances: InstalledAppInstance[], initialSelectedIds?: string[]) => void;
  onRecoverTask?: (deployment: Deployment) => void;
  submittingUpgradeID?: string | null;
}) {
  const titleId = useId();
  const singleInstance = entry.updatableInstances.length === 1 ? entry.updatableInstances[0] : undefined;
  const singleSubmitting = Boolean(entry.singleEligible && submittingUpgradeID === entry.singleEligible.instance.application.id);
  const canExpandHosts = entry.updatableInstances.length > 1 || entry.plan.skipped.length > 0;
  const visibleSites = entry.siteBreakdown.slice(0, 4);
  const extraSiteCount = Math.max(0, entry.siteBreakdown.length - visibleSites.length);

  return (
    <article aria-labelledby={titleId} className="store-update-row" role="article">
      <div className="store-update-row-main">
        <div className="store-update-identity">
          <AppIcon appKey={entry.appKey} className="size-11 shrink-0" />
          <div className="min-w-0">
            <h3 className="text-sm font-semibold leading-5" id={titleId}>
              <button
                className="store-app-title"
                onClick={() => onOpenDetail(entry.suiteId)}
                title={entry.title}
                type="button"
              >
                {entry.title}
              </button>
            </h3>
            <p className="mt-0.5 text-xs font-medium text-success tabular-nums">
              {entry.updatableInstances.length === 1
                ? copy(language, "有可用更新", "Update available")
                : copy(language, `${entry.updatableInstances.length} 台有可用更新`, `${entry.updatableInstances.length} updates available`)}
            </p>
          </div>
        </div>

        <div className="store-update-details">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs tabular-nums">
            <span className="font-medium text-foreground">
              {copy(language, "最新在线版本：", "Latest version: ")}
              {entry.targetVersions.length > 0 ? entry.targetVersions.join(" / ") : "—"}
            </span>
            {entry.installedVersions.length > 0 ? (
              <span className="text-muted-foreground">
                · {copy(language, "当前 ", "Current ")}
                {entry.installedVersions.length === 1
                  ? entry.installedVersions[0]
                  : copy(language, `${entry.installedVersions.length} 个版本`, `${entry.installedVersions.length} versions`)}
              </span>
            ) : null}
            {entry.suite.hasRecovery ? (
              <Badge variant="destructive">
                <CircleAlertIcon aria-hidden="true" data-icon="inline-start" />
                {copy(language, "含需恢复任务", "Recovery required")}
              </Badge>
            ) : null}
          </div>

          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            {singleInstance ? (
              <span className="inline-flex items-center gap-1.5">
                {entry.siteBreakdown[0]?.regionCode ? <RegionFlag code={entry.siteBreakdown[0].regionCode} language={language} /> : null}
                <span>{singleInstance.agent?.name ?? singleInstance.application.nodeId}</span>
                <span aria-hidden="true">·</span>
                <span>{singleInstance.siteName}</span>
                {entry.plan.skipped[0] ? (
                  <Badge className="ml-1" variant="secondary">{entry.plan.skipped[0].reason}</Badge>
                ) : null}
              </span>
            ) : (
              <>
                {visibleSites.map((site, index) => (
                  <span className="inline-flex items-center gap-1 tabular-nums" key={site.siteId}>
                    {index > 0 ? <span aria-hidden="true" className="mr-1 text-border">·</span> : null}
                    {site.regionCode ? <RegionFlag code={site.regionCode} language={language} /> : null}
                    <span>{site.siteName} ({site.total})</span>
                  </span>
                ))}
                {extraSiteCount > 0 ? (
                  <span>{copy(language, `等 ${entry.siteBreakdown.length} 个站点`, `+${extraSiteCount} more sites`)}</span>
                ) : null}
                {entry.plan.skipped.length > 0 ? (
                  <span className="text-warning">
                    · {copy(language, `${entry.plan.skipped.length} 台自动跳过`, `${entry.plan.skipped.length} skipped`)}
                  </span>
                ) : null}
              </>
            )}
          </div>

          {canExpandHosts ? (
            <div className="mt-1.5">
              <button
                aria-expanded={expanded}
                className="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline"
                onClick={onToggleExpand}
                type="button"
              >
                {expanded ? <ChevronUpIcon aria-hidden="true" className="size-3.5" /> : <ChevronDownIcon aria-hidden="true" className="size-3.5" />}
                {expanded
                  ? copy(language, "收起待更新节点", "Hide pending hosts")
                  : copy(language, `查看待更新节点 (${entry.updatableInstances.length})`, `Inspect hosts (${entry.updatableInstances.length})`)}
              </button>
            </div>
          ) : null}
        </div>

        <div className="store-update-actions">
          <Button
            aria-label={entry.manageAriaLabel}
            onClick={() => onOpenDetail(entry.suiteId)}
            size="sm"
            variant="outline"
          >
            {copy(language, "管理", "Manage")}
          </Button>
          {entry.showOpen ? (
            entry.suite.launchUrl ? (
              <Button
                aria-label={copy(language, `在新标签页打开 ${entry.suite.name}`, `Open ${entry.suite.name} in a new tab`)}
                nativeButton={false}
                render={<a href={entry.suite.launchUrl} rel="noreferrer" target="_blank" />}
                size="sm"
                variant="outline"
              >
                {copy(language, "打开", "Open")}
                <ArrowUpRightIcon aria-hidden="true" data-icon="inline-end" />
              </Button>
            ) : entry.suite.openTargetKey ? (
              <Button
                aria-label={copy(language, `打开 ${entry.suite.name}`, `Open ${entry.suite.name}`)}
                onClick={() => onOpenApp(entry.suite.openTargetKey!)}
                size="sm"
                variant="outline"
              >
                {copy(language, "打开", "Open")}
              </Button>
            ) : null
          ) : null}
          {entry.singleEligible ? (
            <Button
              aria-label={copy(language, `更新 ${entry.title}`, `Update ${entry.title}`)}
              className="store-btn-update"
              disabled={singleSubmitting}
              onClick={() => onUpgrade(entry.singleEligible!.instance.application)}
              size="sm"
            >
              {singleSubmitting ? <Spinner aria-hidden="true" className="motion-reduce:animate-none" data-icon="inline-start" /> : null}
              {singleSubmitting ? copy(language, "更新中…", "Updating…") : copy(language, "更新", "Update")}
            </Button>
          ) : entry.plan.eligible.length > 0 ? (
            <Button
              aria-label={copy(language, `更新 ${entry.title} (${entry.plan.eligible.length} 台)`, `Update ${entry.title} (${entry.plan.eligible.length})`)}
              className="store-btn-update"
              onClick={() => onBatchUpgrade?.(entry.batchTitle, entry.updatableInstances)}
              size="sm"
            >
              {copy(language, `更新 (${entry.plan.eligible.length})`, `Update (${entry.plan.eligible.length})`)}
            </Button>
          ) : (
            <Button
              disabled
              size="sm"
              title={entry.plan.skipped[0]?.reason}
              variant="outline"
            >
              {copy(language, "暂不可更新", "Unavailable")}
            </Button>
          )}
        </div>
      </div>

      {expanded && canExpandHosts ? (
        <div className="store-update-expanded">
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {entry.plan.eligible.map(({ instance, app }) => {
              const hostName = instance.agent?.name ?? instance.application.nodeId;
              const role = glivelaRoleLabel(instance.application.appKey, language, true);
              const busy = submittingUpgradeID === instance.application.id;
              return (
                <div className="store-update-host-chip" key={instance.application.id}>
                  <span aria-hidden="true" className="apps-status-dot bg-latency-fast" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-xs font-medium text-foreground" title={hostName}>
                      {hostName}
                      {role ? <span className="ml-1 font-normal text-muted-foreground">· {role}</span> : null}
                    </p>
                    <p className="truncate text-[11px] text-muted-foreground tabular-nums">
                      {instance.siteName} · v{instance.application.installedVersion} → v{app.app.version}
                    </p>
                  </div>
                  <Button
                    aria-label={copy(language, `更新 ${hostName} 的应用`, `Update application on ${hostName}`)}
                    disabled={busy}
                    onClick={() => onUpgrade(instance.application)}
                    size="xs"
                    variant="outline"
                  >
                    {busy ? <Spinner aria-hidden="true" className="motion-reduce:animate-none" data-icon="inline-start" /> : null}
                    {busy ? copy(language, "更新中", "Updating") : copy(language, "更新", "Update")}
                  </Button>
                </div>
              );
            })}
            {entry.plan.skipped.map(({ instance, reason }) => {
              const hostName = instance.agent?.name ?? instance.application.nodeId;
              const needsRecovery = Boolean(instance.activeChange?.reconciliationRequired);
              return (
                <div className="store-update-host-chip store-update-host-chip-skipped" key={instance.application.id}>
                  <span aria-hidden="true" className={`apps-status-dot ${needsRecovery ? "bg-destructive" : "bg-warning"}`} />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-xs font-medium text-foreground" title={hostName}>{hostName}</p>
                    <p className="truncate text-[11px] text-muted-foreground">{instance.siteName} · {reason}</p>
                  </div>
                  {needsRecovery && onRecoverTask ? (
                    <Button onClick={() => onRecoverTask(instance.activeChange!)} size="xs" variant="outline">
                      {copy(language, "核对恢复", "Recover")}
                    </Button>
                  ) : (
                    <Button onClick={() => onManageInstance(instance.application)} size="xs" variant="ghost">
                      {copy(language, "详情", "Details")}
                    </Button>
                  )}
                </div>
              );
            })}
          </div>
          <div className="mt-2.5 flex justify-end">
            <button
              className="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline"
              onClick={() => onOpenDetail(entry.suiteId, "updatable")}
              type="button"
            >
              {copy(
                language,
                `在主机工作台中查看全部 ${entry.suite.counts.hosts} 台主机并筛选 / 勾选更新`,
                `Open host workbench (${entry.suite.counts.hosts} hosts) to filter & select`,
              )}
              <ChevronRightIcon aria-hidden="true" className="size-3.5" />
            </button>
          </div>
        </div>
      ) : null}
    </article>
  );
}

function StoreInstalledCard({
  suite,
  language,
  onOpenDetail,
  onOpenApp,
}: {
  suite: StoreSuite;
  language: Language;
  onOpenDetail: (suiteId: string, statusFilter?: SuiteStatusFilter) => void;
  onOpenApp: (key: string) => void;
}) {
  const titleId = useId();
  const singleInstance = suite.instances.length === 1 ? suite.instances[0] : undefined;
  const versionSummary = suite.installedVersions.length === 1
    ? suite.installedVersions[0]
    : suite.installedVersions.length > 1
      ? copy(language, `${suite.installedVersions.length} 个版本`, `${suite.installedVersions.length} versions`)
      : "";

  let subtitle = "";
  if (suite.kind === "glivela") {
    const ctrlCount = suite.controllerInstances.length;
    const nodeHosts = new Set(suite.nodeInstances.map((item) => item.application.nodeId)).size;
    subtitle = copy(
      language,
      `控制端 ${ctrlCount} · 节点端 ${nodeHosts} 台`,
      `Controller ${ctrlCount} · ${nodeHosts} node(s)`,
    );
  } else if (singleInstance) {
    const hostName = singleInstance.agent?.name ?? singleInstance.application.nodeId;
    subtitle = versionSummary ? `${hostName} · ${versionSummary}` : hostName;
  } else {
    subtitle = copy(
      language,
      `${suite.counts.hosts} 台主机${versionSummary ? ` · ${versionSummary}` : ""}`,
      `${suite.counts.hosts} hosts${versionSummary ? ` · ${versionSummary}` : ""}`,
    );
  }

  const hasOpen = !suite.background && Boolean(suite.launchUrl || suite.canOpenWorkspace);

  return (
    <article aria-labelledby={titleId} className="store-installed-card" role="article">
      <div className="flex items-start gap-3">
        <AppIcon appKey={suite.appKey} className="size-11 shrink-0" />
        <div className="min-w-0 flex-1">
          <div className="flex items-start justify-between gap-2">
            <h3 className="min-w-0 text-sm font-semibold leading-5" id={titleId}>
              <button
                className="store-app-title"
                onClick={() => onOpenDetail(suite.id)}
                title={suite.name}
                type="button"
              >
                {suite.name}
              </button>
            </h3>
            <SuiteStatusPill language={language} suite={suite} />
          </div>
          <p className="mt-1 truncate text-xs text-muted-foreground tabular-nums" title={subtitle}>
            {subtitle}
          </p>
        </div>
      </div>

      <div className="mt-auto flex flex-wrap items-center gap-2 pl-14">
        <Button
          aria-label={copy(language, `管理 ${suite.name}`, `Manage ${suite.name}`)}
          onClick={() => onOpenDetail(suite.id)}
          size="sm"
          variant={hasOpen ? "outline" : "default"}
        >
          {copy(language, "管理", "Manage")}
        </Button>
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
              onClick={() => onOpenApp(suite.openTargetKey!)}
              size="sm"
              variant="default"
            >
              {copy(language, "打开", "Open")}
            </Button>
          ) : null
        ) : null}
      </div>
    </article>
  );
}

function SuiteStatusPill({ suite, language }: { suite: StoreSuite; language: Language }) {
  if (suite.counts.attention > 0) {
    return (
      <Badge className="shrink-0 tabular-nums" variant="destructive">
        <CircleAlertIcon aria-hidden="true" data-icon="inline-start" />
        {copy(language, `${suite.counts.attention} 台需处理`, `${suite.counts.attention} attention`)}
      </Badge>
    );
  }
  if (suite.counts.progress > 0) {
    return (
      <Badge className="shrink-0 tabular-nums" variant="outline">
        <Spinner aria-hidden="true" className="motion-reduce:animate-none" data-icon="inline-start" />
        {copy(language, `${suite.counts.progress} 台处理中`, `${suite.counts.progress} active`)}
      </Badge>
    );
  }
  if (suite.counts.offline > 0) {
    return (
      <Badge className="shrink-0 tabular-nums" variant="secondary">
        {copy(language, `${suite.counts.offline} 台离线`, `${suite.counts.offline} offline`)}
      </Badge>
    );
  }
  if (suite.counts.running > 0) {
    return (
      <span className="inline-flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
        <span aria-hidden="true" className="apps-status-dot bg-latency-fast" />
        {copy(language, "运行中", "Running")}
      </span>
    );
  }
  return <span className="shrink-0 text-xs text-muted-foreground">{copy(language, "未运行", "Stopped")}</span>;
}
