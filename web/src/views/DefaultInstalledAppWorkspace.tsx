import { ApplicationUpdate, ApplicationPrimaryStatus, ApplicationStatus, AccessStatus } from "./apps/InstalledApplicationPrimitives";
import { useId, useState, type ReactNode } from "react";
import { EllipsisIcon, ExternalLinkIcon, MonitorIcon, RadioTowerIcon, SearchIcon, ShieldAlertIcon } from "lucide-react";
import { api } from "../api";
import type { Mutate } from "../App";
import type { Application } from "../types";
import type { Language } from "../translations";
import { Badge } from "@/components/ui/badge";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Spinner } from "@/components/ui/spinner";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";
import { localized } from "./appAccess";
import { copy } from "./shared";
import { AppIdentityBadge } from "./AppIdentity";
import { RegionFlag } from "./RegionFlag";
import { IPQualityButton } from "./IPQuality";
import { canCreateRealityNode, publicationNeedsAttention, showInstalledNode, threeXUIAppKey, type InstalledAppGroup, type InstalledAppInstance } from "./installed-apps-model";

import type { AppWorkspaceProps } from "../app-workspaces/types";

export function DefaultInstalledAppWorkspace({ group, language, mutate, onManage, onUpgrade, onClients, onReality, showSite }: AppWorkspaceProps) {
  const headingID = useId();
  const [query, setQuery] = useState("");
  const legacyThreeXUI = group.appKey === threeXUIAppKey;
  const threeXUI = legacyThreeXUI;
  const nodeCount = group.instances.filter(showInstalledNode).length;
  const attentionCount = group.instances.reduce((count, instance) => count + instance.publications.filter(publicationNeedsAttention).length, 0);
  const attentionInstance = group.instances.find((instance) => instance.publications.some(publicationNeedsAttention));
  const name = group.app ? localized(group.app, language, "name") : group.instances[0].application.name;
  const search = query.trim().toLocaleLowerCase();
  const instances = group.instances.filter(showInstalledNode).filter((instance) => !search || [instance.agent?.name, instance.application.nodeId, instance.siteName, ...instance.realityServices.map((service) => service.displayName)].some((value) => value?.toLocaleLowerCase().includes(search)));
  const Container = threeXUI ? "section" : Card;
  const Header = threeXUI ? "header" : CardHeader;
  const Content = threeXUI ? "div" : CardContent;

  return <Container aria-labelledby={headingID} data-app-group={group.id} role="region" className={threeXUI ? "apps-three-xui flex min-w-0 flex-col gap-3" : undefined}>
    <Header className="flex flex-row flex-wrap items-center gap-3">
      <div className="min-w-0 flex-1">
        {threeXUI ? <div className="flex items-center gap-2"><h2 className="text-base font-medium" id={headingID}>{name}</h2>{group.app ? <AppIdentityBadge app={group.app} language={language} /> : null}</div> : <CardTitle className="flex flex-wrap items-center gap-2">
          <h2 className="min-w-0 break-words" id={headingID}>{name}</h2>{group.app ? <AppIdentityBadge app={group.app} language={language} /> : null}
        </CardTitle>}
        {threeXUI ? <p className="mt-1 text-xs text-muted-foreground">{copy(language, `${nodeCount} 个线路机 · ${group.controller ? 1 : 0} 台订阅主机`, `${nodeCount} entry nodes · ${group.controller ? 1 : 0} subscription host`)}</p>
          : <CardDescription>{copy(language, `已安装到 ${group.instances.length} 个节点`, `Installed on ${group.instances.length} node(s)`)}</CardDescription>}
      </div>
      {attentionInstance ? <Button onClick={() => onManage(attentionInstance.application)} size="sm" variant="ghost">
        <ShieldAlertIcon aria-hidden="true" data-icon="inline-start" />
        <span className="text-destructive">{copy(language, `${attentionCount} 个入口待处理`, `${attentionCount} access point(s) need attention`)}</span>
      </Button> : null}
    </Header>
    <Content className={cn("flex min-w-0 flex-col", threeXUI ? "gap-3" : "gap-4")}>
      {legacyThreeXUI && group.legacyControllers.length > 0 ? <ControllerConvergence group={group} language={language} onManage={onManage} /> : null}
      <div className={cn("flex flex-wrap items-center gap-3", threeXUI ? "apps-three-xui-toolbar py-2" : "justify-between")}>
        <InputGroup className={threeXUI ? "w-full sm:w-48" : "max-w-xs"}>
          <InputGroupInput type="search" value={query} onChange={(event) => setQuery(event.target.value)} aria-label={copy(language, "搜索节点", "Search nodes")} placeholder={copy(language, "搜索节点…", "Search nodes…")} />
          <InputGroupAddon><SearchIcon aria-hidden="true" /></InputGroupAddon>
        </InputGroup>
        {!threeXUI ? <p role="status" className="text-xs text-muted-foreground">{copy(language, `${instances.length} 个节点`, `${instances.length} node(s)`)}</p> : null}
        {group.controller ? <ControllerBand instance={group.controller} language={language} onClients={onClients} onManage={onManage} onUpgrade={onUpgrade} /> : null}
      </div>
      <Table aria-label={threeXUI ? copy(language, `${name} 节点`, `${name} nodes`) : copy(language, `${name} 已安装实例`, `${name} installed instances`)} className="apps-instance-table block lg:table lg:table-fixed">
        <TableHeader className="hidden lg:table-header-group">
          <TableRow>
            <TableHead className={threeXUI ? "w-[24%]" : "w-[36%]"}>{copy(language, "节点", "Node")}</TableHead>
            {threeXUI ? <TableHead className="w-[48%]">{copy(language, "IP 质量与解锁", "IP quality & availability")}</TableHead> : null}
            <TableHead className={threeXUI ? "w-[12%]" : "w-[24%]"}>{copy(language, "状态", "Status")}</TableHead>
            <TableHead className={threeXUI ? "w-[10%]" : "w-[24%]"}>{copy(language, "入口", "Access")}</TableHead>
            <TableHead className={threeXUI ? "w-[6%]" : "w-[16%]"}><span className="sr-only">{copy(language, "操作", "Actions")}</span></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody className="block lg:table-row-group">
          {threeXUI ? <TableRow className="block bg-muted/30 hover:bg-muted/30 lg:table-row"><TableCell colSpan={5} className="block text-xs font-medium lg:table-cell">{copy(language, "线路机", "Entry nodes")} <span className="ml-1 text-muted-foreground">{instances.length}</span></TableCell></TableRow> : null}
          {instances.map((instance) => <InstalledInstanceRow instance={instance} key={instance.application.id} language={language} mutate={mutate} onManage={onManage} onUpgrade={onUpgrade} onReality={onReality} showSite={showSite} threeXUI={threeXUI} />)}
          {!instances.length ? <TableRow className="block lg:table-row"><TableCell colSpan={threeXUI ? 5 : 4} className="block py-8 text-center text-muted-foreground lg:table-cell">{search ? copy(language, "没有匹配的节点", "No matching nodes") : copy(language, "尚未配置 Xray 节点", "No Xray nodes configured")}</TableCell></TableRow> : null}
        </TableBody>
      </Table>
    </Content>
  </Container>;
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
        : copy(language, "系统会逐台保存恢复点、替换为 Xray 节点并接入上方的全局订阅主机。", "Each host is backed up, replaced with an Xray node, and attached to the global subscription controller above in sequence.")}</p>
      {failed && source ? <Button className="mt-3" onClick={() => onManage(source.application)} size="sm" variant="outline">{copy(language, "查看并重试", "Review and retry")}</Button> : null}
    </AlertDescription>
  </Alert>;
}

function ControllerBand({ instance, language, onClients, onManage, onUpgrade, children }: { instance: InstalledAppInstance; language: Language; onClients: (application: Application) => void; onManage: (application: Application) => void; onUpgrade: (application: Application) => void; children?: ReactNode }) {
  const { application, agent, services, publications, locked } = instance;
  const webServiceIDs = new Set(services.filter((service) => service.protocol === "http" || service.protocol === "https").map((service) => service.id));
  const webAttention = publications.some((publication) => webServiceIDs.has(publication.serviceId) && publicationNeedsAttention(publication));
  const panelService = services.find((service) => service.name === "panel");
  const panelPublication = panelService ? publications.find((publication) => publication.serviceId === panelService.id && publication.status === "ready" && !publication.actionRequired && !publication.lastError && publication.accessUrl) : undefined;

  return <section aria-label={copy(language, "订阅主机", "Subscription controller")} className="flex min-w-0 flex-1 basis-full flex-wrap items-center gap-3 xl:basis-auto" data-slot="subscription-controller">
    <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
      <MonitorIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
      <span className="text-xs text-muted-foreground">{copy(language, "订阅主机", "Subscription controller")}</span>
      <span className="truncate text-sm font-medium" title={agent?.name ?? application.nodeId}>{agent?.name ?? application.nodeId}</span>
      {instance.activeChange || application.status !== "running" ? <ApplicationPrimaryStatus instance={instance} language={language} /> : null}
      {webAttention ? <span className="text-xs text-destructive">{copy(language, "入口待处理", "Access needs attention")}</span> : null}
    </div>
    <div className="flex flex-wrap items-center gap-2">
      <ApplicationUpdate instance={instance} language={language} onUpgrade={onUpgrade} />
      <Button disabled={locked} onClick={() => onClients(application)} size="sm" variant="secondary">{copy(language, "客户端与订阅", "Clients & subscriptions")}</Button>
      {children}
      {panelPublication?.accessUrl ? <a className={buttonVariants({ size: "sm", variant: "ghost" })} href={panelPublication.accessUrl} rel="noreferrer" target="_blank">
        {copy(language, "打开面板", "Open panel")}<ExternalLinkIcon aria-hidden="true" data-icon="inline-end" />
      </a> : null}
      <Button aria-label={copy(language, `管理 ${agent?.name ?? application.nodeId} 订阅主机`, `Manage ${agent?.name ?? application.nodeId} subscription controller`)} onClick={() => onManage(application)} size="icon-sm" variant="ghost"><EllipsisIcon aria-hidden="true" /></Button>
    </div>
  </section>;
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
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        {threeXUI ? <RegionFlag code={instance.realityServices[0]?.regionCode} language={language} /> : null}
        <p className="min-w-0 break-words font-medium">{name}</p>
      </div>
      {threeXUI && application.role === "master" && application.id !== instance.controller?.id ? <Badge className="mt-1" variant="outline">{copy(language, "待转为节点", "Converting to node")}</Badge> : null}
      {showSite || displayName || threeXUI ? <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
        {showSite || displayName ? <p className="min-w-0 truncate text-xs text-muted-foreground" title={displayName ?? instance.siteName}>{showSite ? instance.siteName : displayName}</p> : null}
        {threeXUI && instance.realityServices[0] ? <span className="text-[11px] text-muted-foreground">{(instance.realityServices[0].protocols ?? ["vless"]).map((protocol) => protocol.toUpperCase()).join(" · ")}</span> : null}
      </div> : null}
    </TableCell>
    {threeXUI ? <TableCell className="col-span-2 min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3">
      {instance.realityServices.length > 0 ? <IPQualityButton nodeId={application.nodeId} name={name} language={language} /> : <span className="text-muted-foreground">—</span>}
    </TableCell> : null}
    <TableCell className="min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3">
      <p className="mb-1.5 text-xs text-muted-foreground lg:hidden">{copy(language, "应用状态", "Application")}</p>
      <ApplicationStatus instance={instance} language={language} onUpgrade={onUpgrade} />
    </TableCell>
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
        {needsVLESS ? <Button className="max-md:min-h-11" disabled={!canCreateRealityNode(instance)} onClick={() => onReality(application)} size="sm" variant="outline"><RadioTowerIcon aria-hidden="true" data-icon="inline-start" />{copy(language, "创建 VLESS", "Create VLESS")}</Button> : null}
        <Button aria-label={copy(language, `管理 ${name} 应用`, `Manage ${name} application`)} className="max-lg:min-h-11 max-lg:min-w-11" onClick={() => onManage(application)} size="icon-sm" variant="ghost">
          <EllipsisIcon aria-hidden="true" />
        </Button>
      </div>
    </TableCell>
  </TableRow>;
}
