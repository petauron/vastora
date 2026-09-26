import { useState } from "react";
import { EllipsisIcon, SearchIcon } from "lucide-react";
import type { AppWorkspaceProps } from "@/app-workspaces/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { IPQualityButton } from "@/views/IPQuality";
import { LandingNotice, useLanding } from "@/views/LandingControls";
import { LandingTableRows } from "./LandingNodes";
import { NodeLocation } from "./NodeLocation";
import { RegionFlag } from "@/views/RegionFlag";
import { ApplicationStatus, ApplicationUpdate, ApplicationPrimaryStatus, AccessStatus } from "@/views/apps/InstalledApplicationPrimitives";
import { api } from "@/api";
import type { InstalledAppInstance } from "@/views/installed-apps-model";
import { publicationNeedsAttention, showInstalledNode } from "@/views/installed-apps-model";
import { copy } from "@/views/shared";
import { MeridianNetworkMatrix } from "./NetworkMatrix";
import { manifest } from "./manifest";

export function MeridianWorkspace({ group, data, language, mutate, onManage, onUpgrade, onClients, showSite }: AppWorkspaceProps) {
  const landing = useLanding();
  const [query, setQuery] = useState("");
  const [page, setPage] = useState<string>(manifest.pages[0].id);
  const search = query.trim().toLocaleLowerCase();
  const entries = group.instances.filter(showInstalledNode);
  const instances = entries.filter((instance) => !search || [instance.agent?.name, instance.application.nodeId, instance.siteName, ...instance.realityServices.map((service) => service.displayName)].some((value) => value?.toLocaleLowerCase().includes(search)));
  const siteNames = Object.fromEntries(data.agents.map((agent) => [agent.id, data.sites.find((site) => site.id === agent.siteId)?.name ?? ""]));
  const controller = group.controller;
  const controllerWebIDs = new Set(controller?.services.filter((service) => service.protocol === "http" || service.protocol === "https").map((service) => service.id));
  const controllerAttention = controller?.publications.some((publication) => controllerWebIDs.has(publication.serviceId) && publicationNeedsAttention(publication));
  const accounts = manifest.pages.find((item) => item.surface === "manager")!;
  return <section aria-label="Meridian" className="apps-three-xui flex min-w-0 flex-col gap-3" data-app-workspace={manifest.appKey}>
    <header className="flex items-baseline gap-3"><h2 className="text-base font-medium">Meridian</h2><p className="text-xs text-muted-foreground">{copy(language, `${entries.length} 个线路机 · ${landing?.view?.servers.length ?? "—"} 个落地机`, `${entries.length} entry nodes · ${landing?.view?.servers.length ?? "—"} landing nodes`)}</p></header>
    <LandingNotice language={language} />
    <div className="apps-three-xui-toolbar flex flex-wrap items-center gap-3 py-2">
      <InputGroup className="w-full sm:w-48"><InputGroupInput type="search" value={query} onChange={(event) => setQuery(event.target.value)} aria-label={copy(language, "搜索节点", "Search nodes")} placeholder={copy(language, "搜索节点…", "Search nodes…")} /><InputGroupAddon><SearchIcon aria-hidden="true" /></InputGroupAddon></InputGroup>
      {controller ? <div className="ml-auto flex min-w-0 flex-wrap items-center gap-2">{controller.activeChange || controller.application.status !== "running" ? <ApplicationPrimaryStatus instance={controller} language={language} /> : null}{controllerAttention ? <span className="text-xs text-destructive">{copy(language, "入口待处理", "Access needs attention")}</span> : null}<div className="ml-auto flex items-center gap-2"><ApplicationUpdate instance={controller} language={language} onUpgrade={onUpgrade} /><Button disabled={controller.locked} size="sm" variant="secondary" onClick={() => onClients(controller.application)}>{accounts.title[language]}</Button><Button size="icon-sm" variant="ghost" aria-label={copy(language, "管理订阅主机", "Manage subscription controller")} onClick={() => onManage(controller.application)}><EllipsisIcon aria-hidden="true" /></Button></div></div> : null}
    </div>
    <Tabs value={page} onValueChange={(value) => { if (typeof value === "string") setPage(value); }}>
      <TabsList variant="line" aria-label={copy(language, "Meridian 视图", "Meridian views")}>{manifest.pages.filter((item) => item.surface === "tab").map((item) => <TabsTrigger key={item.id} value={item.id}>{item.title[language]}</TabsTrigger>)}</TabsList>
      <TabsContent value="nodes" className="overflow-hidden rounded-lg border"><Table aria-label={copy(language, "Meridian 节点", "Meridian nodes")} className="apps-instance-table block lg:table lg:table-fixed">
        <TableHeader className="hidden lg:table-header-group"><TableRow><TableHead className="w-[24%]">{copy(language, "节点", "Node")}</TableHead><TableHead className="w-[48%]">{copy(language, "质量与解锁", "Quality & availability")}</TableHead><TableHead className="w-[12%]">{copy(language, "状态", "Status")}</TableHead><TableHead className="w-[10%]">{copy(language, "连接", "Connections")}</TableHead><TableHead className="w-[6%]"><span className="sr-only">{copy(language, "操作", "Actions")}</span></TableHead></TableRow></TableHeader>
        <TableBody className="block lg:table-row-group"><TableRow className="block bg-muted/30 lg:table-row"><TableCell colSpan={5} className="block text-xs lg:table-cell">{copy(language, "线路机", "Entry nodes")} {instances.length}</TableCell></TableRow>
          {instances.map((instance) => {
            const name = instance.agent?.name ?? instance.application.nodeId;
            const service = instance.realityServices[0];
            const hy2Only = service?.protocols?.includes("hy2") && !service.protocols.includes("vless");
            return <TableRow key={instance.application.id} data-application-id={instance.application.id} className="grid grid-cols-2 gap-x-4 gap-y-3 py-4 lg:table-row lg:py-0">
              <TableCell className="col-span-2 min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3"><div className="flex items-center gap-2"><RegionFlag code={service?.regionCode} language={language} /><span className="font-medium">{name}</span></div><p className="mt-1 text-xs text-muted-foreground"><NodeLocation regionCode={service?.regionCode} siteName={showSite ? instance.siteName : undefined} language={language} />{service?.protocols?.length ? ` · ${service.protocols.map((protocol) => protocol.toUpperCase()).join(" / ")}` : ""}</p></TableCell>
              <TableCell className="col-span-2 min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3"><IPQualityButton nodeId={instance.application.nodeId} name={name} language={language} egressAddress={instance.agent?.publicEgress?.address} linkBandwidth /></TableCell>
              <TableCell className="min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3"><p className="mb-1.5 text-xs text-muted-foreground lg:hidden">{copy(language, "应用状态", "Application")}</p><ApplicationStatus instance={instance} language={language} onUpgrade={onUpgrade} /></TableCell>
              <TableCell className="min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3"><p className="mb-1.5 text-xs text-muted-foreground lg:hidden">{copy(language, "公网入口", "Public access")}</p>{hy2Only ? <Badge variant="outline">{copy(language, "HY2 已配置", "HY2 configured")}</Badge> : <AccessStatus services={instance.realityServices} publications={instance.realityPublications} language={language} threeXUI />}</TableCell>
              <TableCell className="col-span-2 min-w-0 p-0 lg:px-2 lg:py-3"><div className="flex justify-end gap-2"><VerifyEntry instance={instance} language={language} mutate={mutate} />{!service ? <Button disabled={instance.locked} variant="outline" size="sm" onClick={() => onManage(instance.application)}>{copy(language, "配置入口", "Configure entry")}</Button> : null}<Button aria-label={copy(language, `管理 ${name} 应用`, `Manage ${name} application`)} className="max-lg:min-h-11 max-lg:min-w-11" size="icon-sm" variant="ghost" onClick={() => onManage(instance.application)}><EllipsisIcon aria-hidden="true" /></Button></div></TableCell>
            </TableRow>;
          })}
          {!instances.length ? <TableRow><TableCell colSpan={5} className="py-8 text-center text-muted-foreground">{copy(language, "没有匹配的线路机", "No matching entry nodes")}</TableCell></TableRow> : null}
          <LandingTableRows language={language} search={search} siteNames={siteNames} data={data} mutate={mutate} />
        </TableBody>
      </Table></TabsContent>
      <TabsContent value="network"><MeridianNetworkMatrix instances={instances} language={language} /></TabsContent>
    </Tabs>
  </section>;
}

function VerifyEntry({ instance, language, mutate }: Pick<AppWorkspaceProps, "language" | "mutate"> & { instance: InstalledAppInstance }) {
  const [busy, setBusy] = useState(false);
  const publication = instance.realityPublications.find((value) => value.status !== "ready" && value.status !== "stopped");
  const protocols = instance.realityServices[0]?.protocols;
  if (!publication || protocols?.includes("hy2") && !protocols.includes("vless")) return null;
  const verify = async () => {
    if (busy || instance.locked) return;
    setBusy(true);
    try { await mutate(() => api.verifyPublication(publication.id), copy(language, "入口检查已完成。", "Access point checked.")); }
    catch { /* The platform mutation notice reports failures. */ }
    finally { setBusy(false); }
  };
  return <Button disabled={busy || instance.locked} size="sm" variant="outline" onClick={() => void verify()}>{copy(language, "检查", "Check")}</Button>;
}
