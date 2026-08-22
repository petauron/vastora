import { useEffect, useMemo, useState, type FormEvent } from "react";
import { AppWindowIcon, ArrowUpCircleIcon, ExternalLinkIcon, Globe2Icon, KeyRoundIcon, PackagePlusIcon, RadioTowerIcon, RotateCcwIcon, Settings2Icon, ShieldAlertIcon, Trash2Icon, UsersIcon } from "lucide-react";
import { api } from "../api";
import type { AppData, Mutate } from "../App";
import type { AgentView, Application, ApplicationCommand, AppView, Deployment, Publication, PublicationKind, Service } from "../types";
import type { Language } from "../translations";
import { canInstall, defaultPublicationHostname, defaultRealityHostname, gatewaysForKind, installBlocker, isActiveApplication, isInstalledApplication, latestOperations, localized, operationLabel, publicationIntentOptions, publicationKindLabel, publicationKindsForIntent, publicationOptions, type PublicationIntent } from "./appAccess";
import { CopyButton, HighPrivilegeBadge, PageHeading, StateBadge, TechnicalError, copy, userError } from "./shared";
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
import { ThreeXUIClientsSheet } from "./ThreeXUIClientsSheet";

type DeploymentEditor = { app: AppView; agent?: AgentView; operation: "install" | "upgrade" | "configure" } | null;

export function AppsView({ data, language, mutate }: { data: AppData; language: Language; mutate: Mutate }) {
  const [deploymentEditor, setDeploymentEditor] = useState<DeploymentEditor>(null);
  const [publicationService, setPublicationService] = useState<Service | null>(null);
  const [uninstallApplication, setUninstallApplication] = useState<Application | null>(null);
  const [credentials, setCredentials] = useState<Deployment["oneTimeCredentials"]>(undefined);
  const [realityApplication, setRealityApplication] = useState<Application | null>(null);
  const [subscriptionApplication, setSubscriptionApplication] = useState<Application | null>(null);
  const [clientsApplication, setClientsApplication] = useState<Application | null>(null);
  const [section, setSection] = useState<"installed" | "store">(() => data.applications.some(isInstalledApplication) ? "installed" : "store");
  const catalogByKey = useMemo(() => new Map(data.apps.map((app) => [app.key, app])), [data.apps]);
  const installedApplications = data.applications.filter(isInstalledApplication);
  const recentOperations = latestOperations(data.deployments).filter((deployment) => deployment.state === "pending" || deployment.state === "running" || deployment.state === "failed");

  const openChange = (application: Application, operation: "upgrade" | "configure") => {
    const app = catalogByKey.get(application.appKey);
    const agent = data.agents.find((value) => value.id === application.nodeId);
    if (app && agent) setDeploymentEditor({ app, agent, operation });
  };

  return (
    <section className="flex flex-col gap-7">
      <PageHeading title={copy(language, "应用", "Apps")} description={copy(language, "先把应用安装为私有服务，再为需要访问的服务添加一个或多个入口。", "Install an app as a private service first, then add one or more access points to the services you need.")} />

      {credentials ? <Alert><KeyRoundIcon /><AlertTitle>{copy(language, "请立即保存 3x-ui 管理账号", "Save the 3x-ui administrator account now")}</AlertTitle><AlertDescription><p>{copy(language, "凭据只显示这一次。Center 和 Agent 会分别加密保存。", "These credentials are shown only once. Center and Agent store them separately in encrypted form.")}</p><dl className="mt-3 grid gap-2 rounded-lg bg-muted p-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">{copy(language, "账号", "Username")}</dt><dd className="mt-1 flex items-center gap-2 font-mono">{credentials.username}<CopyButton language={language} value={credentials.username} /></dd></div><div><dt className="text-muted-foreground">{copy(language, "密码", "Password")}</dt><dd className="mt-1 flex items-center gap-2 break-all font-mono">{credentials.password}<CopyButton language={language} value={credentials.password} /></dd></div></dl><Button className="mt-3" onClick={() => setCredentials(undefined)} size="sm" variant="outline">{copy(language, "我已保存", "I saved them")}</Button></AlertDescription></Alert> : null}

      {recentOperations.length ? <div aria-live="polite" className="flex flex-col gap-3"><div><h2 className="text-lg font-semibold">{copy(language, "最近操作", "Recent operations")}</h2><p className="mt-1 text-sm text-muted-foreground">{copy(language, "页面会自动更新，无需手动刷新。", "This page updates automatically; no manual refresh is needed.")}</p></div>{recentOperations.map((deployment) => {
        const app = catalogByKey.get(deployment.appKey); const agent = data.agents.find((value) => value.id === deployment.agentId); const application = data.applications.find((value) => value.id === deployment.applicationId);
        const retry = () => { if (!app || !agent) return; if (deployment.operation === "uninstall" && application) setUninstallApplication(application); else if (deployment.operation === "upgrade" || deployment.operation === "configure") setDeploymentEditor({ app, agent, operation: deployment.operation }); else setDeploymentEditor({ app, operation: "install" }); };
        return <Card key={deployment.id} size="sm"><CardContent className="flex flex-col gap-3 py-4 sm:flex-row sm:items-center"><StateBadge language={language} value={deployment.state} /><div className="min-w-0 flex-1"><p className="font-medium">{operationLabel(language, deployment.operation)} · {app ? localized(app, language, "name") : deployment.appKey}</p><p className="mt-1 text-xs text-muted-foreground">{agent?.name ?? deployment.agentId}</p>{deployment.error ? <div className="mt-2"><TechnicalError error={deployment.error} language={language} /></div> : null}</div>{deployment.state === "failed" && app && agent ? <Button onClick={retry} size="sm" variant="outline"><RotateCcwIcon data-icon="inline-start" />{copy(language, "重试", "Retry")}</Button> : null}</CardContent></Card>;
      })}</div> : null}

      <div aria-label={copy(language, "应用内容", "App content")} className="inline-flex w-fit rounded-xl bg-muted p-1" role="group">
        <Button aria-pressed={section === "installed"} onClick={() => setSection("installed")} size="sm" type="button" variant={section === "installed" ? "default" : "ghost"}>{copy(language, "已安装", "Installed")}<Badge variant={section === "installed" ? "outline" : "secondary"}>{installedApplications.length}</Badge></Button>
        <Button aria-pressed={section === "store"} onClick={() => setSection("store")} size="sm" type="button" variant={section === "store" ? "default" : "ghost"}>{copy(language, "应用商店", "App Store")}<Badge variant={section === "store" ? "outline" : "secondary"}>{data.apps.length}</Badge></Button>
      </div>

      {section === "installed" ? <div className="flex flex-col gap-4">
        <div><h2 className="text-lg font-semibold">{copy(language, "已安装", "Installed")}</h2><p className="mt-1 text-sm text-muted-foreground">{copy(language, "每个服务都可以有独立的访问方式。", "Each service can have its own access methods.")}</p></div>
        {installedApplications.length === 0 ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><AppWindowIcon /></EmptyMedia><EmptyTitle>{copy(language, "还没有安装应用", "No apps installed yet")}</EmptyTitle><EmptyDescription>{copy(language, "从应用商店选择一个应用开始；失败任务只保留在活动记录中。", "Choose an app from the store to get started. Failed tasks remain only in Activity.")}</EmptyDescription><Button className="mt-3" onClick={() => setSection("store")} size="sm">{copy(language, "打开应用商店", "Open App Store")}</Button></EmptyHeader></Empty> : <div className="grid gap-4 lg:grid-cols-2">{installedApplications.map((application) => <InstalledAppCard application={application} app={catalogByKey.get(application.appKey)} data={data} key={application.id} language={language} onClients={() => setClientsApplication(application)} onConfigure={() => openChange(application, "configure")} onPublish={setPublicationService} onReality={() => setRealityApplication(application)} onSubscription={() => setSubscriptionApplication(application)} onUninstall={() => setUninstallApplication(application)} onUpgrade={() => openChange(application, "upgrade")} mutate={mutate} />)}</div>}
      </div> : null}

      {section === "store" ? <div className="flex flex-col gap-4">
        <div><h2 className="text-lg font-semibold">{copy(language, "应用商店", "App Store")}</h2><p className="mt-1 text-sm text-muted-foreground">{copy(language, "应用默认只在节点的私有地址上运行。", "Apps run on the node's private address by default.")}</p></div>
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">{data.apps.map((app) => {
          const installed = installedApplications.filter((application) => application.appKey === app.key);
          const eligible = data.agents.filter((agent) => canInstall(agent) && !data.applications.some((application) => application.nodeId === agent.id && application.appKey === app.key && (isInstalledApplication(application) || isActiveApplication(application.status))));
          const blocker = eligible.length === 0 ? installBlocker(data, app.key, language) : "";
          return <Card key={app.key}><CardHeader><CardTitle>{localized(app, language, "name")}</CardTitle><CardDescription>{localized(app, language, "description")}</CardDescription><CardAction>{app.app.hostAccess ? <HighPrivilegeBadge language={language} /> : <Badge variant="outline">Docker</Badge>}</CardAction></CardHeader><CardContent><div className="flex flex-wrap gap-2"><Badge variant="secondary">v{app.app.version}</Badge>{app.app.services?.map((service) => <Badge key={service.name} variant="outline">{service.name}</Badge>)}</div>{app.app.hostAccess ? <p className="mt-3 flex items-start gap-2 text-xs leading-5 text-destructive"><ShieldAlertIcon className="mt-0.5 size-3.5 shrink-0" />{copy(language, "此应用需要主机级权限，请确认来源与用途。", "This app needs host-level access. Confirm its source and purpose.")}</p> : null}{blocker ? <p className="mt-3 text-xs leading-5 text-muted-foreground" id={`install-blocker-${app.app.id}`}>{blocker}</p> : null}</CardContent><CardFooter className="justify-between"><span className="text-xs text-muted-foreground">{installed.length ? copy(language, `已安装到 ${installed.length} 个节点`, `Installed on ${installed.length} node(s)`) : copy(language, "尚未安装", "Not installed")}</span><Button aria-describedby={blocker ? `install-blocker-${app.app.id}` : undefined} disabled={eligible.length === 0} onClick={() => setDeploymentEditor({ app, operation: "install" })} size="sm"><PackagePlusIcon data-icon="inline-start" />{copy(language, "安装", "Install")}</Button></CardFooter></Card>;
        })}</div>
      </div> : null}

      <DeploymentSheet data={data} editor={deploymentEditor} language={language} onClose={() => setDeploymentEditor(null)} onSubmit={async (agent, app, config, operation) => {
        let result: Deployment | undefined;
        const messages = { install: copy(language, "安装任务已创建。可在活动中查看进度。", "Install task created. Follow progress in Activity."), upgrade: copy(language, "升级任务已创建。", "Upgrade task created."), configure: copy(language, "配置任务已创建。", "Configuration task created.") };
        await mutate(async () => { result = await api.createDeployment(agent.id, app.key, config, operation); }, messages[operation]);
        if (result?.oneTimeCredentials) setCredentials(result.oneTimeCredentials);
        setDeploymentEditor(null);
      }} />
      <PublicationSheet data={data} language={language} onClose={() => setPublicationService(null)} onSubmit={async (input) => { await mutate(() => api.createPublication(input), copy(language, "访问入口已创建。", "Access point created.")); setPublicationService(null); }} service={publicationService} />
      <RealitySheet application={realityApplication} data={data} language={language} onClose={() => setRealityApplication(null)} />
      <SubscriptionSheet application={subscriptionApplication} data={data} language={language} mutate={mutate} onClose={() => setSubscriptionApplication(null)} />
      <ThreeXUIClientsSheet advancedURL={clientsApplication ? data.deployments.find((value) => value.applicationId === clientsApplication.id && value.state === "succeeded" && value.operation !== "uninstall")?.accessUrl : undefined} application={clientsApplication} language={language} onClose={() => setClientsApplication(null)} />
      <UninstallSheet application={uninstallApplication} app={uninstallApplication ? catalogByKey.get(uninstallApplication.appKey) : undefined} language={language} onClose={() => setUninstallApplication(null)} onSubmit={async (application, deleteData) => { await mutate(() => api.createDeployment(application.nodeId, application.appKey, {}, "uninstall", deleteData), copy(language, "卸载任务已创建。", "Uninstall task created.")); setUninstallApplication(null); }} />
    </section>
  );
}

function InstalledAppCard({ application, app, data, language, onClients, onConfigure, onPublish, onReality, onSubscription, onUninstall, onUpgrade, mutate }: { application: Application; app?: AppView; data: AppData; language: Language; onClients: () => void; onConfigure: () => void; onPublish: (service: Service) => void; onReality: () => void; onSubscription: () => void; onUninstall: () => void; onUpgrade: () => void; mutate: Mutate }) {
  const agent = data.agents.find((value) => value.id === application.nodeId);
  const services = data.services.filter((service) => service.applicationId === application.id && service.status !== "stopped");
  const deployment = data.deployments.find((value) => value.applicationId === application.id && value.state === "succeeded" && value.operation !== "uninstall");
  const activeChange = data.deployments.find((value) => value.applicationId === application.id && (value.state === "pending" || value.state === "running"));
  const subscriptionService = services.find((service) => service.name === "subscription");
  const subscriptionPublication = subscriptionService ? data.publications.find((value) => value.serviceId === subscriptionService.id && value.status !== "stopped" && (value.kind === "cloudflare_tunnel" || value.kind === "public_direct")) : undefined;
  return <Card><CardHeader><CardTitle className="flex flex-wrap items-center gap-2">{app ? localized(app, language, "name") : application.name}{app?.app.hostAccess ? <HighPrivilegeBadge language={language} /> : null}</CardTitle><CardDescription>{agent?.name ?? application.nodeId} · {application.runtime}{application.installedVersion ? ` · v${application.installedVersion}` : ""}</CardDescription><CardAction><StateBadge value={application.status} /></CardAction></CardHeader><CardContent className="flex flex-col gap-3">{activeChange ? <Alert><Spinner /><AlertTitle>{copy(language, `正在${operationLabel(language, activeChange.operation)}`, `${operationLabel(language, activeChange.operation)} in progress`)}</AlertTitle><AlertDescription>{copy(language, "完成前暂时不能发起其他应用变更。", "Other app changes are unavailable until this finishes.")}</AlertDescription></Alert> : null}{application.status === "failed" ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "最近一次操作失败，应用仍保留", "The last operation failed; the app is still installed")}</AlertTitle><AlertDescription>{copy(language, "原有安装记录和数据仍保留；请查看最近操作，修正后重试或卸载。", "The installed record and data remain available. Review the recent operation, then retry or uninstall.")}</AlertDescription></Alert> : null}{application.appKey === "vastora-official/3x-ui" ? <div className="grid gap-2 sm:grid-cols-3"><Button disabled={Boolean(activeChange)} onClick={onClients} size="sm"><UsersIcon data-icon="inline-start" />{copy(language, "管理客户端", "Manage clients")}</Button><Button disabled={Boolean(activeChange)} onClick={onReality} size="sm" variant="outline"><RadioTowerIcon data-icon="inline-start" />{copy(language, "创建 VLESS", "Create VLESS")}</Button>{subscriptionService ? <Button disabled={Boolean(activeChange)} onClick={onSubscription} size="sm" variant="outline"><Globe2Icon data-icon="inline-start" />{subscriptionPublication ? copy(language, "公网订阅", "Public subscription") : copy(language, "开启订阅", "Enable subscription")}</Button> : null}</div> : null}{deployment?.accessUrl ? <Button nativeButton={false} render={<a href={deployment.accessUrl} rel="noreferrer" target="_blank" />} size="sm" variant="outline"><ExternalLinkIcon data-icon="inline-start" />{copy(language, "打开主页", "Open homepage")}</Button> : app?.app.homepage ? <p className="text-xs text-muted-foreground">{copy(language, "添加并完成一个访问入口后，这里会出现“打开主页”。", "After an access point is ready, an Open homepage button appears here.")}</p> : null}{services.length === 0 ? <p className="text-sm text-muted-foreground">{copy(language, "此应用没有可发布的 Web 服务。", "This app has no publishable Web service.")}</p> : services.map((service) => <ServiceRow data={data} key={service.id} language={language} onPublish={() => onPublish(service)} service={service} mutate={mutate} />)}</CardContent><CardFooter className="flex-wrap justify-end gap-2">{application.updateAvailable ? <Button disabled={Boolean(activeChange)} onClick={onUpgrade} size="sm"><ArrowUpCircleIcon data-icon="inline-start" />{copy(language, `升级到 v${application.availableVersion}`, `Upgrade to v${application.availableVersion}`)}</Button> : app ? <Badge variant="secondary">{copy(language, "版本已是最新", "Version up to date")}</Badge> : null}{app && app.app.config.length > 0 && !application.updateAvailable ? <Button disabled={Boolean(activeChange)} onClick={onConfigure} size="sm" variant="outline"><Settings2Icon data-icon="inline-start" />{copy(language, "修改配置", "Change settings")}</Button> : null}<Button disabled={Boolean(activeChange)} onClick={onUninstall} size="sm" variant="ghost"><Trash2Icon data-icon="inline-start" />{copy(language, "卸载", "Uninstall")}</Button></CardFooter></Card>;
}

function ServiceRow({ data, language, service, onPublish, mutate }: { data: AppData; language: Language; service: Service; onPublish: () => void; mutate: Mutate }) {
  const publications = data.publications.filter((value) => value.serviceId === service.id && value.status !== "stopped");
  return <div className="rounded-xl border p-3"><div className="flex flex-wrap items-center gap-2"><div className="min-w-0 flex-1"><p className="truncate text-sm font-medium">{service.name}</p><p className="mt-0.5 truncate font-mono text-xs text-muted-foreground">{service.protocol} · {service.endpoint}</p></div>{service.management ? <Badge variant="destructive">{copy(language, "管理页", "Admin")}</Badge> : null}<Button onClick={onPublish} size="sm" variant="outline"><Globe2Icon data-icon="inline-start" />{copy(language, "添加入口", "Add access")}</Button></div>{publications.length ? <div className="mt-3 flex flex-col gap-2">{publications.map((publication) => <PublicationRow key={publication.id} language={language} mutate={mutate} publication={publication} />)}</div> : <p className="mt-3 text-xs text-muted-foreground">{copy(language, "仅私有源站，尚未添加访问入口。", "Private origin only; no access point added.")}</p>}</div>;
}

function PublicationRow({ publication, language, mutate }: { publication: Publication; language: Language; mutate: Mutate }) {
  const [busy, setBusy] = useState(false);
  const run = async (operation: () => Promise<unknown>, success: string) => { setBusy(true); try { await mutate(operation, success); } catch { /* The shared notice already explains the failure. */ } finally { setBusy(false); } };
  return <div className="flex flex-col gap-2 rounded-lg bg-muted/60 p-3 text-xs sm:flex-row sm:items-center"><StateBadge value={publication.status} /><div className="min-w-0 flex-1"><p className="truncate font-medium">{publication.hostname}</p><p className="mt-0.5 text-muted-foreground">{publicationKindLabel(language, publication.kind)}</p>{publication.sniHostname ? <p className="mt-0.5 truncate font-mono text-muted-foreground">SNI → {publication.sniHostname}</p> : null}{publication.lastError ? <div className="mt-1"><TechnicalError error={publication.lastError} language={language} /></div> : null}{publication.dnsRecord && publication.dnsProvider !== "cloudflare" ? <code className="mt-1 block break-all text-muted-foreground">{publication.dnsRecord.type} {publication.dnsRecord.name} → {publication.dnsRecord.value}</code> : null}</div><div className="flex gap-2">{publication.accessUrl ? <Button aria-label={copy(language, "打开服务", "Open service")} nativeButton={false} render={<a href={publication.accessUrl} rel="noreferrer" target="_blank" />} size="icon-sm" variant="ghost"><ExternalLinkIcon /></Button> : null}{publication.status !== "ready" ? <Button disabled={busy} onClick={() => void run(() => api.verifyPublication(publication.id), copy(language, "入口检查已完成。", "Access point checked."))} size="sm" variant="outline">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "检查", "Check")}</Button> : null}<Button disabled={busy} onClick={() => void run(() => api.stopPublication(publication.id), copy(language, "入口已停止。", "Access point stopped."))} size="sm" variant="ghost">{copy(language, "停止", "Stop")}</Button></div></div>;
}

function DeploymentSheet({ data, editor, language, onClose, onSubmit }: { data: AppData; editor: DeploymentEditor; language: Language; onClose: () => void; onSubmit: (agent: AgentView, app: AppView, config: Record<string, string | boolean | number>, operation: "install" | "upgrade" | "configure") => Promise<void> }) {
  const [agentID, setAgentID] = useState("");
  const [config, setConfig] = useState<Record<string, string | boolean | number>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const candidates = editor ? data.agents.filter((agent) => editor.operation !== "install" ? agent.id === editor.agent?.id : canInstall(agent) && !data.applications.some((application) => application.nodeId === agent.id && application.appKey === editor.app.key && (isInstalledApplication(application) || isActiveApplication(application.status)))) : [];
  useEffect(() => {
    if (!editor) return;
    const defaults: Record<string, string | boolean | number> = {};
    for (const field of editor.app.app.config) {
      if (field.key === "timezone") defaults[field.key] = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
      else if (field.default !== undefined) defaults[field.key] = field.default;
      else defaults[field.key] = field.type === "boolean" ? false : "";
    }
    setAgentID(editor.agent?.id ?? candidates[0]?.id ?? "");
    setConfig(editor.operation === "install" ? defaults : {});
    setError("");
  }, [editor]);
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); if (!editor) return; const agent = data.agents.find((value) => value.id === agentID); if (!agent) { setError(copy(language, "请选择节点。", "Select a node.")); return; } setBusy(true); setError(""); try { await onSubmit(agent, editor.app, config, editor.operation); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); } };
  const verbs = editor?.operation === "install" ? ["安装", "Install"] : editor?.operation === "upgrade" ? ["升级", "Upgrade"] : ["修改配置", "Change settings"];
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(editor)}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{editor ? `${copy(language, verbs[0], verbs[1])} ${localized(editor.app, language, "name")}` : ""}</SheetTitle><SheetDescription>{editor?.operation === "install" ? copy(language, "应用先作为私有源站启动；访问入口稍后单独添加。", "The app starts as a private origin. Add access points separately afterward.") : editor?.operation === "upgrade" ? copy(language, "升级到目录中的新版本；可同时填写需要修改的配置。", "Upgrade to the newer catalog version and optionally change settings.") : copy(language, "只填写至少一项要修改的配置；留空项保持原值。", "Enter at least one setting to change; omitted values keep their previous value.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="deployment-agent">{copy(language, "节点", "Node")}</FieldLabel><NativeSelect disabled={editor?.operation !== "install"} id="deployment-agent" onChange={(event) => setAgentID(event.target.value)} required value={agentID}><option disabled value="">{copy(language, "选择节点", "Select a node")}</option>{candidates.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</NativeSelect><FieldDescription>{candidates.length === 0 ? copy(language, "没有可用节点。请先确认节点网络。", "No eligible node. Confirm node networking first.") : editor?.operation === "install" ? copy(language, "同一应用在同一节点只能安装一次。", "An app can be installed only once on the same node.") : copy(language, "现有安装会在原节点上更新。", "The existing installation is changed on its current node.")}</FieldDescription></Field>{editor?.app.app.config.map((field) => <ConfigField config={config} field={field} key={field.key} language={language} operation={editor.operation} setConfig={setConfig} />)}{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !agentID || editor?.operation === "configure" && Object.keys(config).length === 0} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{editor?.operation === "install" ? copy(language, "开始安装", "Install") : editor?.operation === "upgrade" ? copy(language, "开始升级", "Upgrade") : copy(language, "应用修改", "Apply changes")}</Button></SheetFooter></form></SheetContent></Sheet>;
}

function ConfigField({ config, field, language, operation, setConfig }: { config: Record<string, string | boolean | number>; field: AppView["app"]["config"][number]; language: Language; operation: "install" | "upgrade" | "configure"; setConfig: React.Dispatch<React.SetStateAction<Record<string, string | boolean | number>>> }) {
  const label = field.label[language] || field.label.en;
  const description = field.description[language] || field.description.en;
  if (field.type === "boolean" && operation !== "install") return <Field><FieldLabel htmlFor={`config-${field.key}`}>{label}</FieldLabel><NativeSelect id={`config-${field.key}`} onChange={(event) => setConfig((current) => { const next = { ...current }; if (!event.target.value) delete next[field.key]; else next[field.key] = event.target.value === "true"; return next; })} value={config[field.key] === undefined ? "" : String(config[field.key])}><option value="">{copy(language, "保持当前设置", "Keep current setting")}</option><option value="true">{copy(language, "开启", "On")}</option><option value="false">{copy(language, "关闭", "Off")}</option></NativeSelect><FieldDescription>{description}</FieldDescription></Field>;
  if (field.type === "boolean") return <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor={`config-${field.key}`}>{label}</FieldLabel><FieldDescription>{description}</FieldDescription></div><Switch checked={Boolean(config[field.key])} id={`config-${field.key}`} onCheckedChange={(value) => setConfig((current) => ({ ...current, [field.key]: value }))} /></Field>;
  return <Field><FieldLabel htmlFor={`config-${field.key}`}>{label}</FieldLabel><Input id={`config-${field.key}`} min={field.type === "integer" ? 1 : undefined} onChange={(event) => setConfig((current) => { const next = { ...current }; if (event.target.value === "") delete next[field.key]; else next[field.key] = field.type === "integer" ? Number(event.target.value) : event.target.value; return next; })} placeholder={operation !== "install" ? copy(language, "留空以保持原值", "Leave blank to keep the current value") : undefined} required={operation === "install" && field.required} type={field.secret ? "password" : field.type === "integer" ? "number" : "text"} value={config[field.key] === undefined ? "" : String(config[field.key])} /><FieldDescription>{description}</FieldDescription></Field>;
}

function PublicationSheet({ data, language, onClose, onSubmit, service }: { data: AppData; language: Language; onClose: () => void; onSubmit: (input: { serviceId: string; kind: PublicationKind; gatewayNodeId?: string; hostname: string; sniHostname?: string; dnsProvider: "manual" | "cloudflare" | "headscale"; tlsEnabled?: boolean; confirmHighRisk?: boolean }) => Promise<void>; service: Service | null }) {
  const [intent, setIntent] = useState<PublicationIntent>("private");
  const [kind, setKind] = useState<PublicationKind>("headscale_gateway");
  const [gatewayID, setGatewayID] = useState("");
  const [hostname, setHostname] = useState("");
  const [sniHostname, setSNIHostname] = useState("");
  const [dnsProvider, setDNSProvider] = useState<"manual" | "cloudflare" | "headscale">("manual");
  const [highRisk, setHighRisk] = useState(false);
  const [tlsEnabled, setTLSEnabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const cloudflareReady = data.integrations.some((value) => value.kind === "cloudflare" && value.status === "configured");
  const headscaleReady = data.integrations.some((value) => value.kind === "headscale" && value.status === "configured" && value.mode === "builtin");
  const options = useMemo(() => publicationOptions(data, service, language), [data, service, language]);
  const intents = useMemo(() => publicationIntentOptions(data, service, language), [data, service, language]);
  const gateways = service ? gatewaysForKind(data, service, kind) : [];
  const availableKinds = service ? publicationKindsForIntent(service, intent) : [];
  const advancedOptions = options.filter((option) => availableKinds.includes(option.kind));
  const selectedOption = options.find((option) => option.kind === kind);
  const defaultDNS = (next: PublicationKind) => {
    if (next === "headscale_gateway" && headscaleReady) return "headscale" as const;
    if (next === "cloudflare_tunnel" || (["public_direct", "public_shared_443"] as PublicationKind[]).includes(next) && cloudflareReady) return "cloudflare" as const;
    return "manual" as const;
  };
  useEffect(() => {
    if (!service) return;
    const preferred = publicationIntentOptions(data, service, language).find((option) => option.enabled);
    const nextKind = preferred?.kind ?? publicationOptions(data, service, language).find((option) => option.enabled)?.kind ?? "lan_gateway";
    setIntent(preferred?.intent ?? (service.protocol === "http" || service.protocol === "https" ? "private" : "protocol"));
    setKind(nextKind);
    setGatewayID(gatewaysForKind(data, service, nextKind)[0]?.id ?? "");
    setHostname(defaultPublicationHostname(data, service));
    setSNIHostname("");
    setDNSProvider(defaultDNS(nextKind));
    setTLSEnabled(cloudflareReady && (nextKind === "lan_gateway" || nextKind === "headscale_gateway"));
    setHighRisk(false); setError("");
  }, [service?.id]);
  const selectKind = (next: PublicationKind) => {
    setKind(next); const nodes = service ? gatewaysForKind(data, service, next) : [];
    setGatewayID(nodes[0]?.id ?? "");
    setDNSProvider(defaultDNS(next));
    setTLSEnabled(cloudflareReady && (next === "lan_gateway" || next === "headscale_gateway"));
    setHighRisk(false);
  };
  const selectIntent = (next: PublicationIntent) => {
    setIntent(next);
    if (!service) return;
    const preferred = publicationIntentOptions(data, service, language).find((option) => option.intent === next)?.kind;
    if (preferred) selectKind(preferred);
  };
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); if (!service) return; setBusy(true); setError(""); try { await onSubmit({ serviceId: service.id, kind, gatewayNodeId: gatewayID || undefined, hostname, sniHostname: kind === "public_shared_443" ? sniHostname : undefined, dnsProvider, tlsEnabled: (kind === "lan_gateway" || kind === "headscale_gateway") && tlsEnabled, confirmHighRisk: highRisk }); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); } };
  const highRiskRequired = Boolean(service?.management && (kind === "public_direct" || kind === "cloudflare_tunnel"));
  const canSubmit = Boolean(selectedOption?.enabled && hostname && gatewayID && (kind !== "public_shared_443" || sniHostname) && (!tlsEnabled || cloudflareReady) && (!highRiskRequired || highRisk));
  return (
    <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(service)}>
      <SheetContent className="sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{copy(language, `添加 ${service?.name ?? ""} 的访问方式`, `Add access to ${service?.name ?? ""}`)}</SheetTitle>
          <SheetDescription>{copy(language, "选择谁可以访问，Vastora 会自动使用当前最安全的可用入口。", "Choose who can access it. Vastora automatically uses the safest available method.")}</SheetDescription>
        </SheetHeader>
        <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}>
          <div className="flex-1 overflow-y-auto px-4">
            <FieldGroup>
              <FieldSet>
                <FieldLegend>{copy(language, "谁需要访问？", "Who needs access?")}</FieldLegend>
                {intents.map((option) => (
                  <label className="flex min-h-11 items-start gap-3 rounded-xl border p-3 has-checked:border-primary has-checked:bg-primary/5" data-disabled={!option.enabled} key={option.intent}>
                    <input checked={intent === option.intent} className="mt-1" disabled={!option.enabled} name="publication-intent" onChange={() => selectIntent(option.intent)} type="radio" value={option.intent} />
                    <span className="flex min-w-0 flex-1 flex-col gap-1 text-sm">
                      <span className="font-medium">{option.title}</span>
                      <span className="text-muted-foreground">{option.description}</span>
                    </span>
                  </label>
                ))}
              </FieldSet>
              <Field>
                <FieldLabel htmlFor="publication-hostname">{copy(language, "访问域名", "Access hostname")}</FieldLabel>
                <Input id="publication-hostname" onChange={(event) => setHostname(event.target.value.toLowerCase())} placeholder="service.example.com" required value={hostname} />
                <FieldDescription>{copy(language, "这是以后在浏览器或客户端中使用的地址。", "This is the address used by browsers or clients.")}</FieldDescription>
              </Field>
              {kind === "public_shared_443" ? <Field><FieldLabel htmlFor="publication-sni">{copy(language, "协议 SNI", "Protocol SNI")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="publication-sni" onChange={(event) => setSNIHostname(event.target.value.toLowerCase())} placeholder="www.example.com" required spellCheck={false} value={sniHostname} /><FieldDescription>{copy(language, "客户端握手中使用的 SNI；它与上面的连接域名是两个不同地址。", "The SNI sent in the client handshake. It is different from the connection hostname above.")}</FieldDescription></Field> : null}
              {intent === "protocol" ? <Alert><ShieldAlertIcon /><AlertTitle>{copy(language, "这是高级公网入口", "This is an advanced public access method")}</AlertTitle><AlertDescription>{copy(language, "应用负责协议和端口配置；Vastora 只检查公网能力与运行状态。", "The app controls protocol and ports. Vastora only checks public reachability and runtime status.")}</AlertDescription></Alert> : null}
              {highRiskRequired ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "管理页面公网发布风险较高", "Publishing an admin page publicly is high risk")}</AlertTitle><AlertDescription>{copy(language, "请确认应用已设置强密码。Vastora 第一版不会代管额外的访问认证。", "Confirm that the app has a strong password. Vastora v1 does not manage an additional access login.")}<Field className="mt-3" orientation="horizontal"><FieldLabel htmlFor="confirm-high-risk">{copy(language, "我确认继续公网发布", "I understand and want to publish")}</FieldLabel><Switch checked={highRisk} id="confirm-high-risk" onCheckedChange={setHighRisk} /></Field></AlertDescription></Alert> : null}
              <details className="rounded-xl border p-3">
                <summary className="cursor-pointer text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}</summary>
                <div className="mt-4 flex flex-col gap-4">
                  <Field>
                    <FieldLabel htmlFor="publication-kind">{copy(language, "底层入口方式", "Underlying access method")}</FieldLabel>
                    <NativeSelect id="publication-kind" onChange={(event) => selectKind(event.target.value as PublicationKind)} value={kind}>
                      {advancedOptions.map((option) => <option disabled={!option.enabled} key={option.kind} value={option.kind}>{publicationKindLabel(language, option.kind)} — {option.reason}</option>)}
                    </NativeSelect>
                    <FieldDescription>{selectedOption?.reason}</FieldDescription>
                  </Field>
                  {kind === "lan_gateway" || kind === "headscale_gateway" ? <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="publication-tls">{copy(language, "浏览器可信 HTTPS", "Browser-trusted HTTPS")}</FieldLabel><FieldDescription>{cloudflareReady ? copy(language, "使用 Cloudflare DNS 验证申请公信证书；服务仍只在私网开放。", "Uses Cloudflare DNS validation for a public-trust certificate while the service remains private.") : copy(language, "连接 Cloudflare 后可开启；否则保留私网 HTTP。", "Connect Cloudflare to enable it; otherwise private HTTP remains available.")}</FieldDescription></div><Switch checked={tlsEnabled} disabled={!cloudflareReady} id="publication-tls" onCheckedChange={setTLSEnabled} /></Field> : null}
                  <Field>
                    <FieldLabel htmlFor="publication-node">{copy(language, "入口节点", "Entry node")}</FieldLabel>
                    <NativeSelect id="publication-node" onChange={(event) => setGatewayID(event.target.value)} required value={gatewayID}>
                      <option disabled value="">{copy(language, "没有可用节点", "No node available")}</option>
                      {gateways.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
                    </NativeSelect>
                    <FieldDescription>{copy(language, "默认使用第一个符合安全条件的节点。", "The first node meeting the safety requirements is selected by default.")}</FieldDescription>
                  </Field>
                  <Field>
                    <FieldLabel htmlFor="publication-dns">DNS</FieldLabel>
                    <NativeSelect disabled={kind === "cloudflare_tunnel"} id="publication-dns" onChange={(event) => setDNSProvider(event.target.value as "manual" | "cloudflare" | "headscale")} value={dnsProvider}>
                      <option value="manual">{copy(language, "手动配置", "Manual")}</option>
                      {kind === "headscale_gateway" && headscaleReady ? <option value="headscale">Headscale DNS</option> : null}
                      {(kind === "public_direct" || kind === "public_shared_443") && cloudflareReady ? <option value="cloudflare">Cloudflare DNS-only</option> : null}
                      {kind === "cloudflare_tunnel" ? <option value="cloudflare">Cloudflare Tunnel</option> : null}
                    </NativeSelect>
                  </Field>
                  {kind === "public_shared_443" ? <Alert><ShieldAlertIcon /><AlertTitle>{copy(language, "共享公网 443", "Shared public 443")}</AlertTitle><AlertDescription>{copy(language, "连接域名只负责解析到节点；HAProxy 会按协议 SNI 分流。应用内部端口不能是 443。", "The connection hostname only resolves to the node. HAProxy routes by protocol SNI, and the app's internal port cannot be 443.")}</AlertDescription></Alert> : null}
                </div>
              </details>
              {error ? <FieldError role="alert">{error}</FieldError> : null}
            </FieldGroup>
          </div>
          <SheetFooter>
            <Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button>
            <Button disabled={busy || !canSubmit} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "创建访问方式", "Create access")}</Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  );
}

function SubscriptionSheet({ application, data, language, mutate, onClose }: { application: Application | null; data: AppData; language: Language; mutate: Mutate; onClose: () => void }) {
  const [kind, setKind] = useState<"cloudflare_tunnel" | "public_direct">("cloudflare_tunnel");
  const [gatewayID, setGatewayID] = useState("");
  const [hostname, setHostname] = useState("");
  const [command, setCommand] = useState<ApplicationCommand | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const service = application ? data.services.find((value) => value.applicationId === application.id && value.name === "subscription" && value.status !== "stopped") : undefined;
  const publication = service ? data.publications.find((value) => value.serviceId === service.id && value.status !== "stopped" && (value.kind === "cloudflare_tunnel" || value.kind === "public_direct")) : undefined;
  const cloudflareReady = data.integrations.some((value) => value.kind === "cloudflare" && value.status === "configured");
  const tunnelGateways = application ? data.agents.filter((agent) => agent.siteId === application.siteId && agent.connected && agent.capabilities.tunnel && Boolean(agent.networkProfile)) : [];
  const directGateways = service ? gatewaysForKind(data, service, "public_direct") : [];
  const gateways = kind === "cloudflare_tunnel" ? tunnelGateways : directGateways;
  useEffect(() => {
    if (!application || !service) return;
    const preferredKind = publication?.kind === "public_direct" ? "public_direct" : cloudflareReady && tunnelGateways.length ? "cloudflare_tunnel" : "public_direct";
    const preferredGateways = preferredKind === "cloudflare_tunnel" ? tunnelGateways : directGateways;
    setKind(preferredKind);
    setGatewayID(publication?.gatewayNodeId ?? preferredGateways[0]?.id ?? "");
    setHostname(publication?.hostname ?? defaultPublicationHostname(data, service));
    setCommand(null); setBusy(false); setError("");
    if (!publication) return;
    let cancelled = false;
    void api.latestApplicationCommand(application.id, "3xui.subscription.configure").then((latest) => { if (!cancelled) setCommand(latest); }).catch(() => { /* A ready publication can outlive its completed command record. */ });
    return () => { cancelled = true; };
  }, [application?.id, service?.id, publication?.id]);
  useEffect(() => {
    if (!command || command.state === "failed" || command.state === "succeeded") return;
    let cancelled = false; let timer = 0;
    const poll = async () => {
      try {
        const next = await api.applicationCommand(command.id);
        if (cancelled) return;
        setCommand(next);
        if (next.state === "pending" || next.state === "running") timer = window.setTimeout(() => void poll(), 2500);
      } catch (pollError) {
        if (!cancelled) { setError(userError(language, pollError)); timer = window.setTimeout(() => void poll(), 2500); }
      }
    };
    timer = window.setTimeout(() => void poll(), 2500);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [command?.id, command?.state, language]);
  const selectKind = (next: "cloudflare_tunnel" | "public_direct") => {
    setKind(next);
    const nextGateways = next === "cloudflare_tunnel" ? tunnelGateways : directGateways;
    setGatewayID(nextGateways[0]?.id ?? "");
  };
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!application) return;
    setBusy(true); setError("");
    try {
      let created: ApplicationCommand | undefined;
      await mutate(async () => { created = await api.createSubscriptionCommand({ applicationId: application.id, gatewayNodeId: gatewayID, hostname, kind, dnsProvider: kind === "cloudflare_tunnel" || cloudflareReady ? "cloudflare" : "manual" }); }, copy(language, "公网订阅配置已开始。", "Public subscription setup started."));
      if (created) setCommand(created);
    } catch (submitError) {
      setError(userError(language, submitError));
    } finally {
      setBusy(false);
    }
  };
  const ready = publication?.status === "ready";
  const configured = command?.state === "succeeded";
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(application)}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, "公网订阅", "Public subscription")}</SheetTitle><SheetDescription>{copy(language, "Vastora 会发布独立订阅服务，并把公网域名自动写入 3x-ui。管理面板仍只在私网开放。", "Vastora publishes the separate subscription service and writes its public hostname into 3x-ui. The admin panel stays private.")}</SheetDescription></SheetHeader>{command || publication ? <div aria-live="polite" className="flex flex-1 flex-col gap-4 px-4"><Alert>{ready ? <Globe2Icon /> : command?.state === "failed" ? <ShieldAlertIcon /> : <Spinner />}<AlertTitle>{ready ? copy(language, "公网订阅已开启", "Public subscription is enabled") : command?.state === "failed" ? copy(language, "开启失败", "Could not enable subscription") : configured ? copy(language, "3x-ui 已配置，等待入口就绪", "3x-ui configured; waiting for access") : copy(language, "正在自动配置…", "Configuring automatically…")}</AlertTitle><AlertDescription>{ready ? copy(language, "通用订阅会自动识别 Clash/Mihomo 客户端，也可复制专用 OpenClash 地址。", "General subscriptions auto-detect Clash/Mihomo clients, and a dedicated OpenClash URL is also available.") : command?.state === "failed" ? command.error : configured ? copy(language, "域名和 HTTPS 正在生效；页面会自动更新。", "The hostname and HTTPS are coming online; this page updates automatically.") : copy(language, "正在创建 HTTPS 入口并同步 3x-ui 设置。", "Creating the HTTPS access point and syncing 3x-ui settings.")}</AlertDescription></Alert>{ready ? <div className="rounded-xl border bg-muted/40 p-4"><p className="text-sm font-medium">{copy(language, "支持两种订阅格式", "Two subscription formats are available")}</p><div className="mt-3 flex flex-wrap gap-2"><Badge variant="secondary">{copy(language, "通用订阅", "General")}</Badge><Badge variant="secondary">OpenClash / Mihomo</Badge></div><p className="mt-3 text-xs leading-5 text-muted-foreground">{copy(language, "关闭此页后，在“管理客户端”中为每台设备复制完整地址。不要复制只有域名的服务基址。", "Close this sheet, then copy the complete per-device URL from Manage clients. Do not copy the hostname-only service base.")}</p></div> : null}{command?.state === "failed" && command.error ? <FieldError>{userError(language, new Error(command.error))}</FieldError> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Alert><Globe2Icon /><AlertTitle>{copy(language, "推荐使用 Cloudflare 安全通道", "Cloudflare secure tunnel is recommended")}</AlertTitle><AlertDescription>{copy(language, "无需开放新的公网端口，并自动提供 HTTPS。", "It needs no new public port and provides HTTPS automatically.")}</AlertDescription></Alert><Field><FieldLabel htmlFor="subscription-hostname">{copy(language, "订阅域名", "Subscription hostname")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="subscription-hostname" onChange={(event) => setHostname(event.target.value.toLowerCase())} required spellCheck={false} value={hostname} /><FieldDescription>{copy(language, "只用于订阅下载，不会公开 3x-ui 管理页面。", "Used only for subscription downloads; it does not publish the 3x-ui admin page.")}</FieldDescription></Field><details className="rounded-xl border p-3"><summary className="cursor-pointer text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}</summary><div className="mt-4 flex flex-col gap-4"><Field><FieldLabel htmlFor="subscription-kind">{copy(language, "公网方式", "Public method")}</FieldLabel><NativeSelect id="subscription-kind" onChange={(event) => selectKind(event.target.value as "cloudflare_tunnel" | "public_direct")} value={kind}>{cloudflareReady && tunnelGateways.length ? <option value="cloudflare_tunnel">Cloudflare Tunnel · HTTPS</option> : null}<option disabled={!directGateways.length} value="public_direct">{copy(language, "公网网关 · HTTPS", "Public gateway · HTTPS")}</option></NativeSelect></Field><Field><FieldLabel htmlFor="subscription-gateway">{copy(language, "入口节点", "Entry node")}</FieldLabel><NativeSelect id="subscription-gateway" onChange={(event) => setGatewayID(event.target.value)} required value={gatewayID}><option disabled value="">{copy(language, "没有可用节点", "No node available")}</option>{gateways.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</NativeSelect></Field></div></details>{!cloudflareReady ? <FieldError>{copy(language, "请先连接 Cloudflare，才能自动管理订阅域名和 HTTPS。", "Connect Cloudflare first to manage the subscription hostname and HTTPS automatically.")}</FieldError> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !cloudflareReady || !gatewayID || !hostname} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <Globe2Icon data-icon="inline-start" />}{copy(language, "开启公网订阅", "Enable public subscription")}</Button></SheetFooter></form>}<SheetFooter>{command || publication ? <Button onClick={onClose}>{copy(language, "关闭", "Close")}</Button> : null}</SheetFooter></SheetContent></Sheet>;
}

function RealitySheet({ application, data, language, onClose }: { application: Application | null; data: AppData; language: Language; onClose: () => void }) {
  const [name, setName] = useState("");
  const [gatewayID, setGatewayID] = useState("");
  const [hostname, setHostname] = useState("");
  const [dnsProvider, setDNSProvider] = useState<"manual" | "cloudflare">("manual");
  const [target, setTarget] = useState("");
  const [sniHostname, setSNIHostname] = useState("");
  const [command, setCommand] = useState<ApplicationCommand | null>(null);
  const [shareURI, setShareURI] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const gateways = application ? data.agents.filter((agent) => agent.siteId === application.siteId && agent.connected && agent.capabilities.gateway && agent.networkProfile?.directPublic && agent.networkProfile.enabledKinds.includes("public") && data.sites.some((site) => site.id === application.siteId && site.gatewayNodes.includes(agent.id))) : [];
  const cloudflareReady = data.integrations.some((integration) => integration.kind === "cloudflare" && integration.status === "configured");
  useEffect(() => {
    if (!application) return;
    setName(copy(language, "我的设备", "My device"));
    setGatewayID(gateways[0]?.id ?? "");
    setHostname(defaultRealityHostname(data, application));
    setDNSProvider(cloudflareReady ? "cloudflare" : "manual");
    setTarget(""); setSNIHostname(""); setCommand(null); setShareURI(""); setBusy(false); setError("");
    let cancelled = false;
    void api.latestApplicationCommand(application.id, "3xui.reality.create").then((latest) => { if (!cancelled) { setCommand(latest); setGatewayID(latest.gatewayNodeId); setHostname(latest.hostname); setDNSProvider(latest.dnsProvider); } }).catch(() => { /* No resumable operation is the normal first-use state. */ });
    return () => { cancelled = true; };
  }, [application?.id]);
  useEffect(() => {
    if (!command || command.state === "failed" || command.state === "succeeded") return;
    let cancelled = false;
    let timer = 0;
    const poll = async () => {
      try {
        const next = await api.applicationCommand(command.id);
        if (cancelled) return;
        setCommand(next);
        if (next.state === "pending" || next.state === "running") timer = window.setTimeout(() => void poll(), 2500);
      } catch (pollError) {
        if (!cancelled) {
          setError(userError(language, pollError));
          timer = window.setTimeout(() => void poll(), 2500);
        }
      }
    };
    timer = window.setTimeout(() => void poll(), 2500);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [command?.id, command?.state, language]);
  const reveal = async () => {
    if (!command || command.state !== "succeeded" || !command.resultAvailable) return;
    setBusy(true); setError("");
    try {
      setShareURI((await api.revealApplicationCommand(command.id)).shareUri);
      setCommand({ ...command, resultAvailable: false });
    } catch (revealError) {
      setError(userError(language, revealError));
    } finally {
      setBusy(false);
    }
  };
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!application) return;
    setBusy(true); setError("");
    try {
      setCommand(await api.createRealityCommand({ applicationId: application.id, name, gatewayNodeId: gatewayID, hostname, dnsProvider, target: target || undefined, sniHostname: sniHostname || undefined }));
    } catch (submitError) {
      setError(userError(language, submitError));
    } finally {
      setBusy(false);
    }
  };
  const gateway = data.agents.find((agent) => agent.id === gatewayID);
  const manualRecordType = gateway?.networkProfile?.publicAddress?.includes(":") ? "AAAA" : "A";
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(application)}><SheetContent className="sm:max-w-xl"><SheetHeader><SheetTitle>{copy(language, "创建 VLESS REALITY", "Create VLESS REALITY")}</SheetTitle><SheetDescription>{command ? copy(language, "Vastora 正在节点内配置 3x-ui、共享 443 网关和 DNS。", "Vastora is configuring 3x-ui, the shared 443 gateway, and DNS on the node.") : copy(language, "只需命名一个客户端。目标站点、密钥、端口和共享 443 会自动配置。", "Name a client. The target, keys, port, and shared 443 access are configured automatically.")}</SheetDescription></SheetHeader>{command ? <div aria-live="polite" className="flex flex-1 flex-col gap-4 px-4"><Alert>{command.state === "pending" || command.state === "running" ? <Spinner /> : <RadioTowerIcon />}<AlertTitle>{command.state === "succeeded" ? copy(language, "REALITY 已创建", "REALITY is ready") : command.state === "failed" ? copy(language, "创建失败", "Creation failed") : copy(language, "正在自动配置…", "Configuring automatically…")}</AlertTitle><AlertDescription>{command.state === "pending" ? copy(language, "等待 Agent 接收任务。", "Waiting for the Agent to receive the task.") : command.state === "running" ? copy(language, "正在节点上扫描可用目标并创建入站。", "Scanning feasible targets and creating the inbound on the node.") : command.state === "succeeded" ? copy(language, `已使用 ${command.sniHostname ?? "已验证目标"}，客户端连接 ${command.hostname}:443。`, `Using ${command.sniHostname ?? "a verified target"}; clients connect to ${command.hostname}:443.`) : command.error}</AlertDescription></Alert>{command.state === "succeeded" && command.resultAvailable && !shareURI ? <Alert><KeyRoundIcon /><AlertTitle>{copy(language, "客户端链接只显示一次", "The client link is shown once")}</AlertTitle><AlertDescription><p>{copy(language, "准备好立即导入客户端后再显示；显示后 Center 会删除保存的副本。", "Reveal it only when you are ready to import it. Center deletes its saved copy afterward.")}</p><Button className="mt-3" disabled={busy} onClick={() => void reveal()} size="sm">{busy ? <Spinner data-icon="inline-start" /> : <KeyRoundIcon data-icon="inline-start" />}{copy(language, "显示一次性链接", "Reveal one-time link")}</Button></AlertDescription></Alert> : null}{shareURI ? <div><p className="mb-2 text-sm font-medium">{copy(language, "一次性客户端链接", "One-time client link")}</p><div className="relative"><code className="block max-h-48 overflow-auto break-all rounded-xl bg-muted p-4 pr-14 text-xs leading-6">{shareURI}</code><CopyButton className="absolute right-2 top-2" label={copy(language, "复制链接", "Copy link")} language={language} size="icon" value={shareURI} /></div><p className="mt-2 text-xs text-muted-foreground">{copy(language, "请立即导入客户端并保存；Center 已删除这份一次性链接。", "Import and save it now. Center has deleted its one-time copy.")}</p></div> : null}{command.state === "failed" && command.error ? <FieldError>{userError(language, new Error(command.error))}</FieldError> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}{dnsProvider === "manual" && gateway?.networkProfile?.publicAddress ? <Alert><Globe2Icon /><AlertTitle>{copy(language, "还需添加一条 DNS 记录", "One DNS record is still needed")}</AlertTitle><AlertDescription><code className="break-all">{manualRecordType} {hostname} → {gateway.networkProfile.publicAddress}</code></AlertDescription></Alert> : null}</div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="reality-client-name">{copy(language, "客户端名称", "Client name")}</FieldLabel><Input autoFocus id="reality-client-name" maxLength={64} onChange={(event) => setName(event.target.value)} required value={name} /><FieldDescription>{copy(language, "例如手机、MacBook 或家庭路由器。", "For example, Phone, MacBook, or Home router.")}</FieldDescription></Field><Field><FieldLabel htmlFor="reality-hostname">{copy(language, "连接域名", "Connection hostname")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="reality-hostname" onChange={(event) => setHostname(event.target.value.toLowerCase())} required spellCheck={false} value={hostname} /><FieldDescription>{copy(language, "按“reality.节点.位置.域名空间”自动生成。", "Generated as “reality.node.location.domain-namespace”.")}</FieldDescription></Field><Field><FieldLabel htmlFor="reality-gateway">{copy(language, "公网入口", "Public entry")}</FieldLabel><NativeSelect id="reality-gateway" onChange={(event) => setGatewayID(event.target.value)} required value={gatewayID}><option disabled value="">{copy(language, "没有可用的公网网关", "No public gateway available")}</option>{gateways.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</NativeSelect><FieldDescription>{copy(language, "3x-ui 入站运行在应用节点；所选网关统一占用公网 443 并按 SNI 分流。", "The 3x-ui inbound runs on its app node. This gateway owns public 443 and routes by SNI.")}</FieldDescription></Field><Field><FieldLabel htmlFor="reality-dns">DNS</FieldLabel><NativeSelect id="reality-dns" onChange={(event) => setDNSProvider(event.target.value as "manual" | "cloudflare")} value={dnsProvider}><option value="manual">{copy(language, "手动添加 A/AAAA", "Add A/AAAA manually")}</option>{cloudflareReady ? <option value="cloudflare">{copy(language, "Cloudflare 自动管理", "Manage with Cloudflare")}</option> : null}</NativeSelect></Field><details className="rounded-xl border p-3"><summary className="cursor-pointer text-sm font-medium">{copy(language, "高级：自定义伪装目标", "Advanced: custom camouflage target")}</summary><div className="mt-4 flex flex-col gap-4"><Field><FieldLabel htmlFor="reality-target">Target</FieldLabel><Input id="reality-target" onChange={(event) => setTarget(event.target.value.toLowerCase())} placeholder="www.example.com:443" value={target} /></Field><Field><FieldLabel htmlFor="reality-sni">SNI</FieldLabel><Input id="reality-sni" onChange={(event) => setSNIHostname(event.target.value.toLowerCase())} placeholder="www.example.com" value={sniHostname} /><FieldDescription>{copy(language, "留空时由应用节点实时扫描并选择可行目标；自定义时两项必须一起填写。", "Leave both empty for a live node-local scan. Custom values must be provided together.")}</FieldDescription></Field></div></details>{gateways.length === 0 ? <FieldError>{copy(language, "此位置还没有已确认公网能力的网关。请先在“网络”中确认公网地址并允许直接公网。", "This location has no gateway with confirmed public ingress. Confirm its public address and direct-public permission in Network first.")}</FieldError> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !name || !gatewayID || !hostname || Boolean(target) !== Boolean(sniHostname)} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <RadioTowerIcon data-icon="inline-start" />}{copy(language, "自动创建", "Create automatically")}</Button></SheetFooter></form>}<SheetFooter>{command ? <Button onClick={onClose}>{copy(language, shareURI ? "完成" : "关闭", shareURI ? "Done" : "Close")}</Button> : null}</SheetFooter></SheetContent></Sheet>;
}

function UninstallSheet({ application, app, language, onClose, onSubmit }: { application: Application | null; app?: AppView; language: Language; onClose: () => void; onSubmit: (application: Application, deleteData: boolean) => Promise<void> }) {
  const [deleteData, setDeleteData] = useState(false); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  useEffect(() => { if (application) { setDeleteData(false); setError(""); } }, [application]);
  const submit = async () => { if (!application) return; setBusy(true); setError(""); try { await onSubmit(application, deleteData); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(application)}><SheetContent><SheetHeader><SheetTitle>{copy(language, `卸载 ${app ? localized(app, language, "name") : application?.name ?? ""}`, `Uninstall ${app ? localized(app, language, "name") : application?.name ?? ""}`)}</SheetTitle><SheetDescription>{copy(language, "卸载会停止应用并移除所有访问入口。默认保留持久数据，便于以后重新安装。", "Uninstalling stops the app and removes all access points. Persistent data is kept by default for a later reinstall.")}</SheetDescription></SheetHeader><div className="px-4"><Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="delete-data">{copy(language, "同时永久删除应用数据", "Permanently delete app data too")}</FieldLabel><FieldDescription>{copy(language, "此操作不可恢复，包括配置、账号和历史数据。", "This cannot be undone and includes configuration, accounts, and history.")}</FieldDescription></div><Switch checked={deleteData} id="delete-data" onCheckedChange={setDeleteData} /></Field>{deleteData ? <Alert className="mt-4" variant="destructive"><Trash2Icon /><AlertTitle>{copy(language, "应用数据将永久删除", "App data will be permanently deleted")}</AlertTitle></Alert> : null}{error ? <FieldError className="mt-3">{error}</FieldError> : null}</div><SheetFooter><Button onClick={onClose} variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} onClick={() => void submit()} variant={deleteData ? "destructive" : "default"}>{busy ? <Spinner data-icon="inline-start" /> : null}{deleteData ? copy(language, "卸载并删除数据", "Uninstall and delete data") : copy(language, "卸载并保留数据", "Uninstall and keep data")}</Button></SheetFooter></SheetContent></Sheet>;
}
