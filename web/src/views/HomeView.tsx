import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { AppWindowIcon, ArrowRightIcon, CircleCheckIcon, PencilIcon, PlusIcon, ServerIcon } from "lucide-react";
import { api } from "../api";
import { browserTimezone } from "../lib/network";
import { actionKind, actionMessage, groupActions } from "./ActivityView";
import type { AppData, Mutate, Screen } from "../App";
import type { AgentView, Site, SiteInput } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, copy, formatDate, userError } from "./shared";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldSet, FieldLegend } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

export function HomeView({ data, language, onNavigate, mutate }: { data: AppData; language: Language; onNavigate: (screen: Screen) => void; mutate: Mutate }) {
  const [editingSite, setEditingSite] = useState<Site | null>(null);
  const [siteEditorOpen, setSiteEditorOpen] = useState(false);
  const activeAgents = data.agents.filter((agent) => agent.status === "active");
  const connected = activeAgents.filter((agent) => agent.connected).length;
  const readyEntries = data.publications.filter((publication) => publication.status === "ready").length;
  const needsNetwork = activeAgents.filter((agent) => !agent.networkProfile).length;
  const recentActions = groupActions(data.actions).slice(0, 5).map((group) => group.actions[0]);
  return (
    <section className="flex flex-col gap-7">
      <PageHeading title={copy(language, "欢迎回来", "Welcome back")} description={copy(language, "从这里查看节点、应用和访问入口是否正常。", "See whether your nodes, apps, and access points are healthy.")} />
      <SetupGuide activeAgents={activeAgents.length} language={language} needsNetwork={needsNetwork} onNavigate={onNavigate} runningApps={data.applications.filter((application) => application.status === "running").length} />
      <div className="grid gap-4 sm:grid-cols-3">
        <SummaryCard description={copy(language, "在线节点", "online nodes")} icon={<ServerIcon />} title={`${connected}/${activeAgents.length}`} />
        <SummaryCard description={copy(language, "运行中的应用", "running apps")} icon={<AppWindowIcon />} title={String(data.applications.filter((application) => application.status === "running").length)} />
        <SummaryCard description={copy(language, "可访问入口", "ready access points")} icon={<CircleCheckIcon />} title={String(readyEntries)} />
      </div>

      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3"><div><h2 className="text-lg font-semibold">{copy(language, "位置", "Locations")}</h2><p className="mt-1 text-sm text-muted-foreground">{copy(language, "位置把同一网络中的节点放在一起。", "A location groups nodes that share a network.")}</p></div><Button onClick={() => { setEditingSite(null); setSiteEditorOpen(true); }} size="sm" variant="outline"><PlusIcon data-icon="inline-start" />{copy(language, "新建位置", "New location")}</Button></div>
        <div className="grid gap-4 lg:grid-cols-2">
          {data.sites.map((site) => {
            const agents = activeAgents.filter((agent) => agent.siteId === site.id);
            return <Card key={site.id}><CardHeader><CardTitle>{site.name}</CardTitle><CardDescription>{site.description || copy(language, "一个网络位置", "A network location")}</CardDescription><CardAction><Button aria-label={copy(language, "编辑位置", "Edit location")} onClick={() => { setEditingSite(site); setSiteEditorOpen(true); }} size="icon-sm" variant="ghost"><PencilIcon /></Button></CardAction></CardHeader><CardContent><dl className="grid grid-cols-2 gap-4 text-sm"><div><dt className="text-muted-foreground">{copy(language, "节点", "Nodes")}</dt><dd className="mt-1 font-medium">{agents.length}</dd></div><div><dt className="text-muted-foreground">{copy(language, "网关", "Gateways")}</dt><dd className="mt-1 font-medium">{site.gatewayNodes.length}</dd></div><div><dt className="text-muted-foreground">{copy(language, "时区", "Time zone")}</dt><dd className="mt-1 truncate text-xs font-medium">{site.timezone}</dd></div><div><dt className="text-muted-foreground">{copy(language, "默认域名", "Default domain")}</dt><dd className="mt-1 truncate font-mono text-xs">{site.domainSuffix || copy(language, "未设置", "Not set")}</dd></div></dl></CardContent><CardFooter className="justify-between"><StateBadge value={site.gatewayStatus} /><Button onClick={() => onNavigate("network")} size="sm" variant="ghost">{copy(language, "查看网络", "View network")}<ArrowRightIcon data-icon="inline-end" /></Button></CardFooter></Card>;
          })}
        </div>
      </div>

      <div className="flex flex-col gap-4">
        <div className="flex items-center justify-between"><h2 className="text-lg font-semibold">{copy(language, "最近活动", "Recent activity")}</h2><Button onClick={() => onNavigate("activity")} size="sm" variant="ghost">{copy(language, "查看全部", "View all")}<ArrowRightIcon data-icon="inline-end" /></Button></div>
        <Card size="sm"><CardContent className="flex flex-col gap-3">{recentActions.map((action) => <div className="flex min-h-11 items-center gap-3 border-b border-border/70 py-2 last:border-b-0" key={action.id}><StateBadge language={language} value={action.event} /><div className="min-w-0 flex-1"><p className="truncate text-sm font-medium">{actionKind(language, action.kind)}</p><p className="truncate text-xs text-muted-foreground">{actionMessage(language, action.message) || action.taskId}</p></div><time className="hidden text-xs text-muted-foreground sm:block">{formatDate(language, action.createdAt)}</time></div>)}</CardContent></Card>
      </div>
      <SiteEditor agents={activeAgents} language={language} open={siteEditorOpen} site={editingSite} onClose={() => { setSiteEditorOpen(false); setEditingSite(null); }} onSave={async (site, input) => { await mutate(() => site ? api.updateSite(site, input) : api.createSite(input), copy(language, site ? "位置信息已保存。" : "位置已创建。", site ? "Location saved." : "Location created.")); setSiteEditorOpen(false); setEditingSite(null); }} />
    </section>
  );
}

function SetupGuide({ activeAgents, language, needsNetwork, onNavigate, runningApps }: { activeAgents: number; language: Language; needsNetwork: number; onNavigate: (screen: Screen) => void; runningApps: number }) {
  const steps = [
    { done: activeAgents > 0, title: copy(language, "添加第一台节点", "Add your first node"), description: copy(language, "在 Linux 设备运行 Center 生成的一条命令。", "Run one command generated by Center on a Linux device."), screen: "nodes" as Screen, action: copy(language, "添加节点", "Add node") },
    { done: activeAgents > 0 && needsNetwork === 0, title: copy(language, "确认节点网络", "Confirm node networking"), description: copy(language, "使用 Agent 自动发现的建议地址，不需要手动判断网卡。", "Use the addresses suggested by Agent; no interface knowledge is required."), screen: "network" as Screen, action: copy(language, "确认网络", "Confirm network") },
    { done: runningApps > 0, title: copy(language, "安装第一个应用", "Install your first app"), description: copy(language, "选择应用和节点，其他选项都有安全默认值。", "Choose an app and node; the remaining options have safe defaults."), screen: "apps" as Screen, action: copy(language, "打开应用商店", "Open app store") }
  ];
  const current = steps.findIndex((step) => !step.done);
  if (current === -1) return null;
  const completed = steps.filter((step) => step.done).length;
  return <Card className="overflow-hidden"><CardHeader><CardTitle>{copy(language, "完成首次设置", "Finish setup")}</CardTitle><CardDescription>{copy(language, "一次只完成当前步骤，Vastora 会自动显示下一步。", "Complete one step at a time; Vastora reveals what comes next automatically.")}</CardDescription><CardAction><span className="text-sm font-medium text-muted-foreground">{completed}/{steps.length}</span></CardAction></CardHeader><CardContent><ol className="flex flex-col">{steps.map((step, index) => <li aria-current={index === current ? "step" : undefined} className="grid min-h-16 grid-cols-[20px_minmax(0,1fr)] items-start gap-x-3 border-b py-3 last:border-b-0 sm:flex" key={step.title}>{step.done ? <CircleCheckIcon aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-success" /> : <span aria-hidden="true" className={`grid size-5 shrink-0 place-items-center rounded-full border text-[11px] font-semibold ${index === current ? "border-primary bg-primary text-primary-foreground" : "text-muted-foreground"}`}>{index + 1}</span>}<div className="min-w-0 flex-1"><p className="text-sm font-medium">{step.title}</p><p className="mt-1 text-xs leading-5 text-muted-foreground">{step.description}</p></div>{index === current ? <Button className="col-start-2 mt-2 w-full sm:mt-0 sm:w-auto sm:shrink-0" onClick={() => onNavigate(step.screen)} size="sm">{step.action}<ArrowRightIcon data-icon="inline-end" /></Button> : null}</li>)}</ol></CardContent></Card>;
}

function SummaryCard({ icon, title, description }: { icon: ReactNode; title: string; description: string }) {
  return <Card size="sm"><CardHeader><CardTitle className="flex items-center gap-2">{icon}{title}</CardTitle><CardDescription>{description}</CardDescription></CardHeader></Card>;
}

function SiteEditor({ agents, language, open, site, onClose, onSave }: { agents: AgentView[]; language: Language; open: boolean; site: Site | null; onClose: () => void; onSave: (site: Site | null, input: SiteInput) => Promise<void> }) {
  const [name, setName] = useState("");
  const [code, setCode] = useState("");
  const [description, setDescription] = useState("");
  const [timezone, setTimezone] = useState(browserTimezone);
  const [domainSuffix, setDomainSuffix] = useState("");
  const [gatewayNodes, setGatewayNodes] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    setName(site?.name ?? ""); setCode(site?.code ?? `site-${crypto.randomUUID().slice(0, 8)}`); setDescription(site?.description ?? ""); setTimezone(site?.timezone ?? browserTimezone()); setDomainSuffix(site?.domainSuffix ?? ""); setGatewayNodes(site?.gatewayNodes ?? []); setError("");
  }, [open, site]);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true); setError("");
    try { await onSave(site, { name, code, description, timezone, domainSuffix, gatewayNodes }); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); }
  };
  const candidates = agents.filter((agent) => agent.siteId === site?.id && agent.capabilities.gateway);
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{site ? copy(language, "编辑位置", "Edit location") : copy(language, "新建位置", "New location")}</SheetTitle><SheetDescription>{copy(language, "位置通常对应一个家庭、办公室或数据中心。", "A location usually represents a home, office, or data center.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="site-name">{copy(language, "名称", "Name")}</FieldLabel><Input aria-invalid={Boolean(error)} autoFocus id="site-name" onChange={(event) => setName(event.target.value)} placeholder={copy(language, "例如：家里", "For example: Home")} required value={name} /></Field><Field><FieldLabel htmlFor="site-description">{copy(language, "说明", "Description")}</FieldLabel><Input id="site-description" onChange={(event) => setDescription(event.target.value)} value={description} /></Field><Field><FieldLabel htmlFor="site-timezone">{copy(language, "时区", "Time zone")}</FieldLabel><Input id="site-timezone" onChange={(event) => setTimezone(event.target.value)} required value={timezone} /><FieldDescription>{copy(language, "新位置默认使用当前浏览器时区。", "New locations default to this browser's time zone.")}</FieldDescription></Field><Field><FieldLabel htmlFor="site-domain">{copy(language, "默认域名", "Default domain")}</FieldLabel><Input id="site-domain" onChange={(event) => setDomainSuffix(event.target.value.toLowerCase())} placeholder="home.example.com" value={domainSuffix} /><FieldDescription>{copy(language, "可以留空，发布服务时再填写完整域名。", "Optional. A full hostname can be entered when publishing.")}</FieldDescription></Field>{site ? <FieldSet><FieldLegend>{copy(language, "网关节点", "Gateway nodes")}</FieldLegend><FieldDescription>{copy(language, "网关负责把此位置内的 Web 服务变成容易访问的地址。", "Gateways provide friendly addresses for Web services in this location.")}</FieldDescription><FieldGroup>{candidates.map((agent) => <Field key={agent.id} orientation="horizontal"><FieldLabel htmlFor={`gateway-${agent.id}`}><span>{agent.name}</span><span className="text-xs font-normal text-muted-foreground">{agent.networkProfile?.serviceAddress || copy(language, "网络未确认", "Network not confirmed")}</span></FieldLabel><Switch checked={gatewayNodes.includes(agent.id)} id={`gateway-${agent.id}`} onCheckedChange={(checked) => setGatewayNodes((current) => checked ? [...new Set([...current, agent.id])] : current.filter((id) => id !== agent.id))} /></Field>)}</FieldGroup></FieldSet> : null}<details className="rounded-xl border p-3"><summary className="cursor-pointer text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}</summary><Field className="mt-4"><FieldLabel htmlFor="site-code">{copy(language, "内部标识", "Internal code")}</FieldLabel><Input id="site-code" onChange={(event) => setCode(event.target.value.toLowerCase())} pattern="[a-z][a-z0-9-]{0,31}" required value={code} /><FieldDescription>{copy(language, "用于内部识别，通常不需要修改。", "Used internally and normally does not need to change.")}</FieldDescription></Field></details>{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !name || !timezone} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{site ? copy(language, "保存", "Save") : copy(language, "创建位置", "Create location")}</Button></SheetFooter></form></SheetContent></Sheet>;
}
