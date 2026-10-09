import { useEffect, useState, type FormEvent } from "react";
import { AppWindowIcon, ArrowRightIcon, CircleCheckIcon, CircleArrowUpIcon, PencilIcon, PlusIcon, ServerIcon } from "lucide-react";
import { api } from "../api";
import { browserTimezone } from "../lib/network";
import { actionKind, groupActions, visibleActionMessage } from "./activityPresentation";
import type { AppData, Mutate, Screen } from "../App";
import type { AgentView, Site, SiteInput } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, copy, formatDate, userError } from "./shared";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldSet, FieldLegend } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { TimezoneCombobox } from "@/components/TimezoneCombobox";

import { Alert, AlertTitle, AlertDescription } from "@/components/ui/alert";
import { publicationNeedsAttention } from "./installed-apps-model";
import { HomeTaskSummary } from "./HomeTaskSummary";

export function HomeView({ data, language, onNavigate, mutate }: { data: AppData; language: Language; onNavigate: (screen: Screen) => void; mutate: Mutate }) {
  const [editingSite, setEditingSite] = useState<Site | null>(null);
  const [siteEditorOpen, setSiteEditorOpen] = useState(false);
  const activeAgents = data.agents.filter((agent) => agent.status === "active");
  const connected = activeAgents.filter((agent) => agent.connected && !agent.credentialRevoked).length;
  const readyEntries = data.publications.filter((publication) => publication.status === "ready" && !publicationNeedsAttention(publication)).length;
  const entryIssues = data.publications.filter((publication) => publication.status !== "stopped" && publicationNeedsAttention(publication)).length;
  const appIssues = data.applications.filter((app) => ["failed", "degraded"].includes(app.status)).length;
  const needsNetwork = activeAgents.filter((agent) => !agent.networkProfile).length;
  const recentActions = groupActions(data.actions).slice(0, 5).map((group) => group.actions[0]);
  const editingServiceIDs = new Set(data.services.filter((service) => service.siteId === editingSite?.id).map((service) => service.id));
  const namespaceLocked = data.publications.some((publication) => publication.status !== "stopped" && editingServiceIDs.has(publication.serviceId));
  const attention = [
    connected < activeAgents.length ? copy(language, `${activeAgents.length - connected} 台节点未连接`, `${activeAgents.length - connected} nodes disconnected`) : "",
    appIssues ? copy(language, `${appIssues} 个应用异常`, `${appIssues} app issues`) : "",
    entryIssues ? copy(language, `${entryIssues} 个访问入口异常`, `${entryIssues} access issues`) : "",
  ].filter(Boolean);
  return <section className="mac-overview flex min-w-0 flex-col gap-5">
    <PageHeading title={copy(language, "概览", "Overview")} description={copy(language, "先看运行状态，再处理需要关注的事项。", "Check current status and anything that needs attention.")} />
    {attention.length ? <Alert><AlertTitle>{attention.join(" · ")}</AlertTitle><AlertDescription><Button onClick={() => onNavigate(connected < activeAgents.length ? "nodes" : "apps")} size="sm" variant="outline">{copy(language, "查看运行状态", "View status")}</Button></AlertDescription></Alert> : null}
    <HomeTaskSummary data={data} language={language} onNavigate={onNavigate} />
    <div className="mac-overview-summary">
      {[{ screen: "nodes" as Screen, icon: ServerIcon, value: `${connected}/${activeAgents.length}`, label: copy(language, "节点在线", "Nodes online") }, { screen: "apps" as Screen, icon: AppWindowIcon, value: data.applications.filter((app) => app.status === "running").length, label: copy(language, "应用运行中", "Apps running") }, { screen: "apps" as Screen, icon: CircleCheckIcon, value: readyEntries, label: copy(language, "访问入口就绪", "Access points ready") }].map(({ screen, icon: Icon, value, label }) => <Button className="h-auto justify-between p-4" key={label} variant="outline" onClick={() => onNavigate(screen)}><span className="flex items-center gap-3"><Icon data-icon="inline-start" /><span className="text-left"><strong className="block text-xl tabular-nums">{value}</strong><span className="text-xs text-muted-foreground">{label}</span></span></span><ArrowRightIcon data-icon="inline-end" /></Button>)}
    </div>
    {data.centerUpdate.updateAvailable ? <Card size="sm"><CardHeader><CardTitle className="flex items-center gap-2"><CircleArrowUpIcon />{copy(language, "管理中心有新版本", "Management center update available")}</CardTitle><CardDescription>{data.centerUpdate.latestVersion}</CardDescription><CardAction><Button onClick={() => { onNavigate("settings"); window.history.replaceState({}, "", "/settings#updates"); }} size="sm" variant="outline">{copy(language, "查看更新", "View update")}</Button></CardAction></CardHeader></Card> : null}
    <SetupGuide activeAgents={activeAgents.length} language={language} needsNetwork={needsNetwork} onNavigate={onNavigate} runningApps={data.applications.filter((app) => app.status === "running").length} />
    <Card size="sm"><CardHeader><CardTitle>{copy(language, "最近活动", "Recent activity")}</CardTitle><CardAction><Button onClick={() => onNavigate("activity")} size="sm" variant="ghost">{copy(language, "查看全部", "View all")}<ArrowRightIcon data-icon="inline-end" /></Button></CardAction></CardHeader><CardContent className="flex flex-col gap-2">
      {recentActions.length ? recentActions.map((action) => <Button className="h-auto min-w-0 justify-start gap-3 whitespace-normal py-3 text-left" key={action.id} onClick={() => onNavigate("activity")} variant="ghost"><StateBadge language={language} value={action.currentState || action.event} /><span className="min-w-0 flex-1"><span className="block text-sm font-medium">{data.agents.find((agent) => agent.id === action.agentId)?.name ?? copy(language, "未知节点", "Unknown node")} · {actionKind(language, action.kind)}</span><span className="mt-1 block text-xs text-muted-foreground">{visibleActionMessage(language, action)}</span></span><time className="hidden shrink-0 text-xs text-muted-foreground sm:block">{formatDate(language, action.createdAt)}</time></Button>) : <p className="py-3 text-sm text-muted-foreground">{copy(language, "暂无活动。添加节点或安装应用后，进度会显示在这里。", "No activity yet. Node and app operations will appear here.")}</p>}
    </CardContent></Card>
    <details className="rounded-xl border bg-card"><summary className="cursor-pointer px-4 py-3 text-sm font-medium">{copy(language, `位置管理 · ${data.sites.length}`, `Locations · ${data.sites.length}`)}</summary><div className="flex flex-col gap-3 border-t p-4"><div className="flex items-center justify-between gap-3"><p className="text-sm text-muted-foreground">{copy(language, "按家庭、办公室或机房分组管理节点。", "Group nodes by home, office, or data center.")}</p><Button onClick={() => { setEditingSite(null); setSiteEditorOpen(true); }} size="sm" variant="outline"><PlusIcon data-icon="inline-start" />{copy(language, "新建位置", "New location")}</Button></div>
      {data.sites.map((site) => <div className="flex min-w-0 items-center gap-3 border-b py-2 last:border-b-0" key={site.id}><div className="min-w-0 flex-1"><p className="truncate text-sm font-medium">{site.name}</p><p className="text-xs text-muted-foreground">{copy(language, `${activeAgents.filter((agent) => agent.siteId === site.id).length} 台节点`, `${activeAgents.filter((agent) => agent.siteId === site.id).length} nodes`)}</p></div><Button aria-label={copy(language, `编辑位置 ${site.name}`, `Edit location ${site.name}`)} onClick={() => { setEditingSite(site); setSiteEditorOpen(true); }} size="sm" variant="ghost"><PencilIcon data-icon="inline-start" />{copy(language, "编辑", "Edit")}</Button></div>)}
    </div></details>
    <SiteEditor agents={activeAgents} language={language} namespaceLocked={namespaceLocked} open={siteEditorOpen} site={editingSite} onClose={() => { setSiteEditorOpen(false); setEditingSite(null); }} onSave={async (site, input) => { await mutate(() => site ? api.updateSite(site, input) : api.createSite(input), copy(language, site ? "位置信息已保存。" : "位置已创建。", site ? "Location saved." : "Location created.")); setSiteEditorOpen(false); setEditingSite(null); }} />
  </section>;
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

function SiteEditor({ agents, language, namespaceLocked, open, site, onClose, onSave }: { agents: AgentView[]; language: Language; namespaceLocked: boolean; open: boolean; site: Site | null; onClose: () => void; onSave: (site: Site | null, input: SiteInput) => Promise<void> }) {
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
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{site ? copy(language, "编辑位置", "Edit location") : copy(language, "新建位置", "New location")}</SheetTitle><SheetDescription>{copy(language, "位置通常对应一个家庭、办公室或数据中心。", "A location usually represents a home, office, or data center.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="site-name">{copy(language, "名称", "Name")}</FieldLabel><Input aria-invalid={Boolean(error)} autoFocus id="site-name" onChange={(event) => setName(event.target.value)} placeholder={copy(language, "例如：家里", "For example: Home")} required value={name} /></Field><Field><FieldLabel htmlFor="site-description">{copy(language, "说明", "Description")}</FieldLabel><Input id="site-description" onChange={(event) => setDescription(event.target.value)} value={description} /></Field><Field><FieldLabel htmlFor="site-timezone">{copy(language, "时区", "Time zone")}</FieldLabel><TimezoneCombobox id="site-timezone" language={language} onValueChange={setTimezone} value={timezone} /><FieldDescription>{copy(language, "新位置默认使用当前浏览器时区，也可以搜索并选择。", "New locations default to this browser's time zone and can be searched and changed.")}</FieldDescription></Field><Field><FieldLabel htmlFor="site-domain">{copy(language, "服务域名空间", "Service domain namespace")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" disabled={namespaceLocked} id="site-domain" onChange={(event) => setDomainSuffix(event.target.value.toLowerCase())} placeholder="vastora.example.com" spellCheck={false} value={domainSuffix} /><FieldDescription>{namespaceLocked ? copy(language, "已有访问入口正在使用此域名空间；停止这些入口后才能修改。", "Active access points use this namespace. Stop them before changing it.") : copy(language, `服务会按“服务.应用.${code || "位置"}.${domainSuffix || "vastora.example.com"}”分层命名。`, `Services use the hierarchy “service.app.${code || "location"}.${domainSuffix || "vastora.example.com"}”.`)}</FieldDescription></Field>{site ? <FieldSet><FieldLegend>{copy(language, "网关节点", "Gateway nodes")}</FieldLegend><FieldDescription>{copy(language, "网关负责把此位置内的 Web 服务变成容易访问的地址。第一个具备网关能力并完成网络确认的节点会自动选中。", "Gateways provide friendly addresses for Web services in this location. The first gateway-capable node is selected automatically after network confirmation.")}</FieldDescription><FieldGroup>{candidates.map((agent) => <Field key={agent.id} orientation="horizontal"><FieldLabel htmlFor={`gateway-${agent.id}`}><span>{agent.name}</span><span className="text-xs font-normal text-muted-foreground">{agent.networkProfile?.serviceAddress || copy(language, "网络未确认", "Network not confirmed")}</span></FieldLabel><Switch checked={gatewayNodes.includes(agent.id)} id={`gateway-${agent.id}`} onCheckedChange={(checked) => setGatewayNodes((current) => checked ? [...new Set([...current, agent.id])] : current.filter((id) => id !== agent.id))} /></Field>)}</FieldGroup></FieldSet> : null}<details className="rounded-xl border p-3"><summary className="cursor-pointer text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}</summary><Field className="mt-4"><FieldLabel htmlFor="site-code">{copy(language, "域名中的位置标识", "Location label in domains")}</FieldLabel><Input disabled={namespaceLocked} id="site-code" onChange={(event) => setCode(event.target.value.toLowerCase())} pattern="[a-z][a-z0-9-]{0,31}" required value={code} /><FieldDescription>{namespaceLocked ? copy(language, "此标识已经用于现有入口，暂时不能修改。", "This label is already used by active access points.") : copy(language, "会出现在此位置的服务域名中；创建入口前可以修改。", "Appears in this location's service hostnames and can be changed before access points are created.")}</FieldDescription></Field></details>{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !name || !timezone} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{site ? copy(language, "保存", "Save") : copy(language, "创建位置", "Create location")}</Button></SheetFooter></form></SheetContent></Sheet>;
}
