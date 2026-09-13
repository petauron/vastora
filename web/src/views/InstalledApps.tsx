import { useId, useState } from "react";
import { LandingProvider, LandingManager, LandingNotice, LandingExitSelect, LandingLatency } from "./LandingControls";
import { AppWindowIcon, EllipsisIcon, ExternalLinkIcon, MonitorIcon, RadioTowerIcon, SearchIcon, ShieldAlertIcon } from "lucide-react";
import { api } from "../api";
import type { Mutate } from "../App";
import type { Application, Publication, Service } from "../types";
import type { Language } from "../translations";
import { Badge } from "@/components/ui/badge";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Spinner } from "@/components/ui/spinner";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";
import { localized, operationLabel } from "./appAccess";
import { catalogInstallBlocked, copy, StateBadge } from "./shared";
import { AppIdentityBadge } from "./AppIdentity";
import { canCreateRealityNode, publicationNeedsAttention, serviceNeedsAttention, showInstalledNode, threeXUIAppKey, type InstalledAppGroup, type InstalledAppInstance } from "./installed-apps-model";

type InstalledAppsProps = {
  groups: InstalledAppGroup[];
  language: Language;
  mutate: Mutate;
  onManage: (application: Application) => void;
  onUpgrade: (application: Application) => void;
  onClients: (application: Application) => void;
  onReality: (application: Application) => void;
};

export function InstalledApps({ groups, ...props }: InstalledAppsProps) {
  const [selectedID, setSelectedID] = useState(groups[0]?.id ?? "");
  const selected = groups.find((group) => group.id === selectedID) ?? groups[0];
  const showSite = new Set(groups.flatMap((group) => group.instances.map((instance) => instance.application.siteId))).size > 1;
  return <LandingProvider enabled={groups.some((group) => group.appKey === threeXUIAppKey)}>
    <Tabs value={selected?.id ?? ""} onValueChange={(value) => { if (typeof value === "string") setSelectedID(value); }} className="apps-chooser gap-4">
      <div className="max-w-full overflow-x-auto pb-1">
        <TabsList aria-label={copy(props.language, "已安装的应用", "Installed applications")}>
          {groups.map((group) => <TabsTrigger value={group.id} key={group.id}>
            <AppWindowIcon aria-hidden="true" />
            {group.app ? localized(group.app, props.language, "name") : group.instances[0].application.name}
            <span className="text-xs text-muted-foreground tabular-nums">{group.instances.length}</span>
          </TabsTrigger>)}
        </TabsList>
      </div>
      {groups.map((group) => <TabsContent value={group.id} key={group.id}>
        <InstalledApplicationGroup group={group} showSite={showSite} {...props} />
      </TabsContent>)}
    </Tabs>
  </LandingProvider>;
}

function InstalledApplicationGroup({ group, language, mutate, onManage, onUpgrade, onClients, onReality, showSite }: Omit<InstalledAppsProps, "groups"> & { group: InstalledAppGroup; showSite: boolean }) {
  const headingID = useId();
  const [query, setQuery] = useState("");
  const threeXUI = group.appKey === threeXUIAppKey;
  const nodeCount = group.instances.filter((instance) => instance.realityServices.length > 0).length;
  const attentionCount = group.instances.reduce((count, instance) => count + instance.publications.filter(publicationNeedsAttention).length, 0);
  const attentionInstance = group.instances.find((instance) => instance.publications.some(publicationNeedsAttention));
  const name = group.app ? localized(group.app, language, "name") : group.instances[0].application.name;
  const search = query.trim().toLocaleLowerCase();
  const instances = group.instances.filter(showInstalledNode).filter((instance) => !search || [instance.agent?.name, instance.application.nodeId, instance.siteName, ...instance.realityServices.map((service) => service.displayName)].some((value) => value?.toLocaleLowerCase().includes(search)));

  return <Card aria-labelledby={headingID} data-app-group={group.id} role="region">
    <CardHeader className="flex flex-row flex-wrap items-center gap-3">
      <div className="min-w-0 flex-1">
        <CardTitle className="flex flex-wrap items-center gap-2">
          <h2 className="min-w-0 break-words" id={headingID}>{name}</h2>{group.app ? <AppIdentityBadge app={group.app} language={language} /> : null}
        </CardTitle>
        <CardDescription>
          {threeXUI ? copy(language, `${nodeCount} 个 VLESS 节点`, `${nodeCount} VLESS node(s)`)
            : copy(language, `已安装到 ${group.instances.length} 个节点`, `Installed on ${group.instances.length} node(s)`)}
        </CardDescription>
      </div>
      {threeXUI ? <LandingManager language={language} /> : null}
      {attentionInstance ? <Button onClick={() => onManage(attentionInstance.application)} size="sm" variant="ghost">
        <ShieldAlertIcon aria-hidden="true" data-icon="inline-start" />
        <span className="text-destructive">{copy(language, `${attentionCount} 个入口待处理`, `${attentionCount} access point(s) need attention`)}</span>
      </Button> : null}
    </CardHeader>
    <CardContent className="flex min-w-0 flex-col gap-4">
      {threeXUI && group.legacyControllers.length > 0 ? <ControllerConvergence group={group} language={language} onManage={onManage} /> : null}
      {group.controller ? <ControllerBand instance={group.controller} language={language} onClients={onClients} onManage={onManage} onUpgrade={onUpgrade} /> : null}
      {threeXUI ? <LandingNotice language={language} /> : null}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <InputGroup className="max-w-xs">
          <InputGroupInput type="search" value={query} onChange={(event) => setQuery(event.target.value)} aria-label={copy(language, "搜索节点", "Search nodes")} placeholder={copy(language, "搜索节点…", "Search nodes…")} />
          <InputGroupAddon><SearchIcon aria-hidden="true" /></InputGroupAddon>
        </InputGroup>
        <p role="status" className="text-xs text-muted-foreground">{copy(language, `${instances.length} 个节点`, `${instances.length} node(s)`)}</p>
      </div>
      <Table aria-label={threeXUI ? copy(language, `${name} VLESS 节点`, `${name} VLESS nodes`) : copy(language, `${name} 已安装实例`, `${name} installed instances`)} className="apps-instance-table block lg:table lg:table-fixed">
        <TableHeader className="hidden lg:table-header-group">
          <TableRow>
            <TableHead className={threeXUI ? "w-[23%]" : "w-[36%]"}>{copy(language, "节点", "Node")}</TableHead>
            <TableHead className={threeXUI ? "w-[15%]" : "w-[24%]"}>{copy(language, "状态", "Status")}</TableHead>
            {threeXUI ? <><TableHead className="w-[24%]">{copy(language, "出口", "Exit")}</TableHead><TableHead className="w-[14%]">{copy(language, "落地延迟", "Exit latency")}</TableHead></> : null}
            <TableHead className={threeXUI ? "w-[12%]" : "w-[24%]"}>{copy(language, "入口", "Access")}</TableHead>
            <TableHead className={threeXUI ? "w-[12%]" : "w-[16%]"}><span className="sr-only">{copy(language, "操作", "Actions")}</span></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody className="block lg:table-row-group">
          {instances.map((instance) => <InstalledInstanceRow instance={instance} key={instance.application.id} language={language} mutate={mutate} onManage={onManage} onUpgrade={onUpgrade} onReality={onReality} showSite={showSite} threeXUI={threeXUI} />)}
          {!instances.length ? <TableRow className="block lg:table-row"><TableCell colSpan={threeXUI ? 6 : 4} className="block py-8 text-center text-muted-foreground lg:table-cell">{search ? copy(language, "没有匹配的节点", "No matching nodes") : copy(language, "尚未配置 VLESS 节点", "No VLESS nodes configured")}</TableCell></TableRow> : null}
        </TableBody>
      </Table>
      {threeXUI ? <p className="text-xs text-muted-foreground">{copy(language, "切换出口时，当前连接会短暂中断。", "Switching exits briefly interrupts current connections.")}</p> : null}
    </CardContent>
  </Card>;
}

function ControllerConvergence({ group, language, onManage }: { group: InstalledAppGroup; language: Language; onManage: (application: Application) => void }) {
  const migration = group.convergence;
  const failed = migration?.state === "failed" || Boolean(migration?.lastError);
  const source = migration
    ? group.legacyControllers.find((instance) => instance.application.id === migration.sourceApplicationId)
    : group.legacyControllers[0];
  return <Alert aria-live="polite" variant={failed ? "destructive" : "default"}>
    {failed ? <ShieldAlertIcon /> : <Spinner />}
    <AlertTitle>{failed
      ? copy(language, "旧订阅主机转换已暂停", "Legacy controller conversion paused")
      : copy(language, `正在合并 ${group.legacyControllers.length} 个旧订阅主机`, `Consolidating ${group.legacyControllers.length} legacy controller(s)`)}</AlertTitle>
    <AlertDescription>
      <p>{failed
        ? migration?.lastError || copy(language, "请检查对应节点后重试。", "Check the affected node, then retry.")
        : copy(language, "系统会逐台保存恢复点、转成 VLESS 节点并接入上方的全局订阅主机。", "Each host is backed up, converted to a VLESS node, and attached to the global subscription controller above in sequence.")}</p>
      {failed && source ? <Button className="mt-3" onClick={() => onManage(source.application)} size="sm" variant="outline">{copy(language, "查看并重试", "Review and retry")}</Button> : null}
    </AlertDescription>
  </Alert>;
}

function ControllerBand({ instance, language, onClients, onManage, onUpgrade }: { instance: InstalledAppInstance; language: Language; onClients: (application: Application) => void; onManage: (application: Application) => void; onUpgrade: (application: Application) => void }) {
  const { application, agent, services, publications, locked } = instance;
  const webServiceIDs = new Set(services.filter((service) => service.protocol === "http" || service.protocol === "https").map((service) => service.id));
  const webAttention = publications.some((publication) => webServiceIDs.has(publication.serviceId) && publicationNeedsAttention(publication));
  const panelService = services.find((service) => service.name === "panel");
  const panelPublication = panelService ? publications.find((publication) => publication.serviceId === panelService.id && publication.status === "ready" && !publication.actionRequired && !publication.lastError && publication.accessUrl) : undefined;

  return <section aria-label={copy(language, "订阅主机", "Subscription controller")} className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-muted/30 px-3 py-2.5" data-slot="subscription-controller">
    <MonitorIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
    <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1">
      <span className="text-xs text-muted-foreground">{copy(language, "订阅主机", "Subscription controller")}</span>
      <span className="truncate text-sm font-medium" title={agent?.name ?? application.nodeId}>{agent?.name ?? application.nodeId}</span>
      {instance.activeChange || application.status !== "running" ? <ApplicationPrimaryStatus instance={instance} language={language} /> : null}
      {webAttention ? <span className="text-xs text-destructive">{copy(language, "入口待处理", "Access needs attention")}</span> : null}
    </div>
    <div className="flex flex-wrap items-center gap-2">
      <ApplicationUpdate instance={instance} language={language} onUpgrade={onUpgrade} />
      <Button disabled={locked} onClick={() => onClients(application)} size="sm" variant="outline">{copy(language, "客户端与订阅", "Clients & subscriptions")}</Button>
      {panelPublication?.accessUrl ? <a className={buttonVariants({ size: "sm", variant: "outline" })} href={panelPublication.accessUrl} rel="noreferrer" target="_blank">
        {copy(language, "打开面板", "Open panel")}<ExternalLinkIcon aria-hidden="true" data-icon="inline-end" />
      </a> : null}
      <Button aria-label={copy(language, `管理 ${agent?.name ?? application.nodeId} 订阅主机`, `Manage ${agent?.name ?? application.nodeId} subscription controller`)} onClick={() => onManage(application)} size="icon-sm" variant="ghost"><EllipsisIcon aria-hidden="true" /></Button>
    </div>
  </section>;
}

function ApplicationUpdate({ instance, language, onUpgrade }: { instance: InstalledAppInstance; language: Language; onUpgrade: (application: Application) => void }) {
  const { application, app, agent, activeChange } = instance;
  if (!application.updateAvailable || activeChange) return null;
  const legacy = application.appKey === threeXUIAppKey && application.role === "master" && Boolean(application.controllerApplicationId) && application.id !== application.controllerApplicationId;
  const disabled = !agent?.connected || legacy || catalogInstallBlocked(app) || ["pending", "deploying"].includes(application.status);
  const name = agent?.name ?? application.nodeId;
  return <Button aria-label={copy(language, `更新 ${name} 的应用`, `Update application on ${name}`)} className="max-md:min-h-11" disabled={disabled} onClick={() => onUpgrade(application)} size="sm" variant="outline" title={copy(language, `更新到 v${application.availableVersion}`, `Update to v${application.availableVersion}`)}>
    {application.status === "failed" ? copy(language, "重试更新", "Retry update") : copy(language, "更新", "Update")}
  </Button>;
}

function ApplicationPrimaryStatus({ instance, language }: { instance: InstalledAppInstance; language: Language }) {
  const { application, activeChange } = instance;
  if (activeChange) return <Badge role="status" variant={activeChange.reconciliationRequired ? "destructive" : "outline"}>
    {activeChange.reconciliationRequired ? <ShieldAlertIcon aria-hidden="true" data-icon="inline-start" /> : <Spinner aria-hidden="true" data-icon="inline-start" className="motion-reduce:animate-none" />}
    {activeChange.reconciliationRequired ? copy(language, "需要恢复", "Recovery required")
      : activeChange.operation === "upgrade" ? copy(language, "正在更新", "Updating")
      : copy(language, `正在${operationLabel(language, activeChange.operation)}`, `${operationLabel(language, activeChange.operation)} in progress`)}
  </Badge>;
  return application.status === "running"
    ? <span className="inline-flex items-center gap-2"><span aria-hidden="true" className="apps-status-dot bg-latency-fast" />{copy(language, "运行中", "Running")}</span>
    : <StateBadge language={language} value={application.status} />;
}

function ApplicationStatus({ instance, language, onUpgrade }: { instance: InstalledAppInstance; language: Language; onUpgrade: (application: Application) => void }) {
  const { application, activeChange, agent } = instance;
  const syncing = application.role === "worker" && (application.nodeSyncStatus === "pending" || application.nodeSyncStatus === "applying");
  const syncFailed = application.role === "worker" && (!instance.controller || !["ready", "pending", "applying"].includes(application.nodeSyncStatus ?? ""));

  return <div className="flex flex-col items-start gap-1.5">
    <ApplicationPrimaryStatus instance={instance} language={language} />
    {!activeChange && syncing ? <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"><Spinner aria-hidden="true" />{copy(language, "正在接入订阅主机", "Connecting to controller")}</span> : null}
    {!activeChange && syncFailed ? <span className="text-xs text-destructive">{copy(language, "尚未接入订阅主机", "Controller not connected")}</span> : null}
    {agent && !agent.connected ? <span className="text-xs text-destructive">{copy(language, "节点离线", "Node offline")}</span> : null}
    <ApplicationUpdate instance={instance} language={language} onUpgrade={onUpgrade} />
  </div>;
}

function AccessStatus({ services, publications, language, threeXUI }: { services: Service[]; publications: Publication[]; language: Language; threeXUI: boolean }) {
  const attention = publications.some(publicationNeedsAttention) || services.some(serviceNeedsAttention);
  const changing = publications.find((publication) => publication.status === "pending" || publication.status === "applying");
  const hardening = services.some((service) => service.guardStatus === "pending" || service.guardStatus === "hardening");

  if (attention) return <div className="flex flex-col items-start gap-1">
    <span className="inline-flex items-center gap-1.5 text-sm text-destructive"><ShieldAlertIcon aria-hidden="true" className="size-4 shrink-0" />{copy(language, "入口待处理", "Access needs attention")}</span>
    
  </div>;
  if (hardening) return <StateBadge language={language} value="applying" />;
  if (changing) return <StateBadge language={language} value={changing.status} />;
  if (publications.length > 0) return <span className="inline-flex items-center gap-2 text-muted-foreground"><span aria-hidden="true" className="apps-status-dot bg-latency-fast" />{copy(language, "就绪", "Ready")}</span>;
  return <span className="text-xs text-muted-foreground">
    {threeXUI && services.length === 0 ? copy(language, "尚未创建 VLESS", "VLESS not configured") : services.length > 0 ? copy(language, "未添加入口", "No access point") : copy(language, "无需访问入口", "No access point needed")}
  </span>;
}

function InstalledInstanceRow({ instance, language, mutate, onManage, onUpgrade, onReality, threeXUI, showSite }: { instance: InstalledAppInstance; language: Language; mutate: Mutate; onManage: (application: Application) => void; onUpgrade: (application: Application) => void; onReality: (application: Application) => void; threeXUI: boolean; showSite: boolean }) {
  const [checking, setChecking] = useState(false);
  const { application, agent, locked } = instance;
  const services = threeXUI ? instance.realityServices : instance.services;
  const publications = threeXUI ? instance.realityPublications : instance.publications;
  const hy2Only = threeXUI && instance.realityServices[0]?.protocols?.includes("hy2") && !instance.realityServices[0]?.protocols?.includes("vless");
  const pendingPublication = publications.find((publication) => publication.status !== "ready" && publication.status !== "stopped");
  const name = agent?.name ?? application.nodeId;
  const displayName = instance.realityServices[0]?.displayName;
  const needsVLESS = threeXUI && instance.realityServices.length === 0;
  const check = async () => {
    if (!pendingPublication || locked || checking) return;
    setChecking(true);
    try {
      await mutate(() => api.verifyPublication(pendingPublication.id), copy(language, "入口检查已完成。", "Access point checked."));
    } catch { /* The shared notice reports mutation failures. */ } finally {
      setChecking(false);
    }
  };

  return <TableRow className="grid grid-cols-2 gap-x-4 gap-y-3 py-4 lg:table-row lg:py-0" data-application-id={application.id}>
    <TableCell className="col-span-2 min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3">
      <p className="break-words font-medium">{name}</p>
      {threeXUI && instance.realityServices[0] ? <div className="mt-1 flex flex-wrap gap-1">{(instance.realityServices[0].protocols ?? ["vless"]).map((protocol) => <Badge key={protocol} variant="outline">{protocol.toUpperCase()}</Badge>)}</div> : null}
      {threeXUI && application.role === "master" && application.id !== instance.controller?.id ? <Badge className="mt-1" variant="outline">{copy(language, "待转为节点", "Converting to node")}</Badge> : null}
      {showSite || displayName ? <p className="mt-1 truncate text-xs text-muted-foreground" title={displayName ?? instance.siteName}>{showSite ? instance.siteName : displayName}</p> : null}
    </TableCell>
    <TableCell className="min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3">
      <p className="mb-1.5 text-xs text-muted-foreground lg:hidden">{copy(language, "应用状态", "Application")}</p>
      <ApplicationStatus instance={instance} language={language} onUpgrade={onUpgrade} />
    </TableCell>
    {threeXUI ? <>
      <TableCell className="min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3">
        <p className="mb-1.5 text-xs text-muted-foreground lg:hidden">{copy(language, "出口", "Exit")}</p>
        {needsVLESS ? <span className="text-muted-foreground">—</span> : <LandingExitSelect applicationId={application.id} nodeId={application.nodeId} name={name} locked={locked} language={language} />}
      </TableCell>
      <TableCell className="min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3">
        <p className="mb-1.5 text-xs text-muted-foreground lg:hidden">{copy(language, "落地延迟", "Exit latency")}</p>
        {needsVLESS ? <span className="text-muted-foreground">—</span> : <LandingLatency applicationId={application.id} nodeId={application.nodeId} language={language} />}
      </TableCell>
    </> : null}
    <TableCell className="min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3">
      <p className="mb-1.5 text-xs text-muted-foreground lg:hidden">{threeXUI ? copy(language, "公网入口", "Public access") : copy(language, "访问入口", "Access")}</p>
      {hy2Only ? <Badge variant="outline">{copy(language, "HY2 已配置", "HY2 configured")}</Badge> : <AccessStatus language={language} publications={publications} services={services} threeXUI={threeXUI} />}
    </TableCell>
    <TableCell className="col-span-2 min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3">
      <div className="flex flex-wrap items-center justify-end gap-2">
        {!threeXUI && instance.deployment?.accessUrl ? <a aria-label={copy(language, `打开 ${name} 的应用主页`, `Open the app homepage on ${name}`)} className={cn(buttonVariants({ size: "icon-sm", variant: "outline" }), "max-md:min-h-11 max-md:min-w-11")} href={instance.deployment.accessUrl} rel="noreferrer" target="_blank">
          <ExternalLinkIcon aria-hidden="true" />
        </a> : null}
        {pendingPublication && !hy2Only ? <Button aria-label={copy(language, `检查 ${name} 的入口`, `Check ${name} access`)} className="max-md:min-h-11" disabled={locked || checking} onClick={() => void check()} size="sm" variant="outline">
          {checking ? <Spinner aria-hidden="true" data-icon="inline-start" /> : null}{copy(language, "检查", "Check")}
        </Button> : null}
        {needsVLESS ? <Button className="max-md:min-h-11" disabled={!canCreateRealityNode(instance)} onClick={() => onReality(application)} size="sm" variant="outline">
          <RadioTowerIcon aria-hidden="true" data-icon="inline-start" />{copy(language, "创建 VLESS", "Create VLESS")}
        </Button> : null}
        <Button aria-label={copy(language, `管理 ${name} 应用`, `Manage ${name} application`)} className="max-lg:min-h-11 max-lg:min-w-11" onClick={() => onManage(application)} size="icon-sm" variant="ghost">
          <EllipsisIcon aria-hidden="true" />
        </Button>
      </div>
    </TableCell>
  </TableRow>;
}
