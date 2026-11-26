import { useState } from "react";
import { CircleArrowUpIcon } from "lucide-react";
import type { AppData, Application, AppView } from "@/types";
import type { Language } from "@/translations";
import { AppIcon } from "@/components/desktop/AppIcon";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { SelectControl } from "@/components/SelectControl";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { AppStoreTile } from "./AppStore";
import { AppIdentityBadge } from "./AppIdentity";
import { AppInstallationSummary } from "./apps/AppInstallationSummary";
import { planBatchUpgrade } from "./apps/BatchUpgradeSheet";
import { ApplicationStatus } from "./apps/InstalledApplicationPrimitives";
import { eligibleAppNodes, installBlocker, localized } from "./appAccess";
import type { InstalledAppGroup, InstalledAppInstance } from "./installed-apps-model";
import { catalogInstallBlocked, copy } from "./shared";

const controllerKey = "vastora-official/meridian";
const nodeCapabilities = [
  { key: "vastora-official/glivela-entry", zh: "接收客户端连接", en: "Accept client connections" },
  { key: "vastora-official/glivela-node", zh: "提供出口访问", en: "Provide outbound access" },
  { key: "vastora-official/glivela-dual", zh: "接收连接并提供出口", en: "Accept connections and provide outbound access" },
] as const;

export const glivelaAppKeys: ReadonlySet<string> = new Set([controllerKey, ...nodeCapabilities.map((item) => item.key)]);

export function isGlivelaCatalogApp(app: AppView) {
  return app.sourceId === "vastora-official" && app.key === `vastora-official/${app.app.id}` && glivelaAppKeys.has(app.key);
}

export function glivelaMatches(query: string, data: AppData, groups: InstalledAppGroup[], language: Language) {
  if (!query) return true;
  const catalog = data.apps.filter(isGlivelaCatalogApp);
  const installed = groups.filter((group) => glivelaAppKeys.has(group.appKey));
  return ["Glivela", "控制端", "节点端", "Controller", "Node",
    ...nodeCapabilities.flatMap((item) => [item.key, item.zh, item.en]),
    ...catalog.flatMap((app) => [localized(app, language, "name"), localized(app, language, "description")]),
    ...installed.flatMap((group) => group.instances.map((instance) => instance.application.name)),
  ].some((value) => value.toLocaleLowerCase().includes(query));
}

export function GlivelaStoreCard({ data, groups, language, onInstall, onOpen, onManage, onUpgrade, onBatchUpgrade, submittingUpgradeID }: {
  data: AppData;
  groups: InstalledAppGroup[];
  language: Language;
  onInstall: (app: AppView) => void;
  onOpen: (key: string) => void;
  onManage: (application: Application) => void;
  onUpgrade: (application: Application) => void;
  onBatchUpgrade?: (title: string, instances: InstalledAppInstance[]) => void;
  submittingUpgradeID?: string | null;
}) {
  const [open, setOpen] = useState(false);
  const [nodePackageKey, setNodePackageKey] = useState<string>(nodeCapabilities[0].key);
  const officialApp = data.apps.find((app) => app.key === controllerKey && isGlivelaCatalogApp(app));
  const originalController = groups.find((group) => group.appKey === controllerKey);
  const legacy = originalController?.instances.filter((instance) => instance.application.legacyGlivela) ?? [];
  const controllerInstances = originalController?.instances.filter((instance) => !instance.application.legacyGlivela) ?? [];
  const controller = originalController && controllerInstances.length ? { ...originalController, instances: controllerInstances } : undefined;
  const nodeGroups = groups.filter((group) => group.appKey !== controllerKey && glivelaAppKeys.has(group.appKey));
  const nodeInstances = nodeGroups.flatMap((group) => group.instances);
  const nodeGroup = nodeGroups[0] ? { ...nodeGroups[0], instances: nodeInstances } : undefined;
  const nodeHosts = new Map<string, InstalledAppInstance[]>();
  for (const instance of nodeInstances) {
    const host = nodeHosts.get(instance.application.nodeId);
    if (host) host.push(instance);
    else nodeHosts.set(instance.application.nodeId, [instance]);
  }
  const hosts = [...nodeHosts.values()].sort((left, right) =>
    (left[0].agent?.name ?? left[0].application.nodeId).localeCompare(right[0].agent?.name ?? right[0].application.nodeId));
  const openController = () => { setOpen(false); onOpen(controllerKey); };
  const parts = [
    { id: "controller", zh: "控制端", en: "Controller", group: controller, appKey: controllerKey },
    { id: "node", zh: "节点端", en: "Node", group: nodeGroup, appKey: nodePackageKey },
  ] as const;
  const instanceRow = (instance: InstalledAppInstance, label: string, showHost: boolean, multiple: boolean) => {
    const capability = nodeCapabilities.find((item) => item.key === instance.application.appKey);
    const capabilityLabel = capability ? copy(language, capability.zh, capability.en) : "";
    const name = instance.agent?.name ?? instance.application.nodeId;
    return <div className="flex flex-wrap items-center gap-3 p-4" key={instance.application.id}>
      <div className="min-w-0 flex-1">
        {showHost ? <p className="break-words text-sm font-medium">{name}</p> : null}
        {multiple ? <p className="text-sm">{capabilityLabel}</p> : null}
        <p className="mt-1 text-xs text-muted-foreground">v{instance.application.installedVersion}{showHost ? ` · ${instance.siteName}` : ""}</p>
      </div>
      <ApplicationStatus instance={instance} language={language} submittingUpgradeID={submittingUpgradeID} onUpgrade={(application) => { setOpen(false); onUpgrade(application); }} />
      <Button aria-label={copy(language, `管理${label}：${name}${multiple ? ` · ${capabilityLabel}` : ""}`, `Manage ${label}: ${name}${multiple ? ` · ${capabilityLabel}` : ""}`)} onClick={() => { setOpen(false); onManage(instance.application); }} variant="outline" size="sm">{copy(language, "管理", "Manage")}</Button>
    </div>;
  };

  return <>
    <AppStoreTile appKey={controllerKey} name="Glivela" language={language} onDetails={() => setOpen(true)}
      meta={<>{parts.map((part) => <span className="block" key={part.id}>
        {copy(language, part.zh, part.en)} · {part.group ? <AppInstallationSummary group={part.group} language={language} compact byNode /> : copy(language, "尚未安装", "Not installed")}
      </span>)}</>}
      action={<div className="flex flex-wrap gap-2">
        <Button aria-label={copy(language, "管理 Glivela", "Manage Glivela")} onClick={() => setOpen(true)} size="sm" variant={controller ? "outline" : "default"}>{copy(language, "管理", "Manage")}</Button>
        {controller ? <Button aria-label={copy(language, "打开 Glivela", "Open Glivela")} onClick={openController} size="sm">{copy(language, "打开", "Open")}</Button> : null}
      </div>} />
    <Sheet open={open} onOpenChange={setOpen}><SheetContent className="sm:max-w-xl">
      <SheetHeader>
        <SheetTitle className="flex items-center gap-3"><AppIcon appKey={controllerKey} className="size-11" />Glivela{officialApp ? <AppIdentityBadge app={officialApp} language={language} /> : null}</SheetTitle>
        <SheetDescription>{copy(language, "管理控制端和节点端。节点功能在各主机配置中管理。", "Manage controllers and nodes. Configure node capabilities on each host.")}</SheetDescription>
      </SheetHeader>
      <SheetBody className="gap-5">
        {parts.map((part) => {
          const label = copy(language, part.zh, part.en);
          const group = part.group;
          const partBatch = part.id === "node" && group && onBatchUpgrade && group.instances.length > 1
            ? planBatchUpgrade(`Glivela ${label}`, group.instances, data, language)
            : null;
          const app = data.apps.find((value) => value.key === part.appKey && isGlivelaCatalogApp(value));
          const canInstall = Boolean(app && !catalogInstallBlocked(app) && eligibleAppNodes(data, app.key).length);
          const blocker = app && !canInstall
            ? catalogInstallBlocked(app)
              ? app.installBlockedReason || copy(language, "请先在控制面板刷新应用目录。", "Refresh the app catalog in Control Panel first.")
              : installBlocker(data, app.key, language)
            : "";
          return <section aria-labelledby={`glivela-part-${part.id}`} className="flex flex-col gap-3" key={part.id}>
            <div className="flex flex-wrap items-start justify-between gap-2">
              <h3 className="font-medium" id={`glivela-part-${part.id}`}>{label}</h3>
              {part.id === "controller" && controller ? <Button onClick={openController} size="sm" variant="outline">{copy(language, "打开 Glivela", "Open Glivela")}</Button> : null}
            </div>
            {group ? <>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <AppInstallationSummary group={group} language={language} byNode />
                {partBatch && partBatch.eligible.length > 0 ? <Button onClick={() => { setOpen(false); onBatchUpgrade?.(`Glivela ${label}`, group.instances); }} size="sm"><CircleArrowUpIcon aria-hidden="true" data-icon="inline-start" />{copy(language, `全部更新 (${partBatch.eligible.length})`, `Update all (${partBatch.eligible.length})`)}</Button> : null}
              </div>
              <div className="divide-y rounded-xl border">
                {part.id === "controller"
                  ? group.instances.map((instance) => instanceRow(instance, label, true, false))
                  : hosts.map((instances) => <div key={instances[0].application.nodeId}>
                    <div className="px-4 pt-4"><p className="break-words text-sm font-medium">{instances[0].agent?.name ?? instances[0].application.nodeId}</p><p className="mt-1 text-xs text-muted-foreground">{instances[0].siteName}</p></div>
                    {instances.map((instance) => instanceRow(instance, label, false, instances.length > 1))}
                  </div>)}
              </div>
            </> : <p className="text-sm text-muted-foreground">{copy(language, "尚未安装", "Not installed")}</p>}
            {part.id === "node" ? <Field>
              <FieldLabel htmlFor="glivela-node-capabilities">{copy(language, "新节点功能", "New node capabilities")}</FieldLabel>
              <SelectControl id="glivela-node-capabilities" value={nodePackageKey} onValueChange={setNodePackageKey} options={nodeCapabilities.map((item) => ({ value: item.key, label: copy(language, item.zh, item.en) }))} />
              <FieldDescription>{copy(language, "仅用于下一次安装，已安装节点的配置保持不变。", "Applies to the next installation only. Existing node settings stay unchanged.")}</FieldDescription>
            </Field> : null}
            {app ? <div className="flex flex-col gap-2">
              <p className="text-xs text-muted-foreground">{copy(language, "目录版本", "Catalog version")} v{app.app.version} · r{app.app.packageRevision ?? 0}</p>
              <Button aria-describedby={blocker ? `glivela-install-${part.id}-blocker` : undefined} aria-label={copy(language, `安装${label}`, `Install ${label}`)} disabled={!canInstall} onClick={() => { setOpen(false); onInstall(app); }} size="sm" variant="outline">{group ? copy(language, "安装到其他主机", "Install on another host") : copy(language, "安装", "Install")}</Button>
              {blocker ? <p className="text-xs text-muted-foreground" id={`glivela-install-${part.id}-blocker`}>{blocker}</p> : null}
            </div> : <p className="text-xs text-muted-foreground">{copy(language, "目录暂未提供所选功能", "The selected capability is unavailable in the catalog")}</p>}
            {part.id === "node" && legacy.length ? <div className="flex flex-col gap-2 rounded-xl border p-4">
              <p className="text-sm font-medium">{copy(language, "旧安装待核对", "Legacy installations need review")}</p>
              <p className="text-xs text-muted-foreground">{copy(language, "保留原安装记录；核对接管和停止状态后再处理。", "Existing records are preserved. Verify handover and stopped execution before proceeding.")}</p>
              {legacy.map((instance) => <p className="text-sm" key={instance.application.id}>{instance.agent?.name ?? instance.application.nodeId} · v{instance.application.installedVersion}</p>)}
            </div> : null}
          </section>;
        })}
      </SheetBody>
    </SheetContent></Sheet>
  </>;
}
