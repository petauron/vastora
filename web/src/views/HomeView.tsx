import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { AppWindowIcon, ArrowRightIcon, CircleCheckIcon, NetworkIcon, PencilIcon, ServerIcon } from "lucide-react";
import { api } from "../api";
import type { DashboardData, Mutate, Screen } from "../App";
import type { AgentView, Site } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, copy, formatDate } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldSet, FieldLegend } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

export function HomeView({ data, language, onNavigate, mutate }: { data: DashboardData; language: Language; onNavigate: (screen: Screen) => void; mutate: Mutate }) {
  const [editingSite, setEditingSite] = useState<Site | null>(null);
  const connected = data.agents.filter((agent) => agent.connected).length;
  const readyEntries = data.publications.filter((publication) => publication.status === "ready").length;
  const needsNetwork = data.agents.filter((agent) => !agent.networkProfile).length;
  return (
    <section className="flex flex-col gap-7">
      <PageHeading title={copy(language, "欢迎回来", "Welcome back")} description={copy(language, "从这里查看节点、应用和访问入口是否正常。", "See whether your nodes, apps, and access points are healthy.")} />
      {needsNetwork > 0 ? <Alert><NetworkIcon /><AlertTitle>{copy(language, `${needsNetwork} 个节点还没有确认网络`, `${needsNetwork} node(s) need network confirmation`)}</AlertTitle><AlertDescription>{copy(language, "确认建议地址后，才能安装应用和发布服务。", "Confirm the suggested addresses before installing and publishing services.")}<div className="mt-3"><Button onClick={() => onNavigate("network")} size="sm"><NetworkIcon data-icon="inline-start" />{copy(language, "配置网络", "Configure network")}</Button></div></AlertDescription></Alert> : null}
      <div className="grid gap-4 sm:grid-cols-3">
        <SummaryCard description={copy(language, "在线节点", "online nodes")} icon={<ServerIcon />} title={`${connected}/${data.agents.length}`} />
        <SummaryCard description={copy(language, "运行中的应用", "running apps")} icon={<AppWindowIcon />} title={String(data.applications.filter((application) => application.status === "running").length)} />
        <SummaryCard description={copy(language, "可访问入口", "ready access points")} icon={<CircleCheckIcon />} title={String(readyEntries)} />
      </div>

      <div className="flex flex-col gap-4">
        <div className="flex items-center justify-between gap-3"><h2 className="text-lg font-semibold">{copy(language, "位置", "Locations")}</h2><p className="text-sm text-muted-foreground">{copy(language, "位置把同一网络中的节点放在一起。", "A location groups nodes that share a network.")}</p></div>
        <div className="grid gap-4 lg:grid-cols-2">
          {data.sites.map((site) => {
            const agents = data.agents.filter((agent) => agent.siteId === site.id);
            return <Card key={site.id}><CardHeader><CardTitle>{site.name}</CardTitle><CardDescription>{site.description || copy(language, "一个网络位置", "A network location")}</CardDescription><CardAction><Button aria-label={copy(language, "编辑位置", "Edit location")} onClick={() => setEditingSite(site)} size="icon-sm" variant="ghost"><PencilIcon /></Button></CardAction></CardHeader><CardContent><dl className="grid grid-cols-2 gap-4 text-sm"><div><dt className="text-muted-foreground">{copy(language, "节点", "Nodes")}</dt><dd className="mt-1 font-medium">{agents.length}</dd></div><div><dt className="text-muted-foreground">{copy(language, "网关", "Gateways")}</dt><dd className="mt-1 font-medium">{site.gatewayNodes.length}</dd></div><div className="col-span-2"><dt className="text-muted-foreground">{copy(language, "默认域名", "Default domain")}</dt><dd className="mt-1 truncate font-mono text-xs">{site.domainSuffix || copy(language, "未设置", "Not set")}</dd></div></dl></CardContent><CardFooter className="justify-between"><StateBadge value={site.gatewayStatus} /><Button onClick={() => onNavigate("network")} size="sm" variant="ghost">{copy(language, "查看网络", "View network")}<ArrowRightIcon data-icon="inline-end" /></Button></CardFooter></Card>;
          })}
        </div>
      </div>

      <div className="flex flex-col gap-4">
        <div className="flex items-center justify-between"><h2 className="text-lg font-semibold">{copy(language, "最近活动", "Recent activity")}</h2><Button onClick={() => onNavigate("activity")} size="sm" variant="ghost">{copy(language, "查看全部", "View all")}<ArrowRightIcon data-icon="inline-end" /></Button></div>
        <Card size="sm"><CardContent className="flex flex-col gap-3">{data.actions.slice(0, 5).map((action) => <div className="flex min-h-11 items-center gap-3 border-b border-border/70 py-2 last:border-b-0" key={action.id}><StateBadge value={action.event} /><div className="min-w-0 flex-1"><p className="truncate text-sm font-medium">{action.kind}</p><p className="truncate text-xs text-muted-foreground">{action.message || action.taskId}</p></div><time className="hidden text-xs text-muted-foreground sm:block">{formatDate(language, action.createdAt)}</time></div>)}</CardContent></Card>
      </div>
      <SiteEditor agents={data.agents} language={language} open={Boolean(editingSite)} site={editingSite} onClose={() => setEditingSite(null)} onSave={async (site, input) => { await mutate(() => api.updateSite(site, input), copy(language, "位置信息已保存。", "Location saved.")); setEditingSite(null); }} />
    </section>
  );
}

function SummaryCard({ icon, title, description }: { icon: ReactNode; title: string; description: string }) {
  return <Card size="sm"><CardHeader><CardTitle className="flex items-center gap-2">{icon}{title}</CardTitle><CardDescription>{description}</CardDescription></CardHeader></Card>;
}

function SiteEditor({ agents, language, open, site, onClose, onSave }: { agents: AgentView[]; language: Language; open: boolean; site: Site | null; onClose: () => void; onSave: (site: Site, input: { name: string; code: string; description: string; domainSuffix: string; gatewayNodes: string[] }) => Promise<void> }) {
  const [name, setName] = useState("");
  const [code, setCode] = useState("");
  const [description, setDescription] = useState("");
  const [domainSuffix, setDomainSuffix] = useState("");
  const [gatewayNodes, setGatewayNodes] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!site) return;
    setName(site.name); setCode(site.code); setDescription(site.description); setDomainSuffix(site.domainSuffix); setGatewayNodes(site.gatewayNodes); setError("");
  }, [site]);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!site) return;
    setBusy(true); setError("");
    try { await onSave(site, { name, code, description, domainSuffix, gatewayNodes }); } catch (submitError) { setError(submitError instanceof Error ? submitError.message : "Request failed"); } finally { setBusy(false); }
  };
  const candidates = agents.filter((agent) => agent.siteId === site?.id && agent.capabilities.gateway);
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, "编辑位置", "Edit location")}</SheetTitle><SheetDescription>{copy(language, "名称帮助识别位置；默认域名用于建议服务地址。", "The name identifies this location. The domain is used for service suggestions.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="site-name">{copy(language, "名称", "Name")}</FieldLabel><Input aria-invalid={Boolean(error)} id="site-name" onChange={(event) => setName(event.target.value)} required value={name} /></Field><Field><FieldLabel htmlFor="site-code">{copy(language, "内部标识", "Internal code")}</FieldLabel><Input id="site-code" onChange={(event) => setCode(event.target.value.toLowerCase())} pattern="[a-z][a-z0-9-]{0,31}" required value={code} /></Field><Field><FieldLabel htmlFor="site-description">{copy(language, "说明", "Description")}</FieldLabel><Input id="site-description" onChange={(event) => setDescription(event.target.value)} value={description} /></Field><Field><FieldLabel htmlFor="site-domain">{copy(language, "默认域名", "Default domain")}</FieldLabel><Input id="site-domain" onChange={(event) => setDomainSuffix(event.target.value.toLowerCase())} placeholder="home.example.com" value={domainSuffix} /><FieldDescription>{copy(language, "可以留空，发布服务时再填写完整域名。", "Optional. A full hostname can be entered when publishing.")}</FieldDescription></Field><FieldSet><FieldLegend>{copy(language, "网关节点", "Gateway nodes")}</FieldLegend><FieldDescription>{copy(language, "网关负责把此位置内的 Web 服务变成容易访问的地址。", "Gateways provide friendly addresses for Web services in this location.")}</FieldDescription><FieldGroup>{candidates.map((agent) => <Field key={agent.id} orientation="horizontal"><FieldLabel htmlFor={`gateway-${agent.id}`}><span>{agent.name}</span><span className="text-xs font-normal text-muted-foreground">{agent.networkProfile?.serviceAddress || copy(language, "网络未确认", "Network not confirmed")}</span></FieldLabel><Switch checked={gatewayNodes.includes(agent.id)} id={`gateway-${agent.id}`} onCheckedChange={(checked) => setGatewayNodes((current) => checked ? [...new Set([...current, agent.id])] : current.filter((id) => id !== agent.id))} /></Field>)}</FieldGroup></FieldSet>{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "保存", "Save")}</Button></SheetFooter></form></SheetContent></Sheet>;
}
