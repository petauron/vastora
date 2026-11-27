import { useEffect, useMemo, useState, type FormEvent } from "react";
import { CircleArrowUpIcon, SearchIcon } from "lucide-react";
import type { AppData } from "../../App";
import type { AgentView, AppView } from "../../types";
import type { Language } from "../../translations";
import { siteRegionCode } from "@/lib/regions";
import { localized, packageNodeBlocker } from "../appAccess";
import { isDaylightAppKey, daylightAppKey } from "../daylight";
import { threeXUIAppKey, type InstalledAppInstance } from "../installed-apps-model";
import { catalogInstallBlocked, copy } from "../shared";
import { RegionFlag } from "../RegionFlag";
import { capabilityLabel } from "./DeploymentSheets";
import { AppIcon } from "@/components/desktop/AppIcon";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Progress, ProgressLabel, ProgressValue } from "@/components/ui/progress";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";

export type BatchUpgradeTarget = {
  instance: InstalledAppInstance;
  app: AppView;
  agent: AgentView;
  label: string;
};

export type BatchUpgradeSkipped = {
  instance: InstalledAppInstance;
  label: string;
  reason: string;
};

export type BatchUpgradePlan = {
  title: string;
  eligible: BatchUpgradeTarget[];
  skipped: BatchUpgradeSkipped[];
};

export type BatchUpgradeSnapshot = {
  version: string;
  packageRevision: number;
  manifestSha256: string;
};

export type BatchUpgradeRequest = {
  title: string;
  applicationIds: string[];
  initialSelectedIds?: string[];
  snapshots: Record<string, BatchUpgradeSnapshot>;
};

export type BatchUpgradeOutcome = {
  queued: number;
  failed: Array<{ applicationId: string; name: string; error: string }>;
};

const batchGlivelaKeys = new Set([
  "vastora-official/meridian",
  "vastora-official/glivela-entry",
  "vastora-official/glivela-node",
  "vastora-official/glivela-dual",
]);

function batchSuiteGroupMeta(app: AppView, language: Language) {
  if (batchGlivelaKeys.has(app.key)) {
    return { suiteId: "vastora-official/meridian", name: "Glivela" };
  }
  if (isDaylightAppKey(app.key)) {
    return { suiteId: daylightAppKey, name: "Daylight" };
  }
  return { suiteId: app.key, name: localized(app, language, "name") };
}

function recipeDisambiguator(appKey: string, app: AppView | undefined, language: Language) {
  const glivelaLabels: Record<string, [string, string]> = {
    "vastora-official/glivela-entry": ["接收客户端连接", "Client entry"],
    "vastora-official/glivela-node": ["提供出口访问", "Outbound exit"],
    "vastora-official/glivela-dual": ["入口与出口", "Entry & exit"],
  };
  const glivela = glivelaLabels[appKey];
  if (glivela) return copy(language, ...glivela);
  if (appKey === "vastora-official/daylight-https") return "HTTPS";
  if (appKey === "vastora-official/daylight") return "HTTP";
  return app ? localized(app, language, "name") : appKey;
}

function formatTargetLabel(
  instance: InstalledAppInstance,
  app: AppView | undefined,
  includeRecipe: boolean,
  language: Language,
) {
  const host = instance.agent?.name ?? instance.application.nodeId;
  if (!includeRecipe) return host;
  return `${host} · ${recipeDisambiguator(instance.application.appKey, app, language)}`;
}

function isLegacyController(instance: InstalledAppInstance) {
  const { application } = instance;
  return application.appKey === threeXUIAppKey
    && application.role === "master"
    && Boolean(application.controllerApplicationId)
    && application.id !== application.controllerApplicationId;
}

export function planBatchUpgrade(
  title: string,
  instances: InstalledAppInstance[],
  data: AppData,
  language: Language,
): BatchUpgradePlan {
  const catalogByKey = new Map(data.apps.map((app) => [app.key, app]));
  const updatable = instances.filter((instance) => instance.application.updateAvailable && !isLegacyController(instance));
  const includeRecipe = new Set(updatable.map((instance) => instance.application.appKey)).size > 1;
  const eligible: BatchUpgradeTarget[] = [];
  const skipped: BatchUpgradeSkipped[] = [];

  for (const instance of updatable) {
    const { application, agent, activeChange } = instance;
    const app = catalogByKey.get(application.appKey);
    const label = formatTargetLabel(instance, app, includeRecipe, language);

    const hasActiveTask = Boolean(activeChange)
      || ["pending", "deploying"].includes(application.status)
      || data.deployments.some((task) => task.agentId === application.nodeId
        && task.appKey === application.appKey
        && (task.state === "pending" || task.state === "running" || task.reconciliationRequired));
    if (hasActiveTask) {
      skipped.push({ instance, label, reason: copy(language, "已有进行中的任务", "Task already in progress") });
      continue;
    }
    if (application.adoptionState === "pending" || application.adoptionState === "blocked") {
      skipped.push({ instance, label, reason: copy(language, "需先完成资源接管", "Resource adoption required") });
      continue;
    }
    if (!agent || !agent.connected) {
      skipped.push({ instance, label, reason: copy(language, "节点离线", "Node offline") });
      continue;
    }
    if (agent.credentialRevoked) {
      skipped.push({ instance, label, reason: copy(language, "节点凭据已撤销", "Node credential revoked") });
      continue;
    }
    if (!app || catalogInstallBlocked(app)) {
      skipped.push({ instance, label, reason: copy(language, "请先刷新应用目录", "Refresh the app catalog first") });
      continue;
    }
    const blocker = packageNodeBlocker(agent, app, language);
    if (blocker) {
      skipped.push({ instance, label, reason: blocker });
      continue;
    }
    eligible.push({ instance, app, agent, label });
  }

  return { title, eligible, skipped };
}

export function createBatchUpgradeRequest(
  title: string,
  instances: InstalledAppInstance[],
  data: AppData,
  initialSelectedIds?: string[],
): BatchUpgradeRequest {
  const catalogByKey = new Map(data.apps.map((app) => [app.key, app]));
  const snapshots: Record<string, BatchUpgradeSnapshot> = {};
  const updatable = instances.filter((instance) => instance.application.updateAvailable && !isLegacyController(instance));
  for (const instance of updatable) {
    const app = catalogByKey.get(instance.application.appKey);
    if (!app || catalogInstallBlocked(app) || snapshots[app.key]) continue;
    snapshots[app.key] = {
      version: app.app.version,
      packageRevision: app.app.packageRevision ?? 0,
      manifestSha256: app.manifestSha256 ?? "",
    };
  }
  return {
    title,
    applicationIds: updatable.map((instance) => instance.application.id),
    initialSelectedIds: initialSelectedIds?.length ? [...initialSelectedIds] : undefined,
    snapshots,
  };
}

export function BatchUpgradeSheet({
  request,
  data,
  instancesByID,
  language,
  onClose,
  onSubmit,
}: {
  request: BatchUpgradeRequest | null;
  data: AppData;
  instancesByID: Map<string, InstalledAppInstance>;
  language: Language;
  onClose: () => void;
  onSubmit: (
    targets: BatchUpgradeTarget[],
    approvedCapabilities: string[],
    onProgress: (done: number, total: number) => void,
  ) => Promise<BatchUpgradeOutcome>;
}) {
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [approvedCapabilities, setApprovedCapabilities] = useState<string[]>([]);
  const [searchQuery, setSearchQuery] = useState("");
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null);
  const [result, setResult] = useState<BatchUpgradeOutcome | null>(null);
  const [error, setError] = useState("");

  const scopedInstances = useMemo(
    () => (request ? request.applicationIds.map((id) => instancesByID.get(id)).filter((value): value is InstalledAppInstance => Boolean(value)) : []),
    [request, instancesByID],
  );
  const plan = useMemo(
    () => (request ? planBatchUpgrade(request.title, scopedInstances, data, language) : null),
    [request, scopedInstances, data, language],
  );
  const catalogByKey = useMemo(() => new Map(data.apps.map((app) => [app.key, app])), [data.apps]);
  const siteByID = useMemo(() => new Map(data.sites.map((site) => [site.id, site])), [data.sites]);

  useEffect(() => {
    if (!request) return;
    const initialInstances = request.applicationIds.map((id) => instancesByID.get(id)).filter((value): value is InstalledAppInstance => Boolean(value));
    const initialPlan = planBatchUpgrade(request.title, initialInstances, data, language);
    const eligibleIds = initialPlan.eligible.map((item) => item.instance.application.id);
    const preselected = request.initialSelectedIds?.filter((id) => eligibleIds.includes(id)) ?? [];
    setSelectedIds(preselected.length > 0 ? preselected : eligibleIds);
    setApprovedCapabilities([]);
    setSearchQuery("");
    setBusy(false);
    setProgress(null);
    setResult(null);
    setError("");
    // Only reset local selection when a new batch confirmation session opens.
  }, [request]);

  const eligible = plan?.eligible ?? [];
  const skipped = plan?.skipped ?? [];
  const chosen = eligible.filter((item) => selectedIds.includes(item.instance.application.id));
  const capabilitySource = chosen.length > 0 ? chosen : eligible;
  const requiredCapabilities = [...new Set(capabilitySource.flatMap((item) => item.app.app.runtime?.requiredCapabilities ?? []))];
  const permissionDetails = [...new Set(capabilitySource.flatMap((item) => item.app.permissionDetails ?? []))];
  const permissionsApproved = requiredCapabilities.every((capability) => approvedCapabilities.includes(capability));

  const activeTargets = chosen.length > 0 ? chosen : eligible;
  const activeRecipeKeys = [...new Set(activeTargets.map((item) => item.app.key))];
  const snapshotKeys = request ? Object.keys(request.snapshots) : [];
  const checkedKeys = activeRecipeKeys.length > 0 ? activeRecipeKeys : snapshotKeys;
  const unreviewedRecipe = Boolean(request) && activeRecipeKeys.some((key) => !request?.snapshots[key]);
  const catalogBlocked = checkedKeys.some((key) => {
    const current = catalogByKey.get(key);
    return !current || catalogInstallBlocked(current);
  });
  const catalogChanged = unreviewedRecipe || checkedKeys.some((key) => {
    const snap = request?.snapshots[key];
    const current = catalogByKey.get(key);
    if (!snap || !current) return false;
    return current.app.version !== snap.version
      || (current.app.packageRevision ?? 0) !== snap.packageRevision
      || (current.manifestSha256 ?? "") !== snap.manifestSha256;
  });
  const catalogMessage = catalogBlocked
    ? copy(language, "请先在控制面板刷新应用目录，再安装或升级。已安装应用不受影响。", "Refresh the app catalog in Control Panel before installing or upgrading. Installed apps are unaffected.")
    : catalogChanged
      ? copy(language, "目录中的版本已更新，请关闭并重新打开此窗口，确认新版本后继续。", "The catalog version changed. Reopen this window to review the new version before continuing.")
      : "";

  const targetVersions = [...new Set(eligible.map((item) => `v${item.app.app.version}`))];
  const allSelected = eligible.length > 0 && chosen.length === eligible.length;

  const siteChips = useMemo(() => {
    const map = new Map<string, { siteId: string; siteName: string; regionCode: string; ids: string[] }>();
    for (const target of eligible) {
      const siteId = target.instance.application.siteId;
      const site = siteByID.get(siteId);
      const siteName = site?.name ?? target.instance.siteName;
      const existing = map.get(siteId);
      if (existing) existing.ids.push(target.instance.application.id);
      else {
        map.set(siteId, {
          siteId,
          siteName,
          regionCode: site ? siteRegionCode(site) : siteRegionCode({ name: siteName }),
          ids: [target.instance.application.id],
        });
      }
    }
    return [...map.values()].sort((left, right) => right.ids.length - left.ids.length || left.siteName.localeCompare(right.siteName, language));
  }, [eligible, siteByID, language]);

  const normalizedSearch = searchQuery.trim().toLocaleLowerCase();
  const visibleEligible = useMemo(() => {
    if (!normalizedSearch) return eligible;
    return eligible.filter((item) => [
      item.label,
      item.agent.name,
      item.instance.siteName,
      localized(item.app, language, "name"),
      item.app.key,
    ].some((value) => value.toLocaleLowerCase().includes(normalizedSearch)));
  }, [eligible, normalizedSearch, language]);

  const groupedEligible = useMemo(() => {
    const groups = new Map<string, { suiteId: string; name: string; versions: Set<string>; items: BatchUpgradeTarget[] }>();
    for (const item of visibleEligible) {
      const meta = batchSuiteGroupMeta(item.app, language);
      const existing = groups.get(meta.suiteId);
      if (existing) {
        existing.versions.add(`v${item.app.app.version}`);
        existing.items.push(item);
      } else {
        groups.set(meta.suiteId, {
          suiteId: meta.suiteId,
          name: meta.name,
          versions: new Set([`v${item.app.app.version}`]),
          items: [item],
        });
      }
    }
    return [...groups.values()];
  }, [visibleEligible, language]);

  const showGroupHeaders = new Set(eligible.map((item) => batchSuiteGroupMeta(item.app, language).suiteId)).size > 1;
  const showFilterControls = eligible.length > 5 || siteChips.length > 1;

  const updateSelection = (nextIds: string[]) => {
    const prevChosen = eligible.filter((item) => selectedIds.includes(item.instance.application.id));
    const nextChosen = eligible.filter((item) => nextIds.includes(item.instance.application.id));
    const prevKeys = new Set(prevChosen.map((item) => item.app.key));
    const nextKeys = new Set(nextChosen.map((item) => item.app.key));
    const addedRecipe = [...nextKeys].some((key) => !prevKeys.has(key));
    setSelectedIds(nextIds);
    if (addedRecipe) {
      setApprovedCapabilities([]);
    } else {
      const nextRequired = new Set((nextChosen.length > 0 ? nextChosen : eligible).flatMap((item) => item.app.app.runtime?.requiredCapabilities ?? []));
      setApprovedCapabilities((current) => current.filter((capability) => nextRequired.has(capability)));
    }
  };

  const toggleAll = () => {
    updateSelection(allSelected ? [] : eligible.map((item) => item.instance.application.id));
  };

  const toggleSubset = (subsetIds: string[]) => {
    const allInSubsetSelected = subsetIds.length > 0 && subsetIds.every((id) => selectedIds.includes(id));
    const nextIds = allInSubsetSelected
      ? selectedIds.filter((id) => !subsetIds.includes(id))
      : [...new Set([...selectedIds, ...subsetIds])];
    updateSelection(nextIds);
  };

  const toggleOne = (applicationId: string, checked: boolean) => {
    const nextIds = checked
      ? [...selectedIds.filter((id) => id !== applicationId), applicationId]
      : selectedIds.filter((id) => id !== applicationId);
    updateSelection(nextIds);
  };

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!request || busy || chosen.length === 0) return;
    if (!permissionsApproved) {
      setError(copy(language, "请先确认此配方需要的每项权限。", "Approve each permission required by this recipe first."));
      return;
    }
    const expiredNow = chosen.some((target) => {
      const current = catalogByKey.get(target.app.key);
      return !current || catalogInstallBlocked(current);
    });
    const unreviewedNow = chosen.some((target) => !request.snapshots[target.app.key]);
    if (expiredNow || unreviewedNow || catalogBlocked || catalogChanged) {
      setError(catalogMessage || copy(language, "目录需要重新验证，请先刷新应用目录。", "Refresh the app catalog to verify it again."));
      return;
    }

    setBusy(true);
    setError("");
    setResult(null);
    setProgress({ done: 0, total: chosen.length });
    try {
      const outcome = await onSubmit(chosen, approvedCapabilities, (done, total) => {
        setProgress({ done, total });
      });
      if (outcome.failed.length === 0) {
        onClose();
      } else {
        setResult(outcome);
        setSelectedIds(outcome.failed.map((item) => item.applicationId));
      }
    } finally {
      setBusy(false);
      setProgress(null);
    }
  };

  const progressText = progress ? copy(language, `正在提交 ${progress.done}/${progress.total} 台…`, `Submitting ${progress.done}/${progress.total}…`) : "";

  const renderTargetRow = ({ instance, app, label }: BatchUpgradeTarget) => {
    const checked = selectedIds.includes(instance.application.id);
    const id = `batch-target-${instance.application.id}`;
    return (
      <div className="flex items-center gap-3 px-3 py-2.5 text-sm hover:bg-muted/40" key={instance.application.id}>
        <Checkbox
          aria-label={copy(language, `选择 ${label}`, `Select ${label}`)}
          checked={checked}
          disabled={busy}
          id={id}
          onCheckedChange={(value) => toggleOne(instance.application.id, Boolean(value))}
        />
        <div className="min-w-0 flex-1">
          <FieldLabel className="block cursor-pointer truncate font-medium" htmlFor={id}>{label}</FieldLabel>
          {siteChips.length > 1 ? (
            <span className="block truncate text-[11px] text-muted-foreground">{instance.siteName}</span>
          ) : null}
        </div>
        <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
          v{instance.application.installedVersion} → v{app.app.version}
        </span>
      </div>
    );
  };

  return (
    <Sheet open={Boolean(request)} onOpenChange={(open) => { if (!open && !busy) onClose(); }}>
      <SheetContent className="sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{request ? copy(language, `批量更新 ${request.title}`, `Update ${request.title}`) : ""}</SheetTitle>
          <SheetDescription>
            {targetVersions.length === 1
              ? copy(language, `目标版本：${targetVersions[0]}。保留现有配置、凭据和访问入口。`, `Target version: ${targetVersions[0]}. Existing settings, credentials and access points are preserved.`)
              : copy(language, "更新到当前应用目录中的最新版本。保留现有配置、凭据和访问入口。", "Update to the latest catalog version. Existing settings, credentials and access points are preserved.")}
          </SheetDescription>
        </SheetHeader>
        <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}>
          <SheetBody>
            {catalogMessage ? (
              <Alert id="batch-upgrade-catalog-error" role="status">
                <AlertTitle>{copy(language, "暂不能继续", "Cannot continue yet")}</AlertTitle>
                <AlertDescription>{catalogMessage}</AlertDescription>
              </Alert>
            ) : null}

            {requiredCapabilities.length > 0 ? (
              <FieldSet>
                <FieldLegend>{copy(language, "此配方需要管理员授权", "This recipe requires administrator approval")}</FieldLegend>
                <FieldDescription>{copy(language, "本次批量更新只需确认一次，应用到所选全部主机。", "Approve once to apply across all selected hosts in this batch.")}</FieldDescription>
                <FieldGroup>
                  {requiredCapabilities.map((capability) => (
                    <Field key={capability} orientation="horizontal">
                      <Checkbox
                        checked={approvedCapabilities.includes(capability)}
                        disabled={busy}
                        id={`batch-capability-${capability}`}
                        onCheckedChange={(checked) => setApprovedCapabilities((current) => checked
                          ? [...current.filter((value) => value !== capability), capability]
                          : current.filter((value) => value !== capability))}
                      />
                      <FieldLabel htmlFor={`batch-capability-${capability}`}>{capabilityLabel(capability, language)}</FieldLabel>
                    </Field>
                  ))}
                </FieldGroup>
                {permissionDetails.length > 0 ? (
                  <FieldDescription>
                    {permissionDetails.map((detail) => <span className="block break-all font-mono text-xs" key={detail}>{detail}</span>)}
                  </FieldDescription>
                ) : null}
              </FieldSet>
            ) : null}

            <FieldSet>
              <FieldLegend>{copy(language, "选择更新主机", "Select hosts to update")}</FieldLegend>
              <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
                <span className="tabular-nums">{copy(language, `已选择 ${chosen.length}/${eligible.length} 台主机`, `${chosen.length} of ${eligible.length} host(s) selected`)}</span>
                {eligible.length > 1 ? (
                  <Button disabled={busy} onClick={toggleAll} size="xs" type="button" variant="ghost">
                    {allSelected ? copy(language, "取消全选", "Clear all") : copy(language, "全选", "Select all")}
                  </Button>
                ) : null}
              </div>

              {showFilterControls ? (
                <div className="flex flex-col gap-2">
                  <InputGroup>
                    <InputGroupAddon><SearchIcon aria-hidden="true" /></InputGroupAddon>
                    <InputGroupInput
                      aria-label={copy(language, "筛选待更新主机", "Filter updatable hosts")}
                      disabled={busy}
                      onChange={(event) => setSearchQuery(event.target.value)}
                      placeholder={copy(language, "按主机、站点或套件筛选…", "Filter by host, site, or app…")}
                      type="search"
                      value={searchQuery}
                    />
                  </InputGroup>
                  {siteChips.length > 1 ? (
                    <div aria-label={copy(language, "按站点快捷勾选", "Quick-select by site")} className="flex flex-wrap items-center gap-1.5" role="group">
                      {siteChips.map((chip) => {
                        const selectedCount = chip.ids.filter((id) => selectedIds.includes(id)).length;
                        const active = selectedCount === chip.ids.length;
                        return (
                          <button
                            aria-pressed={active}
                            className={`store-filter-chip ${active ? "store-filter-chip-active" : ""}`}
                            disabled={busy}
                            key={chip.siteId}
                            onClick={() => toggleSubset(chip.ids)}
                            type="button"
                          >
                            {chip.regionCode ? <RegionFlag code={chip.regionCode} language={language} /> : null}
                            <span>{chip.siteName}</span>
                            <span className="tabular-nums opacity-75">{selectedCount}/{chip.ids.length}</span>
                          </button>
                        );
                      })}
                    </div>
                  ) : null}
                </div>
              ) : null}

              {eligible.length > 0 ? (
                visibleEligible.length > 0 ? (
                  <div className="max-h-[min(26rem,46svh)] overflow-y-auto rounded-lg border">
                    {showGroupHeaders ? (
                      <div className="divide-y">
                        {groupedEligible.map((group) => {
                          const groupIds = group.items.map((item) => item.instance.application.id);
                          const selectedCount = groupIds.filter((id) => selectedIds.includes(id)).length;
                          const allGroupSelected = selectedCount === groupIds.length;
                          return (
                            <div key={group.suiteId}>
                              <div className="flex items-center justify-between gap-2 bg-muted/45 px-3 py-2 text-xs">
                                <div className="flex min-w-0 items-center gap-2">
                                  <AppIcon appKey={group.suiteId} className="size-4 shrink-0" />
                                  <span className="truncate font-medium text-foreground">{group.name}</span>
                                  <span className="shrink-0 text-muted-foreground tabular-nums">→ {[...group.versions].join(" / ")}</span>
                                </div>
                                <div className="flex shrink-0 items-center gap-2">
                                  <span className="text-muted-foreground tabular-nums">{selectedCount}/{groupIds.length}</span>
                                  {groupIds.length > 1 ? (
                                    <Button
                                      disabled={busy}
                                      onClick={() => toggleSubset(groupIds)}
                                      size="xs"
                                      type="button"
                                      variant="ghost"
                                    >
                                      {allGroupSelected ? copy(language, "取消", "Clear") : copy(language, "全选", "All")}
                                    </Button>
                                  ) : null}
                                </div>
                              </div>
                              <div className="divide-y">
                                {group.items.map(renderTargetRow)}
                              </div>
                            </div>
                          );
                        })}
                      </div>
                    ) : (
                      <div className="divide-y">
                        {visibleEligible.map(renderTargetRow)}
                      </div>
                    )}
                  </div>
                ) : (
                  <p className="rounded-lg border border-dashed py-5 text-center text-xs text-muted-foreground">
                    {copy(language, "没有匹配的待更新主机。", "No updatable hosts match your filter.")}
                  </p>
                )
              ) : (
                <p className="text-sm text-muted-foreground">{copy(language, "当前没有可直接更新的在线主机。", "No online hosts are currently eligible to update.")}</p>
              )}
            </FieldSet>

            {skipped.length > 0 ? (
              <details className="rounded-lg border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
                <summary className="cursor-pointer font-medium text-foreground">
                  {copy(language, `已自动跳过 ${skipped.length} 台不可更新主机`, `${skipped.length} ineligible host(s) skipped automatically`)}
                </summary>
                <ul className="mt-2 space-y-1">
                  {skipped.map(({ instance, label, reason }) => (
                    <li className="flex items-center justify-between gap-2" key={instance.application.id}>
                      <span className="truncate font-medium text-foreground">{label}</span>
                      <span className="shrink-0">{reason}</span>
                    </li>
                  ))}
                </ul>
              </details>
            ) : null}

            {progress ? (
              <Progress aria-valuetext={progressText} value={Math.round((100 * progress.done) / Math.max(1, progress.total))}>
                <ProgressLabel>{copy(language, "提交更新任务", "Submitting update tasks")}</ProgressLabel>
                <ProgressValue>{() => progressText}</ProgressValue>
              </Progress>
            ) : null}

            {result && result.failed.length > 0 ? (
              <Alert role="alert" variant="destructive">
                <AlertTitle>{copy(language, `已提交 ${result.queued} 台，${result.failed.length} 台提交失败`, `Queued ${result.queued}; ${result.failed.length} failed`)}</AlertTitle>
                <AlertDescription>
                  <ul className="mt-1 space-y-1 text-xs">
                    {result.failed.map((item) => (
                      <li key={item.applicationId}><strong>{item.name}</strong>：{item.error}</li>
                    ))}
                  </ul>
                </AlertDescription>
              </Alert>
            ) : null}

            {error ? <FieldError role="alert">{error}</FieldError> : null}
          </SheetBody>
          <SheetFooter>
            <Button disabled={busy} onClick={onClose} type="button" variant="outline">
              {result ? copy(language, "关闭", "Close") : copy(language, "取消", "Cancel")}
            </Button>
            <Button disabled={busy || chosen.length === 0 || !permissionsApproved || catalogBlocked || catalogChanged} type="submit">
              {busy ? <Spinner data-icon="inline-start" /> : <CircleArrowUpIcon data-icon="inline-start" />}
              {copy(language, `开始更新 (${chosen.length})`, `Start update (${chosen.length})`)}
            </Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  );
}
