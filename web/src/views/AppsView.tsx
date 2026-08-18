import { useEffect, useMemo, useState, type FormEvent } from "react";
import { AppWindowIcon, ArrowUpCircleIcon, CopyIcon, ExternalLinkIcon, Globe2Icon, KeyRoundIcon, PackagePlusIcon, RotateCcwIcon, ShieldAlertIcon, Trash2Icon } from "lucide-react";
import { api } from "../api";
import type { DashboardData, Mutate } from "../App";
import type { AgentView, Application, AppView, Deployment, Publication, PublicationKind, Service } from "../types";
import type { Language } from "../translations";
import { HighPrivilegeBadge, PageHeading, StateBadge, copy } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

type DeploymentEditor = { app: AppView; agent?: AgentView; operation: "install" | "upgrade" } | null;

export function AppsView({ data, language, mutate }: { data: DashboardData; language: Language; mutate: Mutate }) {
  const [deploymentEditor, setDeploymentEditor] = useState<DeploymentEditor>(null);
  const [publicationService, setPublicationService] = useState<Service | null>(null);
  const [uninstallApplication, setUninstallApplication] = useState<Application | null>(null);
  const [credentials, setCredentials] = useState<Deployment["oneTimeCredentials"]>(undefined);
  const catalogByKey = useMemo(() => new Map(data.apps.map((app) => [app.key, app])), [data.apps]);
  const installedApplications = data.applications.filter((application) => application.status === "running");
  const recentOperations = latestOperations(data.deployments).filter((deployment) => deployment.state === "pending" || deployment.state === "running" || deployment.state === "failed");

  const openUpgrade = (application: Application) => {
    const app = catalogByKey.get(application.appKey);
    const agent = data.agents.find((value) => value.id === application.nodeId);
    if (app && agent) setDeploymentEditor({ app, agent, operation: "upgrade" });
  };

  return (
    <section className="flex flex-col gap-7">
      <PageHeading title={copy(language, "应用", "Apps")} description={copy(language, "先把应用安装为私有服务，再为需要访问的服务添加一个或多个入口。", "Install an app as a private service first, then add one or more access points to the services you need.")} />

      {credentials ? <Alert><KeyRoundIcon /><AlertTitle>{copy(language, "请立即保存 3x-ui 管理账号", "Save the 3x-ui administrator account now")}</AlertTitle><AlertDescription><p>{copy(language, "凭据只显示这一次。Center 和 Agent 会分别加密保存。", "These credentials are shown only once. Center and Agent store them separately in encrypted form.")}</p><dl className="mt-3 grid gap-2 rounded-lg bg-muted p-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">{copy(language, "账号", "Username")}</dt><dd className="mt-1 flex items-center gap-2 font-mono">{credentials.username}<CopyButton language={language} value={credentials.username} /></dd></div><div><dt className="text-muted-foreground">{copy(language, "密码", "Password")}</dt><dd className="mt-1 flex items-center gap-2 break-all font-mono">{credentials.password}<CopyButton language={language} value={credentials.password} /></dd></div></dl><Button className="mt-3" onClick={() => setCredentials(undefined)} size="sm" variant="outline">{copy(language, "我已保存", "I saved them")}</Button></AlertDescription></Alert> : null}

      {recentOperations.length ? <div aria-live="polite" className="flex flex-col gap-3"><div><h2 className="text-lg font-semibold">{copy(language, "最近操作", "Recent operations")}</h2><p className="mt-1 text-sm text-muted-foreground">{copy(language, "页面会自动更新，无需手动刷新。", "This page updates automatically; no manual refresh is needed.")}</p></div>{recentOperations.map((deployment) => {
        const app = catalogByKey.get(deployment.appKey); const agent = data.agents.find((value) => value.id === deployment.agentId); const application = data.applications.find((value) => value.id === deployment.applicationId);
        const retry = () => { if (!app || !agent) return; if (deployment.operation === "uninstall" && application) setUninstallApplication(application); else setDeploymentEditor({ app, agent: deployment.operation === "upgrade" ? agent : undefined, operation: deployment.operation === "upgrade" ? "upgrade" : "install" }); };
        return <Card key={deployment.id} size="sm"><CardContent className="flex flex-col gap-3 py-4 sm:flex-row sm:items-center"><StateBadge language={language} value={deployment.state} /><div className="min-w-0 flex-1"><p className="font-medium">{operationLabel(language, deployment.operation)} · {app ? localized(app, language, "name") : deployment.appKey}</p><p className="mt-1 text-xs text-muted-foreground">{agent?.name ?? deployment.agentId}</p>{deployment.error ? <p className="mt-2 text-sm text-destructive">{deployment.error}</p> : null}</div>{deployment.state === "failed" && app && agent ? <Button onClick={retry} size="sm" variant="outline"><RotateCcwIcon data-icon="inline-start" />{copy(language, "重试", "Retry")}</Button> : null}</CardContent></Card>;
      })}</div> : null}

      <div className="flex flex-col gap-4">
        <div><h2 className="text-lg font-semibold">{copy(language, "已安装", "Installed")}</h2><p className="mt-1 text-sm text-muted-foreground">{copy(language, "每个服务都可以有独立的访问方式。", "Each service can have its own access methods.")}</p></div>
        {installedApplications.length === 0 ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><AppWindowIcon /></EmptyMedia><EmptyTitle>{copy(language, "还没有安装应用", "No apps installed yet")}</EmptyTitle><EmptyDescription>{copy(language, "从下方应用商店选择一个应用开始；失败任务只保留在活动记录中。", "Choose an app from the store below. Failed tasks remain only in Activity.")}</EmptyDescription></EmptyHeader></Empty> : <div className="grid gap-4 lg:grid-cols-2">{installedApplications.map((application) => <InstalledAppCard application={application} app={catalogByKey.get(application.appKey)} data={data} key={application.id} language={language} onPublish={setPublicationService} onUninstall={() => setUninstallApplication(application)} onUpgrade={() => openUpgrade(application)} mutate={mutate} />)}</div>}
      </div>

      <div className="flex flex-col gap-4">
        <div><h2 className="text-lg font-semibold">{copy(language, "应用商店", "App Store")}</h2><p className="mt-1 text-sm text-muted-foreground">{copy(language, "应用默认只在节点的私有地址上运行。", "Apps run on the node's private address by default.")}</p></div>
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">{data.apps.map((app) => {
          const installed = installedApplications.filter((application) => application.appKey === app.key);
          const eligible = data.agents.filter((agent) => canInstall(agent) && !data.applications.some((application) => application.nodeId === agent.id && application.appKey === app.key && isActiveApplication(application.status)));
          const blocker = eligible.length === 0 ? installBlocker(data, app.key, language) : "";
          return <Card key={app.key}><CardHeader><CardTitle>{localized(app, language, "name")}</CardTitle><CardDescription>{localized(app, language, "description")}</CardDescription><CardAction>{app.app.hostAccess ? <HighPrivilegeBadge language={language} /> : <Badge variant="outline">Docker</Badge>}</CardAction></CardHeader><CardContent><div className="flex flex-wrap gap-2"><Badge variant="secondary">v{app.app.version}</Badge>{app.app.services?.map((service) => <Badge key={service.name} variant="outline">{service.name}</Badge>)}</div>{app.app.hostAccess ? <p className="mt-3 flex items-start gap-2 text-xs leading-5 text-destructive"><ShieldAlertIcon className="mt-0.5 size-3.5 shrink-0" />{copy(language, "此应用需要主机级权限，请确认来源与用途。", "This app needs host-level access. Confirm its source and purpose.")}</p> : null}{blocker ? <p className="mt-3 text-xs leading-5 text-muted-foreground" id={`install-blocker-${app.app.id}`}>{blocker}</p> : null}</CardContent><CardFooter className="justify-between"><span className="text-xs text-muted-foreground">{installed.length ? copy(language, `已安装到 ${installed.length} 个节点`, `Installed on ${installed.length} node(s)`) : copy(language, "尚未安装", "Not installed")}</span><Button aria-describedby={blocker ? `install-blocker-${app.app.id}` : undefined} disabled={eligible.length === 0} onClick={() => setDeploymentEditor({ app, operation: "install" })} size="sm"><PackagePlusIcon data-icon="inline-start" />{copy(language, "安装", "Install")}</Button></CardFooter></Card>;
        })}</div>
      </div>

      <DeploymentSheet data={data} editor={deploymentEditor} language={language} onClose={() => setDeploymentEditor(null)} onSubmit={async (agent, app, config, operation) => {
        let result: Deployment | undefined;
        await mutate(async () => { result = await api.createDeployment(agent.id, app.key, config, operation); }, operation === "install" ? copy(language, "安装任务已创建。可在活动中查看进度。", "Install task created. Follow progress in Activity.") : copy(language, "升级任务已创建。", "Upgrade task created."));
        if (result?.oneTimeCredentials) setCredentials(result.oneTimeCredentials);
        setDeploymentEditor(null);
      }} />
      <PublicationSheet data={data} language={language} onClose={() => setPublicationService(null)} onSubmit={async (input) => { await mutate(() => api.createPublication(input), copy(language, "访问入口已创建。", "Access point created.")); setPublicationService(null); }} service={publicationService} />
      <UninstallSheet application={uninstallApplication} app={uninstallApplication ? catalogByKey.get(uninstallApplication.appKey) : undefined} language={language} onClose={() => setUninstallApplication(null)} onSubmit={async (application, deleteData) => { await mutate(() => api.createDeployment(application.nodeId, application.appKey, {}, "uninstall", deleteData), copy(language, "卸载任务已创建。", "Uninstall task created.")); setUninstallApplication(null); }} />
    </section>
  );
}

function InstalledAppCard({ application, app, data, language, onPublish, onUninstall, onUpgrade, mutate }: { application: Application; app?: AppView; data: DashboardData; language: Language; onPublish: (service: Service) => void; onUninstall: () => void; onUpgrade: () => void; mutate: Mutate }) {
  const agent = data.agents.find((value) => value.id === application.nodeId);
  const services = data.services.filter((service) => service.applicationId === application.id && service.status !== "stopped");
  const deployment = data.deployments.find((value) => value.applicationId === application.id && value.state === "succeeded" && value.operation !== "uninstall");
  return <Card><CardHeader><CardTitle className="flex flex-wrap items-center gap-2">{app ? localized(app, language, "name") : application.name}{app?.app.hostAccess ? <HighPrivilegeBadge language={language} /> : null}</CardTitle><CardDescription>{agent?.name ?? application.nodeId} · {application.runtime}</CardDescription><CardAction><StateBadge value={application.status} /></CardAction></CardHeader><CardContent className="flex flex-col gap-3">{deployment?.accessUrl ? <Button render={<a href={deployment.accessUrl} rel="noreferrer" target="_blank" />} size="sm" variant="outline"><ExternalLinkIcon data-icon="inline-start" />{copy(language, "打开私有主页", "Open private homepage")}</Button> : null}{services.length === 0 ? <p className="text-sm text-muted-foreground">{copy(language, "此应用没有可发布的 Web 服务。", "This app has no publishable Web service.")}</p> : services.map((service) => <ServiceRow data={data} key={service.id} language={language} onPublish={() => onPublish(service)} service={service} mutate={mutate} />)}</CardContent><CardFooter className="justify-end gap-2"><Button onClick={onUpgrade} size="sm" variant="outline"><ArrowUpCircleIcon data-icon="inline-start" />{copy(language, "升级", "Upgrade")}</Button><Button onClick={onUninstall} size="sm" variant="ghost"><Trash2Icon data-icon="inline-start" />{copy(language, "卸载", "Uninstall")}</Button></CardFooter></Card>;
}

function ServiceRow({ data, language, service, onPublish, mutate }: { data: DashboardData; language: Language; service: Service; onPublish: () => void; mutate: Mutate }) {
  const publications = data.publications.filter((value) => value.serviceId === service.id && value.status !== "stopped");
  return <div className="rounded-xl border p-3"><div className="flex flex-wrap items-center gap-2"><div className="min-w-0 flex-1"><p className="truncate text-sm font-medium">{service.name}</p><p className="mt-0.5 truncate font-mono text-xs text-muted-foreground">{service.protocol} · {service.endpoint}</p></div>{service.management ? <Badge variant="destructive">{copy(language, "管理页", "Admin")}</Badge> : null}<Button onClick={onPublish} size="sm" variant="outline"><Globe2Icon data-icon="inline-start" />{copy(language, "添加入口", "Add access")}</Button></div>{publications.length ? <div className="mt-3 flex flex-col gap-2">{publications.map((publication) => <PublicationRow key={publication.id} language={language} mutate={mutate} publication={publication} />)}</div> : <p className="mt-3 text-xs text-muted-foreground">{copy(language, "仅私有源站，尚未添加访问入口。", "Private origin only; no access point added.")}</p>}</div>;
}

function PublicationRow({ publication, language, mutate }: { publication: Publication; language: Language; mutate: Mutate }) {
  const [busy, setBusy] = useState(false);
  const run = async (operation: () => Promise<unknown>, success: string) => { setBusy(true); try { await mutate(operation, success); } finally { setBusy(false); } };
  return <div className="flex flex-col gap-2 rounded-lg bg-muted/60 p-3 text-xs sm:flex-row sm:items-center"><StateBadge value={publication.status} /><div className="min-w-0 flex-1"><p className="truncate font-medium">{publication.hostname}</p><p className="mt-0.5 text-muted-foreground">{publicationKindLabel(language, publication.kind)} · {publication.dnsProvider}</p>{publication.lastError ? <p className="mt-1 text-destructive">{publication.lastError}</p> : null}{publication.dnsRecord && publication.dnsProvider !== "cloudflare" ? <code className="mt-1 block break-all text-muted-foreground">{publication.dnsRecord.type} {publication.dnsRecord.name} → {publication.dnsRecord.value}</code> : null}</div><div className="flex gap-1">{publication.accessUrl ? <Button aria-label={copy(language, "打开服务", "Open service")} render={<a href={publication.accessUrl} rel="noreferrer" target="_blank" />} size="icon-sm" variant="ghost"><ExternalLinkIcon /></Button> : null}{publication.status !== "ready" ? <Button disabled={busy} onClick={() => void run(() => api.verifyPublication(publication.id), copy(language, "入口检查已完成。", "Access point checked."))} size="sm" variant="outline">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "检查", "Check")}</Button> : null}<Button disabled={busy} onClick={() => void run(() => api.stopPublication(publication.id), copy(language, "入口已停止。", "Access point stopped."))} size="sm" variant="ghost">{copy(language, "停止", "Stop")}</Button></div></div>;
}

function DeploymentSheet({ data, editor, language, onClose, onSubmit }: { data: DashboardData; editor: DeploymentEditor; language: Language; onClose: () => void; onSubmit: (agent: AgentView, app: AppView, config: Record<string, string | boolean | number>, operation: "install" | "upgrade") => Promise<void> }) {
  const [agentID, setAgentID] = useState("");
  const [config, setConfig] = useState<Record<string, string | boolean | number>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const candidates = editor ? data.agents.filter((agent) => editor.operation === "upgrade" ? agent.id === editor.agent?.id : canInstall(agent) && !data.applications.some((application) => application.nodeId === agent.id && application.appKey === editor.app.key && isActiveApplication(application.status))) : [];
  useEffect(() => {
    if (!editor) return;
    const defaults: Record<string, string | boolean | number> = {};
    for (const field of editor.app.app.config) {
      if (field.key === "timezone") defaults[field.key] = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
      else if (field.default !== undefined) defaults[field.key] = field.default;
      else defaults[field.key] = field.type === "boolean" ? false : "";
    }
    setAgentID(editor.agent?.id ?? candidates[0]?.id ?? "");
    setConfig(editor.operation === "upgrade" ? {} : defaults);
    setError("");
  }, [editor]);
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); if (!editor) return; const agent = data.agents.find((value) => value.id === agentID); if (!agent) { setError(copy(language, "请选择节点。", "Select a node.")); return; } setBusy(true); setError(""); try { await onSubmit(agent, editor.app, config, editor.operation); } catch (submitError) { setError(submitError instanceof Error ? submitError.message : "Request failed"); } finally { setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(editor)}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{editor ? copy(language, `${editor.operation === "install" ? "安装" : "升级"} ${localized(editor.app, language, "name")}`, `${editor.operation === "install" ? "Install" : "Upgrade"} ${localized(editor.app, language, "name")}`) : ""}</SheetTitle><SheetDescription>{editor?.operation === "install" ? copy(language, "应用先作为私有源站启动；访问入口稍后单独添加。", "The app starts as a private origin. Add access points separately afterward.") : copy(language, "只填写需要修改的配置；留空项保持原值。", "Enter only settings to change; omitted values keep their previous value.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="deployment-agent">{copy(language, "节点", "Node")}</FieldLabel><NativeSelect disabled={editor?.operation === "upgrade"} id="deployment-agent" onChange={(event) => setAgentID(event.target.value)} required value={agentID}><option disabled value="">{copy(language, "选择节点", "Select a node")}</option>{candidates.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</NativeSelect><FieldDescription>{candidates.length === 0 ? copy(language, "没有可安装节点。请先确认节点网络，或该应用已安装。", "No eligible node. Confirm node networking, or the app is already installed.") : copy(language, "同一应用在同一节点只能安装一次。", "An app can be installed only once on the same node.")}</FieldDescription></Field>{editor?.app.app.config.map((field) => <ConfigField config={config} field={field} key={field.key} language={language} operation={editor.operation} setConfig={setConfig} />)}{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !agentID} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{editor?.operation === "install" ? copy(language, "开始安装", "Install") : copy(language, "开始升级", "Upgrade")}</Button></SheetFooter></form></SheetContent></Sheet>;
}

function ConfigField({ config, field, language, operation, setConfig }: { config: Record<string, string | boolean | number>; field: AppView["app"]["config"][number]; language: Language; operation: "install" | "upgrade"; setConfig: React.Dispatch<React.SetStateAction<Record<string, string | boolean | number>>> }) {
  const label = field.label[language] || field.label.en;
  const description = field.description[language] || field.description.en;
  if (field.type === "boolean") return <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor={`config-${field.key}`}>{label}</FieldLabel><FieldDescription>{description}</FieldDescription></div><Switch checked={Boolean(config[field.key])} id={`config-${field.key}`} onCheckedChange={(value) => setConfig((current) => ({ ...current, [field.key]: value }))} /></Field>;
  return <Field><FieldLabel htmlFor={`config-${field.key}`}>{label}</FieldLabel><Input id={`config-${field.key}`} min={field.type === "integer" ? 1 : undefined} onChange={(event) => setConfig((current) => ({ ...current, [field.key]: field.type === "integer" ? Number(event.target.value) : event.target.value }))} placeholder={operation === "upgrade" ? copy(language, "留空以保持原值", "Leave blank to keep the current value") : undefined} required={operation === "install" && field.required} type={field.secret ? "password" : field.type === "integer" ? "number" : "text"} value={config[field.key] === undefined ? "" : String(config[field.key])} /><FieldDescription>{description}</FieldDescription></Field>;
}

function PublicationSheet({ data, language, onClose, onSubmit, service }: { data: DashboardData; language: Language; onClose: () => void; onSubmit: (input: { serviceId: string; kind: PublicationKind; gatewayNodeId?: string; hostname: string; dnsProvider: "manual" | "cloudflare" | "headscale"; confirmHighRisk?: boolean }) => Promise<void>; service: Service | null }) {
  const [kind, setKind] = useState<PublicationKind>("headscale_gateway");
  const [gatewayID, setGatewayID] = useState("");
  const [hostname, setHostname] = useState("");
  const [dnsProvider, setDNSProvider] = useState<"manual" | "cloudflare" | "headscale">("manual");
  const [highRisk, setHighRisk] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const app = service ? data.applications.find((value) => value.id === service.applicationId) : undefined;
  const appAgent = data.agents.find((value) => value.id === app?.nodeId);
  const site = data.sites.find((value) => value.id === service?.siteId);
  const web = service?.protocol === "http" || service?.protocol === "https";
  const cloudflareReady = data.integrations.some((value) => value.kind === "cloudflare" && value.status === "configured");
  const headscaleReady = data.integrations.some((value) => value.kind === "headscale" && value.status === "configured" && value.mode === "builtin");
  const options = useMemo(() => publicationOptions(data, service, language), [data, service, language]);
  const gateways = service ? gatewaysForKind(data, service, kind) : [];
  useEffect(() => {
    if (!service) return;
    const preferred = options.find((option) => option.kind === "headscale_gateway" && option.enabled) ?? options.find((option) => option.kind === "lan_gateway" && option.enabled) ?? options.find((option) => option.enabled);
    const nextKind = preferred?.kind ?? "lan_gateway";
    const nextGateways = gatewaysForKind(data, service, nextKind);
    setKind(nextKind);
    setGatewayID(nextKind === "public_direct" && !web ? appAgent?.id ?? "" : nextGateways[0]?.id ?? "");
    setHostname(site?.domainSuffix ? `${service.name}.${site.domainSuffix}` : "");
    setDNSProvider(nextKind === "headscale_gateway" && headscaleReady ? "headscale" : nextKind === "cloudflare_tunnel" || (nextKind === "public_direct" || nextKind === "public_shared_443") && cloudflareReady ? "cloudflare" : "manual");
    setHighRisk(false); setError("");
  }, [service]);
  const selectKind = (next: PublicationKind) => {
    setKind(next); const nodes = service ? gatewaysForKind(data, service, next) : [];
    setGatewayID(next === "public_direct" && !web ? appAgent?.id ?? "" : nodes[0]?.id ?? "");
    setDNSProvider(next === "headscale_gateway" && headscaleReady ? "headscale" : next === "cloudflare_tunnel" || (next === "public_direct" || next === "public_shared_443") && cloudflareReady ? "cloudflare" : "manual");
    setHighRisk(false);
  };
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); if (!service) return; setBusy(true); setError(""); try { await onSubmit({ serviceId: service.id, kind, gatewayNodeId: gatewayID || undefined, hostname, dnsProvider, confirmHighRisk: highRisk }); } catch (submitError) { setError(submitError instanceof Error ? submitError.message : "Request failed"); } finally { setBusy(false); } };
  const highRiskRequired = Boolean(service?.management && (kind === "public_direct" || kind === "cloudflare_tunnel"));
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(service)}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, `发布 ${service?.name ?? ""}`, `Publish ${service?.name ?? ""}`)}</SheetTitle><SheetDescription>{copy(language, "一个服务可以同时拥有多个入口。公网 Web 入口强制使用 HTTPS。", "A service can have multiple access points. Public Web access always uses HTTPS.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><FieldSet><FieldLegend>{copy(language, "访问方式", "Access method")}</FieldLegend>{options.map((option) => <label className="flex min-h-11 items-start gap-3 rounded-xl border p-3 has-checked:border-primary has-checked:bg-primary/5" data-disabled={!option.enabled} key={option.kind}><input checked={kind === option.kind} className="mt-1" disabled={!option.enabled} name="publication-kind" onChange={() => selectKind(option.kind)} type="radio" value={option.kind} /><span className="flex min-w-0 flex-1 flex-col gap-1 text-sm"><span className="font-medium">{publicationKindLabel(language, option.kind)}</span><span className="text-muted-foreground">{option.reason}</span></span></label>)}</FieldSet><Field><FieldLabel htmlFor="publication-hostname">{kind === "public_shared_443" ? "SNI " : ""}{copy(language, "域名", "Hostname")}</FieldLabel><Input id="publication-hostname" onChange={(event) => setHostname(event.target.value.toLowerCase())} placeholder="service.example.com" required value={hostname} /></Field>{kind !== "public_direct" || web ? <Field><FieldLabel htmlFor="publication-node">{kind === "cloudflare_tunnel" ? copy(language, "Tunnel 节点", "Tunnel node") : copy(language, "入口节点（Gateway）", "Entry node (Gateway)")}</FieldLabel><NativeSelect id="publication-node" onChange={(event) => setGatewayID(event.target.value)} required value={gatewayID}><option disabled value="">{copy(language, "选择节点", "Select a node")}</option>{gateways.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</NativeSelect><FieldDescription>{kind === "public_shared_443" ? copy(language, "HAProxy 将按 SNI 分流，Caddy 仍负责此节点上的 Web HTTPS。", "HAProxy routes by SNI while Caddy continues to manage Web HTTPS on this node.") : copy(language, "入口节点可以反代同一位置内其他节点的服务。", "An entry node can proxy services from other nodes in the same location.")}</FieldDescription></Field> : <Field><FieldLabel>{copy(language, "监听节点", "Listening node")}</FieldLabel><div className="rounded-lg border bg-muted/40 p-3 text-sm">{appAgent?.name ?? "—"}</div><FieldDescription>{copy(language, "原始 TCP/UDP 端口必须由应用所在公网节点直接监听，Vastora 只观察状态。", "Raw TCP/UDP ports must listen directly on the app's public node; Vastora only observes their status.")}</FieldDescription></Field>}{kind === "public_shared_443" ? <Alert><ShieldAlertIcon /><AlertTitle>{copy(language, "应用内部端口不能是 443", "The app's internal port cannot be 443")}</AlertTitle><AlertDescription>{copy(language, "请先在 3x-ui 中把入站改为其他内部端口。公网客户端仍然连接 443；TLS 由协议端到端处理。", "First move the 3x-ui inbound to another internal port. Public clients still connect to 443, and protocol TLS remains end-to-end.")}</AlertDescription></Alert> : null}<Field><FieldLabel htmlFor="publication-dns">DNS</FieldLabel><NativeSelect disabled={kind === "cloudflare_tunnel"} id="publication-dns" onChange={(event) => setDNSProvider(event.target.value as "manual" | "cloudflare" | "headscale")} value={dnsProvider}>{kind === "lan_gateway" ? <option value="manual">{copy(language, "手动配置", "Manual")}</option> : null}{kind === "headscale_gateway" ? <><option value="manual">{copy(language, "手动配置", "Manual")}</option>{headscaleReady ? <option value="headscale">Headscale DNS</option> : null}</> : null}{kind === "public_direct" || kind === "public_shared_443" ? <><option value="manual">{copy(language, "手动配置", "Manual")}</option>{cloudflareReady ? <option value="cloudflare">Cloudflare DNS-only</option> : null}</> : null}{kind === "cloudflare_tunnel" ? <option value="cloudflare">Cloudflare Tunnel</option> : null}</NativeSelect></Field>{highRiskRequired ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "管理页面公网发布风险较高", "Publishing an admin page publicly is high risk")}</AlertTitle><AlertDescription>{copy(language, "Vastora 第一版不托管 Cloudflare Access。必须依赖应用自身认证，并明确确认后才能继续。", "Vastora v1 does not manage Cloudflare Access. Rely on the app's own authentication and explicitly confirm to continue.")}<Field className="mt-3" orientation="horizontal"><FieldLabel htmlFor="confirm-high-risk">{copy(language, "我确认继续公网发布", "I understand and want to publish")}</FieldLabel><Switch checked={highRisk} id="confirm-high-risk" onCheckedChange={setHighRisk} /></Field></AlertDescription></Alert> : null}{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !hostname || !gatewayID || highRiskRequired && !highRisk} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "创建入口", "Create access point")}</Button></SheetFooter></form></SheetContent></Sheet>;
}

function UninstallSheet({ application, app, language, onClose, onSubmit }: { application: Application | null; app?: AppView; language: Language; onClose: () => void; onSubmit: (application: Application, deleteData: boolean) => Promise<void> }) {
  const [deleteData, setDeleteData] = useState(false); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  useEffect(() => { if (application) { setDeleteData(false); setError(""); } }, [application]);
  const submit = async () => { if (!application) return; setBusy(true); setError(""); try { await onSubmit(application, deleteData); } catch (submitError) { setError(submitError instanceof Error ? submitError.message : "Request failed"); } finally { setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(application)}><SheetContent><SheetHeader><SheetTitle>{copy(language, `卸载 ${app ? localized(app, language, "name") : application?.name ?? ""}`, `Uninstall ${app ? localized(app, language, "name") : application?.name ?? ""}`)}</SheetTitle><SheetDescription>{copy(language, "卸载会停止应用并移除所有访问入口。默认保留持久数据，便于以后重新安装。", "Uninstalling stops the app and removes all access points. Persistent data is kept by default for a later reinstall.")}</SheetDescription></SheetHeader><div className="px-4"><Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="delete-data">{copy(language, "同时永久删除应用数据", "Permanently delete app data too")}</FieldLabel><FieldDescription>{copy(language, "此操作不可恢复，包括配置、账号和历史数据。", "This cannot be undone and includes configuration, accounts, and history.")}</FieldDescription></div><Switch checked={deleteData} id="delete-data" onCheckedChange={setDeleteData} /></Field>{deleteData ? <Alert className="mt-4" variant="destructive"><Trash2Icon /><AlertTitle>{copy(language, "应用数据将永久删除", "App data will be permanently deleted")}</AlertTitle></Alert> : null}{error ? <FieldError className="mt-3">{error}</FieldError> : null}</div><SheetFooter><Button onClick={onClose} variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} onClick={() => void submit()} variant={deleteData ? "destructive" : "default"}>{busy ? <Spinner data-icon="inline-start" /> : null}{deleteData ? copy(language, "卸载并删除数据", "Uninstall and delete data") : copy(language, "卸载并保留数据", "Uninstall and keep data")}</Button></SheetFooter></SheetContent></Sheet>;
}

function publicationOptions(data: DashboardData, service: Service | null, language: Language) {
  if (!service) return [];
  const web = service.protocol === "http" || service.protocol === "https";
  const cloudflare = data.integrations.some((value) => value.kind === "cloudflare" && value.status === "configured");
  const lan = gatewaysForKind(data, service, "lan_gateway").length > 0;
  const headscale = gatewaysForKind(data, service, "headscale_gateway").length > 0;
  const direct = gatewaysForKind(data, service, "public_direct").length > 0;
  const shared443 = gatewaysForKind(data, service, "public_shared_443").length > 0;
  const tunnel = gatewaysForKind(data, service, "cloudflare_tunnel").length > 0 && cloudflare;
  const options: Array<{ kind: PublicationKind; enabled: boolean; reason: string }> = [];
  if (web) {
    options.push({ kind: "lan_gateway", enabled: lan, reason: lan ? "HTTP · LAN Gateway" : copy(language, "没有可用的局域网 Gateway", "No LAN Gateway is available") });
    options.push({ kind: "headscale_gateway", enabled: headscale, reason: headscale ? "HTTP · Headscale Gateway" : copy(language, "没有可用的 Headscale Gateway", "No Headscale Gateway is available") });
    options.push({ kind: "public_direct", enabled: direct, reason: direct ? "HTTPS · Public Gateway" : copy(language, "没有已批准的公网 Gateway", "No approved public Gateway is available") });
    options.push({ kind: "cloudflare_tunnel", enabled: tunnel, reason: tunnel ? "HTTPS · Cloudflare Tunnel" : copy(language, "请先连接 Cloudflare 并启用 Tunnel 节点", "Connect Cloudflare and enable a Tunnel node first") });
  } else {
    options.push({ kind: "public_direct", enabled: direct, reason: direct ? copy(language, "原始端口 · 仅应用节点", "Direct raw port · app node only") : copy(language, "应用节点没有已批准的公网地址", "The app node has no approved public address") });
    if (service.protocol === "tcp") options.push({ kind: "public_shared_443", enabled: shared443, reason: shared443 ? copy(language, "与 Web 共用公网 443 · 自动启用 HAProxy", "Share public 443 with Web · enables HAProxy automatically") : copy(language, "没有可用的公网 Gateway", "No public Gateway is available") });
  }
  return options;
}

function gatewaysForKind(data: DashboardData, service: Service, kind: PublicationKind): AgentView[] {
  const app = data.applications.find((value) => value.id === service.applicationId);
  const site = data.sites.find((value) => value.id === service.siteId);
  return data.agents.filter((agent) => {
    if (!agent.connected || agent.siteId !== service.siteId || !agent.networkProfile) return false;
    if (kind === "cloudflare_tunnel") return agent.capabilities.tunnel;
    if (kind === "public_direct" && service.protocol !== "http" && service.protocol !== "https") return agent.id === app?.nodeId && agent.networkProfile.directPublic && agent.networkProfile.enabledKinds.includes("public");
    if (!agent.capabilities.gateway || !site?.gatewayNodes.includes(agent.id)) return false;
    if (kind === "lan_gateway") return agent.networkProfile.enabledKinds.includes("lan");
    if (kind === "headscale_gateway") return agent.networkProfile.enabledKinds.includes("headscale");
    return agent.networkProfile.directPublic && agent.networkProfile.enabledKinds.includes("public");
  });
}

function canInstall(agent: AgentView) { return agent.connected && agent.capabilities.docker && Boolean(agent.networkProfile); }
function isActiveApplication(status: string) { return ["pending", "deploying", "running"].includes(status); }
function latestOperations(deployments: Deployment[]) {
  const seen = new Set<string>();
  return deployments.filter((deployment) => {
    const key = `${deployment.agentId}\n${deployment.appKey}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}
function operationLabel(language: Language, operation: Deployment["operation"]) {
  const labels: Record<Deployment["operation"], [string, string]> = { install: ["安装", "Install"], upgrade: ["升级", "Upgrade"], uninstall: ["卸载", "Uninstall"] };
  return copy(language, ...labels[operation]);
}
function installBlocker(data: DashboardData, appKey: string, language: Language) {
  if (data.agents.length === 0) return copy(language, "先添加一台节点，再安装应用。", "Add a node before installing apps.");
  if (!data.agents.some((agent) => agent.connected)) return copy(language, "没有在线节点。请检查 Agent 服务。", "No node is online. Check the Agent service.");
  if (!data.agents.some((agent) => agent.connected && agent.capabilities.docker)) return copy(language, "在线节点没有 Docker 应用能力。", "Online nodes do not provide Docker app capability.");
  if (!data.agents.some((agent) => agent.connected && agent.capabilities.docker && agent.networkProfile)) return copy(language, "请先在“网络”页面确认节点地址。", "Confirm a node address on the Network page first.");
  if (data.agents.every((agent) => !canInstall(agent) || data.applications.some((application) => application.nodeId === agent.id && application.appKey === appKey && isActiveApplication(application.status)))) return copy(language, "所有可用节点都已安装此应用。", "This app is already installed on every eligible node.");
  return copy(language, "当前没有符合条件的节点。", "No eligible node is currently available.");
}
function localized(app: AppView, language: Language, field: "name" | "description") { return app.app[field][language] || app.app[field].en; }
function publicationKindLabel(language: Language, kind: PublicationKind) {
  const labels: Record<PublicationKind, [string, string]> = { lan_gateway: ["局域网", "Local network"], headscale_gateway: ["Headscale 私网", "Headscale private network"], public_direct: ["公网直连", "Direct public"], public_shared_443: ["共享 443", "Shared 443"], cloudflare_tunnel: ["Cloudflare Tunnel", "Cloudflare Tunnel"] };
  return copy(language, ...labels[kind]);
}
function CopyButton({ language, value }: { language: Language; value: string }) { return <Button aria-label={copy(language, "复制", "Copy")} onClick={() => void navigator.clipboard.writeText(value)} size="icon-sm" variant="ghost"><CopyIcon /></Button>; }
