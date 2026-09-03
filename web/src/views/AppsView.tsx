import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { AppWindowIcon, ArrowRightLeftIcon, ArrowUpCircleIcon, CheckCircle2Icon, ExternalLinkIcon, EyeIcon, EyeOffIcon, Globe2Icon, KeyRoundIcon, PackagePlusIcon, PencilIcon, RadioTowerIcon, RefreshCwIcon, RotateCcwIcon, Settings2Icon, ShieldAlertIcon, ShieldCheckIcon, Trash2Icon, UsersIcon } from "lucide-react";
import { api } from "../api";
import type { AppData, Mutate } from "../App";
import type { AgentView, Application, ApplicationCommand, ApplicationCredentialRotation, ApplicationCredentials, AppView, CreatePublicationInput, Deployment, Publication, PublicationIngressInput, PublicationKind, Service, ThreeXUIRole } from "../types";
import type { Language } from "../translations";
import { canInstall, defaultPublicationHostname, gatewaysForKind, installBlocker, isActiveApplication, isInstalledApplication, latestOperations, localized, operationLabel, publicationIntentOptions, publicationKindLabel, publicationKindsForIntent, publicationOptions, type PublicationIntent } from "./appAccess";
import { CopyButton, HighPrivilegeBadge, PageHeading, StateBadge, TechnicalError, copy, formatDate, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SelectControl } from "@/components/SelectControl";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { ThreeXUIClientsSheet } from "./ThreeXUIClientsSheet";
import { ThreeXUIControllerMigrationSheet } from "./ThreeXUIControllerMigrationSheet";
import { ThreeXUIInboundTrafficSheet } from "./ThreeXUIInboundTrafficSheet";
import { RealitySheet } from "./RealitySheet";
import { RegionCombobox, regionBaseName, regionDisplayName } from "./RegionCombobox";
import { useApplicationCommandExecutor } from "../hooks/use-application-command-executor";
import { clearSecretOperation, deploymentSecretScope, readSecretOperation, secretOperation } from "../secret-delivery";
import { administratorPasswordMinLength } from "../lib/security";
import { normalizeHostname, validHostname } from "../lib/network";
import { InstalledApps } from "./InstalledApps";
import { canCreateRealityNode, installedAppGroups, type InstalledAppInstance } from "./installed-apps-model";

type DeploymentEditor = { app: AppView; agent?: AgentView; operation: "install" | "upgrade" | "configure" } | null;
type CredentialDelivery = NonNullable<Deployment["oneTimeCredentials"]> & { deploymentId: string; operationKey: string; scope: string };

function AppSectionCount({ active, value }: { active: boolean; value: number }) {
  return <span className={active ? "inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-primary-foreground/10 px-1.5 text-xs font-semibold text-primary-foreground tabular-nums ring-1 ring-primary-foreground/20 ring-inset" : "inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-secondary px-1.5 text-xs font-semibold text-secondary-foreground tabular-nums ring-1 ring-border ring-inset"} data-active={active} data-slot="app-section-count">{value}</span>;
}

export function AppsView({ data, language, mutate }: { data: AppData; language: Language; mutate: Mutate }) {
  const [deploymentEditor, setDeploymentEditor] = useState<DeploymentEditor>(null);
  const [publicationService, setPublicationService] = useState<Service | null>(null);
  const [uninstallApplication, setUninstallApplication] = useState<Application | null>(null);
	const [credentials, setCredentials] = useState<CredentialDelivery | undefined>(undefined);
	const [credentialAckBusy, setCredentialAckBusy] = useState(false);
	const credentialRecovery = useRef("");
	const [credentialApplication, setCredentialApplication] = useState<Application | null>(null);
	const [realityApplication, setRealityApplication] = useState<Application | null>(null);
	const [realityRenameService, setRealityRenameService] = useState<Service | null>(null);
  const [trafficService, setTrafficService] = useState<Service | null>(null);
  const [subscriptionApplication, setSubscriptionApplication] = useState<Application | null>(null);
  const [clientsApplication, setClientsApplication] = useState<Application | null>(null);
  const [migrationApplication, setMigrationApplication] = useState<Application | null>(null);
	const [managedApplicationID, setManagedApplicationID] = useState<string | null>(null);
	const [recoveringTask, setRecoveringTask] = useState("");
  const [section, setSection] = useState<"installed" | "store">(() => data.applications.some(isInstalledApplication) ? "installed" : "store");
  const catalogByKey = useMemo(() => new Map(data.apps.map((app) => [app.key, app])), [data.apps]);
  const installedApplications = data.applications.filter(isInstalledApplication);
  const installedGroups = useMemo(() => installedAppGroups({
    applications: data.applications, apps: data.apps, agents: data.agents, sites: data.sites,
    services: data.services, publications: data.publications, deployments: data.deployments,
    threeXUIControllerMigrations: data.threeXUIControllerMigrations,
  }), [data.applications, data.apps, data.agents, data.sites, data.services, data.publications, data.deployments, data.threeXUIControllerMigrations]);
  const managedInstance = installedGroups.flatMap((group) => group.instances).find((instance) => instance.application.id === managedApplicationID);
  const recentOperations = latestOperations(data.deployments).filter((deployment) => deployment.state === "pending" || deployment.state === "running" || deployment.state === "failed");
  const trafficApplication = trafficService ? data.applications.find((application) => application.id === trafficService.applicationId) : undefined;
  const trafficController = data.applications.find((application) => application.id === trafficApplication?.controllerApplicationId && application.role === "master" && application.status === "running");

  const openChange = (application: Application, operation: "upgrade" | "configure") => {
    const app = catalogByKey.get(application.appKey);
    const agent = data.agents.find((value) => value.id === application.nodeId);
    if (app && agent) setDeploymentEditor({ app, agent, operation });
  };

  const openFromDetails = (action: () => void) => {
    setManagedApplicationID(null);
    action();
  };

	useEffect(() => {
		if (credentials) return;
		for (const deployment of data.deployments) {
			if (!deployment.oneTimeCredentialsAvailable) continue;
			const scope = deploymentSecretScope(deployment.agentId, deployment.appKey, deployment.operation);
			const operationKey = readSecretOperation(scope);
			if (!operationKey) continue;
			const recoveryKey = `${deployment.id}:${operationKey}`;
			if (credentialRecovery.current === recoveryKey) return;
			credentialRecovery.current = recoveryKey;
			void api.revealDeploymentCredentials(deployment.id, operationKey).then((value) => {
				setCredentials({ ...value, deploymentId: deployment.id, operationKey, scope });
			}).catch(() => { /* The next screen refresh may retry the same durable delivery. */ }).finally(() => {
				if (credentialRecovery.current === recoveryKey) credentialRecovery.current = "";
			});
			return;
		}
	}, [credentials, data.deployments]);

	const acknowledgeCredentials = async () => {
		if (!credentials) return;
		setCredentialAckBusy(true);
		try {
			await mutate(() => api.acknowledgeDeploymentCredentials(credentials.deploymentId, credentials.operationKey), copy(language, "3x-ui 管理账号已确认保存。", "3x-ui administrator credentials were acknowledged."));
			clearSecretOperation(credentials.scope);
			credentialRecovery.current = "";
			setCredentials(undefined);
		} finally {
			setCredentialAckBusy(false);
		}
	};

  return (
    <section className="flex flex-col gap-7">
      <PageHeading title={copy(language, "应用", "Apps")} description={copy(language, "管理应用、订阅与各节点的访问入口。", "Manage apps, subscriptions, and access on every node.")} />

      {credentials ? <Alert><KeyRoundIcon /><AlertTitle>{copy(language, "请保存 3x-ui 管理账号", "Save the 3x-ui administrator account")}</AlertTitle><AlertDescription><p>{copy(language, "确认保存前可在断线或刷新后重新领取；确认后 Center 会永久关闭再次显示。", "Until you acknowledge it, the same browser can recover these credentials after a disconnect or refresh. Acknowledgement permanently disables disclosure.")}</p><dl className="mt-3 grid gap-2 rounded-lg bg-muted p-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">{copy(language, "账号", "Username")}</dt><dd className="mt-1 flex items-center gap-2 font-mono">{credentials.username}<CopyButton language={language} value={credentials.username} /></dd></div><div><dt className="text-muted-foreground">{copy(language, "密码", "Password")}</dt><dd className="mt-1 flex items-center gap-2 break-all font-mono">{credentials.password}<CopyButton language={language} value={credentials.password} /></dd></div></dl><Button className="mt-3" disabled={credentialAckBusy} onClick={() => void acknowledgeCredentials()} size="sm" variant="outline">{credentialAckBusy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "我已保存并关闭再次显示", "Saved — disable further disclosure")}</Button></AlertDescription></Alert> : null}

      {recentOperations.length ? <div aria-live="polite" className="flex flex-col gap-3"><div><h2 className="text-lg font-semibold">{copy(language, "最近操作", "Recent operations")}</h2><p className="mt-1 text-sm text-muted-foreground">{copy(language, "页面会自动更新，无需手动刷新。", "This page updates automatically; no manual refresh is needed.")}</p></div>{recentOperations.map((deployment) => {
        const app = catalogByKey.get(deployment.appKey); const agent = data.agents.find((value) => value.id === deployment.agentId); const application = data.applications.find((value) => value.id === deployment.applicationId);
        const retry = () => { if (!app || !agent) return; if (deployment.operation === "uninstall" && application) setUninstallApplication(application); else if (deployment.operation === "upgrade" || deployment.operation === "configure") setDeploymentEditor({ app, agent, operation: deployment.operation }); else setDeploymentEditor({ app, operation: "install" }); };
	        const recover = async () => {
	          setRecoveringTask(deployment.id);
	          try {
	            await mutate(() => api.retryTaskReconciliation(deployment.id), copy(language, "恢复任务已重新排队。", "The recovery task was queued again."));
	          } finally {
	            setRecoveringTask("");
	          }
	        };
	        return <Card key={deployment.id} size="sm"><CardContent className="flex flex-col gap-3 py-4 sm:flex-row sm:items-center"><StateBadge language={language} value={deployment.reconciliationRequired ? "recovery" : deployment.state} /><div className="min-w-0 flex-1"><p className="font-medium">{operationLabel(language, deployment.operation)} · {app ? localized(app, language, "name") : deployment.appKey}</p><p className="mt-1 text-xs text-muted-foreground">{agent?.name ?? deployment.agentId}</p>{deployment.reconciliationRequired ? <p className="mt-2 text-sm text-amber-700 dark:text-amber-300">{copy(language, "节点状态未能完全确认。系统已锁定这项应用，继续恢复会复用原任务，不会重复安装。", "The node state could not be fully confirmed. This app is locked; continuing recovery reuses the original task and will not install a duplicate.")}</p> : null}{deployment.error ? <div className="mt-2"><TechnicalError error={deployment.error} language={language} /></div> : null}</div>{deployment.reconciliationRequired ? <Button disabled={recoveringTask === deployment.id} onClick={() => void recover()} size="sm" variant="outline">{recoveringTask === deployment.id ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{copy(language, "继续恢复", "Continue recovery")}</Button> : deployment.state === "failed" && app && agent ? <Button onClick={retry} size="sm" variant="outline"><RotateCcwIcon data-icon="inline-start" />{copy(language, "重试", "Retry")}</Button> : null}</CardContent></Card>;
      })}</div> : null}

      <div aria-label={copy(language, "应用内容", "App content")} className="inline-flex w-fit rounded-xl bg-muted p-1" role="group">
        <Button aria-pressed={section === "installed"} onClick={() => setSection("installed")} size="sm" type="button" variant={section === "installed" ? "default" : "ghost"}>{copy(language, "已安装", "Installed")}<AppSectionCount active={section === "installed"} value={installedApplications.length} /></Button>
        <Button aria-pressed={section === "store"} onClick={() => setSection("store")} size="sm" type="button" variant={section === "store" ? "default" : "ghost"}>{copy(language, "应用商店", "App Store")}<AppSectionCount active={section === "store"} value={data.apps.length} /></Button>
      </div>

      {section === "installed" ? <div className="flex flex-col gap-4">
	        {installedApplications.length === 0 ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><AppWindowIcon /></EmptyMedia><EmptyTitle>{copy(language, "还没有安装应用", "No apps installed yet")}</EmptyTitle><EmptyDescription>{copy(language, "从应用商店选择一个应用开始；失败任务只保留在活动记录中。", "Choose an app from the store to get started. Failed tasks remain only in Activity.")}</EmptyDescription><Button className="mt-3" onClick={() => setSection("store")} size="sm">{copy(language, "打开应用商店", "Open App Store")}</Button></EmptyHeader></Empty> : <InstalledApps groups={installedGroups} language={language} mutate={mutate} onClients={setClientsApplication} onManage={(application) => setManagedApplicationID(application.id)} onReality={setRealityApplication} />}
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

      <Sheet onOpenChange={(open) => { if (!open) setManagedApplicationID(null); }} open={Boolean(managedInstance)}>
        {managedInstance ? <InstalledAppDetails
          data={data}
          instance={managedInstance}
          key={managedInstance.application.id}
          language={language}
          mutate={mutate}
          onClients={() => openFromDetails(() => setClientsApplication(managedInstance.application))}
          onConfigure={() => openFromDetails(() => openChange(managedInstance.application, "configure"))}
          onCredentials={() => openFromDetails(() => setCredentialApplication(managedInstance.application))}
          onMigrate={() => openFromDetails(() => setMigrationApplication(managedInstance.application))}
          onPublish={(service) => openFromDetails(() => setPublicationService(service))}
          onReality={() => openFromDetails(() => setRealityApplication(managedInstance.application))}
          onRenameReality={(service) => openFromDetails(() => setRealityRenameService(service))}
          onSubscription={() => openFromDetails(() => setSubscriptionApplication(managedInstance.application))}
          onTraffic={(service) => openFromDetails(() => setTrafficService(service))}
          onUninstall={() => openFromDetails(() => setUninstallApplication(managedInstance.application))}
          onUpgrade={() => openFromDetails(() => openChange(managedInstance.application, "upgrade"))}
        /> : null}
      </Sheet>
      <DeploymentSheet data={data} editor={deploymentEditor} language={language} onClose={() => setDeploymentEditor(null)} onSubmit={async (agent, app, config, operation, role, registryCredentialId) => {
        let result: Deployment | undefined;
		const scope = deploymentSecretScope(agent.id, app.key, operation);
		const operationKey = app.key === "vastora-official/3x-ui" && operation === "install" && role !== "worker" ? secretOperation(scope) : undefined;
        const messages = { install: copy(language, "安装任务已创建。可在活动中查看进度。", "Install task created. Follow progress in Activity."), upgrade: copy(language, "升级任务已创建。", "Upgrade task created."), configure: copy(language, "配置任务已创建。", "Configuration task created.") };
        await mutate(async () => { result = await api.createDeployment(agent.id, app.key, config, operation, false, role, registryCredentialId, operationKey); }, messages[operation]);
        if (result?.oneTimeCredentials && operationKey) setCredentials({ ...result.oneTimeCredentials, deploymentId: result.id, operationKey, scope });
        setDeploymentEditor(null);
      }} />
      <PublicationSheet data={data} language={language} onClose={() => setPublicationService(null)} onSubmit={async (input) => { await mutate(() => api.createPublication(input), copy(language, "访问入口已创建。", "Access point created."), { reportError: false }); setPublicationService(null); }} service={publicationService} />
		<RealitySheet application={realityApplication} data={data} language={language} onClose={() => setRealityApplication(null)} siteTimezone={realityApplication ? data.sites.find((site) => site.id === realityApplication.siteId)?.timezone : undefined} />
		<RealityRenameSheet data={data} language={language} mutate={mutate} onClose={() => setRealityRenameService(null)} service={realityRenameService} />
      <ThreeXUIInboundTrafficSheet controller={trafficController ?? null} language={language} onClose={() => setTrafficService(null)} service={trafficService} siteTimezone={trafficService ? data.sites.find((site) => site.id === trafficService.siteId)?.timezone : undefined} />
      <SubscriptionSheet application={subscriptionApplication} data={data} language={language} mutate={mutate} onClose={() => setSubscriptionApplication(null)} />
	      <ThreeXUIClientsSheet advancedURL={clientsApplication ? data.deployments.find((value) => value.applicationId === clientsApplication.id && value.state === "succeeded" && value.operation !== "uninstall")?.accessUrl : undefined} application={clientsApplication} language={language} onClose={() => setClientsApplication(null)} siteTimezone={clientsApplication ? data.sites.find((site) => site.id === clientsApplication.siteId)?.timezone : undefined} />
	      <ApplicationCredentialsSheet application={credentialApplication} language={language} onClose={() => setCredentialApplication(null)} />
	      <ThreeXUIControllerMigrationSheet application={migrationApplication} data={data} language={language} mutate={mutate} onClose={() => setMigrationApplication(null)} />
      <UninstallSheet application={uninstallApplication} app={uninstallApplication ? catalogByKey.get(uninstallApplication.appKey) : undefined} language={language} onClose={() => setUninstallApplication(null)} onSubmit={async (application, deleteData) => { await mutate(() => api.createDeployment(application.nodeId, application.appKey, {}, "uninstall", deleteData), copy(language, "卸载任务已创建。", "Uninstall task created.")); setUninstallApplication(null); }} />
    </section>
  );
}

function InstalledAppDetails({ instance, data, language, onClients, onConfigure, onCredentials, onMigrate, onPublish, onReality, onRenameReality, onSubscription, onTraffic, onUninstall, onUpgrade, mutate }: { instance: InstalledAppInstance; data: AppData; language: Language; onClients: () => void; onConfigure: () => void; onCredentials: () => void; onMigrate: () => void; onPublish: (service: Service) => void; onReality: () => void; onRenameReality: (service: Service) => void; onSubscription: () => void; onTraffic: (service: Service) => void; onUninstall: () => void; onUpgrade: () => void; mutate: Mutate }) {
  const [syncingNode, setSyncingNode] = useState(false);
  const { application, app, agent, services, deployment, activeChange, locked: serviceAccessLocked } = instance;
  const subscriptionService = services.find((service) => service.name === "subscription");
  const subscriptionPublication = subscriptionService ? data.publications.find((value) => value.serviceId === subscriptionService.id && value.status !== "stopped" && (value.kind === "cloudflare_tunnel" || value.kind === "public_direct")) : undefined;
  const isThreeXUI = application.appKey === "vastora-official/3x-ui";
	const isCPA = application.appKey === "vastora-official/cpa";
  const isController = isThreeXUI && application.role === "master" && application.id === application.controllerApplicationId;
  const isLegacyController = isThreeXUI && application.role === "master" && Boolean(application.controllerApplicationId) && application.id !== application.controllerApplicationId;
  const isWorker = isThreeXUI && application.role === "worker";
  const nodeSyncing = application.nodeSyncStatus === "pending" || application.nodeSyncStatus === "applying";
  const nodeReady = application.nodeSyncStatus === "ready";
  const visibleServices = isWorker || isLegacyController ? services.filter((service) => service.protocol !== "http" && service.protocol !== "https") : services;
  const activeWorkers = isController ? data.applications.filter((value) => value.id !== application.id && value.role === "worker" && value.controllerApplicationId === application.id && (value.status !== "stopped" || value.nodeSyncStatus === "pending" || value.nodeSyncStatus === "applying")) : [];
  const managedApplicationIDs = new Set([application.id, ...activeWorkers.map((worker) => worker.id)]);
  const managedVLESSNodeCount = isController ? data.services.filter((service) => managedApplicationIDs.has(service.applicationId) && service.appProtocol === "vless/tcp/reality" && service.status !== "stopped").length : 0;
  const hasVLESSNode = services.some((service) => service.appProtocol === "vless/tcp/reality");
  const retryNodeSync = async () => {
    setSyncingNode(true);
    try {
      await mutate(() => api.reconcileThreeXUINode(application.id), copy(language, "正在重新连接订阅主机。", "Reconnecting to the subscription controller."));
    } finally {
      setSyncingNode(false);
    }
  };
  return <SheetContent className="data-[side=right]:w-full data-[side=right]:sm:max-w-xl">
    <SheetHeader className="pr-12">
      <SheetTitle className="flex flex-wrap items-center gap-2">{app ? localized(app, language, "name") : application.name}{app?.app.hostAccess ? <HighPrivilegeBadge language={language} /> : null}{isController ? <Badge>{copy(language, "全局订阅主机", "Global subscription controller")}</Badge> : null}{isLegacyController ? <Badge variant="outline">{copy(language, "待转为节点", "Converting to node")}</Badge> : null}{isWorker ? <Badge variant="outline">{copy(language, "VLESS 节点", "VLESS node")}</Badge> : null}</SheetTitle>
      <SheetDescription>{agent?.name ?? application.nodeId} · {application.runtime}{application.installedVersion ? ` · v${application.installedVersion}` : ""}</SheetDescription>
      <div className="mt-2"><StateBadge language={language} value={application.status} /></div>
    </SheetHeader>
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 pb-4">
      {activeChange ? <Alert>{activeChange.reconciliationRequired ? <ShieldAlertIcon /> : <Spinner />}<AlertTitle>{activeChange.reconciliationRequired ? copy(language, "需要继续恢复", "Recovery required") : copy(language, `正在${operationLabel(language, activeChange.operation)}`, `${operationLabel(language, activeChange.operation)} in progress`)}</AlertTitle><AlertDescription>{activeChange.reconciliationRequired ? copy(language, "应用状态尚未确认，恢复完成前不能修改或发布服务；已有入口仍可停止。", "The app state is not confirmed. Services cannot be changed or published until recovery finishes; existing access points can still be stopped.") : copy(language, "完成前暂时不能发起其他应用变更。", "Other app changes are unavailable until this finishes.")}</AlertDescription></Alert> : null}
      {application.status === "failed" ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "最近一次操作失败，应用仍保留", "The last operation failed; the app is still installed")}</AlertTitle><AlertDescription>{copy(language, "原有安装记录和数据仍保留；请查看最近操作，修正后重试或卸载。", "The installed record and data remain available. Review the recent operation, then retry or uninstall.")}</AlertDescription></Alert> : null}
      {isController ? <p aria-atomic="true" className="text-sm text-muted-foreground" role="status">{managedVLESSNodeCount > 0 ? copy(language, `当前订阅包含 ${managedVLESSNodeCount} 个 VLESS 节点，由此订阅主机统一管理。`, `The current subscription includes ${managedVLESSNodeCount} VLESS node(s), managed by this subscription controller.`) : copy(language, "尚未创建 VLESS 节点；创建后会自动加入统一订阅。", "No VLESS nodes yet. New nodes are added to the shared subscription automatically.")}</p> : null}
      {isController || isLegacyController ? <p className="text-xs text-muted-foreground">{application.restorePointState === "ready" && application.restorePointAt ? copy(language, `恢复点已保存 · ${new Date(application.restorePointAt).toLocaleString()}`, `Restore point saved · ${new Date(application.restorePointAt).toLocaleString()}`) : application.restorePointState === "pending" ? copy(language, "正在保存恢复点…", "Saving a restore point…") : copy(language, "恢复点将在节点在线后自动保存。", "A restore point will be saved automatically when the node is online.")}</p> : null}
      {isWorker && nodeSyncing ? <Alert aria-live="polite"><Spinner /><AlertTitle>{copy(language, "正在接入订阅主机", "Connecting to the subscription controller")}</AlertTitle><AlertDescription>{copy(language, "连接完成后即可在此节点一键创建 VLESS。", "Once connected, you can create VLESS on this node.")}</AlertDescription></Alert> : null}
      {isWorker && (application.nodeSyncStatus === "failed" || !application.nodeSyncStatus) ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "尚未接入订阅主机", "Not connected to the subscription controller")}</AlertTitle><AlertDescription><p>{application.nodeSyncError || copy(language, "升级来的旧节点需要重新连接；新节点请确认两台主机能通过 Headscale 或私网互相访问。", "An upgraded existing node must reconnect. For a new node, confirm both hosts can reach each other over Headscale or the private network.")}</p><Button className="mt-3" disabled={syncingNode} onClick={() => void retryNodeSync()} size="sm" variant="outline">{syncingNode ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{copy(language, "重新连接", "Reconnect")}</Button></AlertDescription></Alert> : null}
      {isWorker && nodeReady ? <p className="text-sm text-muted-foreground">{copy(language, "此节点只承载 VLESS；客户端和订阅由全局订阅主机统一管理。", "This node only carries VLESS. Clients and subscriptions are managed by the global controller.")}</p> : null}
      {isLegacyController ? <Alert><Spinner /><AlertTitle>{copy(language, "正在并入全局订阅", "Joining the global subscription")}</AlertTitle><AlertDescription>{copy(language, "系统会先保存恢复点，再把这台旧订阅主机直接转成普通 VLESS 节点。", "The system saves a restore point, then converts this legacy controller directly into a regular VLESS node.")}</AlertDescription></Alert> : null}
	      {isThreeXUI ? <div className={`grid gap-2 ${isController ? "sm:grid-cols-2" : "sm:grid-cols-1"}`}>
	        {isController ? <Button disabled={serviceAccessLocked} onClick={onClients} size="sm"><UsersIcon data-icon="inline-start" />{copy(language, "管理客户端", "Manage clients")}</Button> : null}
	        {isController ? <Button onClick={onCredentials} size="sm" variant="outline"><KeyRoundIcon data-icon="inline-start" />{copy(language, "管理账号", "Admin credentials")}</Button> : null}
	        {!hasVLESSNode ? <Button disabled={!canCreateRealityNode(instance) || syncingNode} onClick={onReality} size="sm" variant={isController ? "outline" : "default"}><RadioTowerIcon data-icon="inline-start" />{copy(language, "创建 VLESS", "Create VLESS")}</Button> : null}
        {isController && subscriptionService ? <Button disabled={serviceAccessLocked} onClick={onSubscription} size="sm" variant="outline"><Globe2Icon data-icon="inline-start" />{subscriptionPublication ? copy(language, "公网订阅", "Public subscription") : copy(language, "开启订阅", "Enable subscription")}</Button> : null}
      </div> : null}
			{isCPA ? <Button disabled={Boolean(activeChange)} onClick={onCredentials} size="sm" variant="outline"><KeyRoundIcon data-icon="inline-start" />{copy(language, "凭据", "Credentials")}</Button> : null}
      {!isWorker && !isLegacyController && deployment?.accessUrl ? <Button nativeButton={false} render={<a href={deployment.accessUrl} rel="noreferrer" target="_blank" />} size="sm" variant="outline"><ExternalLinkIcon data-icon="inline-start" />{copy(language, "打开主页", "Open homepage")}</Button> : !isWorker && !isLegacyController && app?.app.homepage ? <p className="text-xs text-muted-foreground">{copy(language, "添加并完成一个访问入口后，这里会出现“打开主页”。", "After an access point is ready, an Open homepage button appears here.")}</p> : null}
      {!isWorker && !isLegacyController && visibleServices.length === 0 ? <p className="text-sm text-muted-foreground">{copy(language, "此应用没有可发布的 Web 服务。", "This app has no publishable Web service.")}</p> : null}
			{visibleServices.map((service) => <ServiceRow data={data} key={service.id} language={language} locked={serviceAccessLocked} onPublish={() => onPublish(service)} onRename={() => onRenameReality(service)} onTraffic={() => onTraffic(service)} service={service} mutate={mutate} />)}
      {isController && activeWorkers.length > 0 ? <p className="text-xs text-muted-foreground">{copy(language, "移除所有 VLESS 节点后才能卸载订阅主机。", "Remove all VLESS nodes before uninstalling the subscription controller.")}</p> : null}
    </div>
    <SheetFooter className="flex-row flex-wrap justify-end gap-2 border-t">{isLegacyController ? <Button disabled={Boolean(activeChange) || application.restorePointState === "pending"} onClick={onMigrate} size="sm" variant="outline"><ArrowRightLeftIcon data-icon="inline-start" />{copy(language, "查看转换进度", "View conversion")}</Button> : isController && activeWorkers.some((worker) => worker.nodeSyncStatus === "ready") ? <Button disabled={Boolean(activeChange) || application.restorePointState === "pending"} onClick={onMigrate} size="sm" variant="outline"><ArrowRightLeftIcon data-icon="inline-start" />{copy(language, "迁移订阅主机", "Move subscription host")}</Button> : null}{application.updateAvailable ? <Button disabled={Boolean(activeChange) || isLegacyController} onClick={onUpgrade} size="sm"><ArrowUpCircleIcon data-icon="inline-start" />{copy(language, `升级到 v${application.availableVersion}`, `Upgrade to v${application.availableVersion}`)}</Button> : app ? <Badge variant="secondary">{copy(language, "版本已是最新", "Version up to date")}</Badge> : null}{app && app.app.config.length > 0 && !application.updateAvailable ? <Button disabled={Boolean(activeChange) || isLegacyController} onClick={onConfigure} size="sm" variant="outline"><Settings2Icon data-icon="inline-start" />{copy(language, "修改配置", "Change settings")}</Button> : null}<Button disabled={Boolean(activeChange) || isLegacyController || activeWorkers.length > 0} onClick={onUninstall} size="sm" variant="ghost"><Trash2Icon data-icon="inline-start" />{copy(language, "卸载", "Uninstall")}</Button></SheetFooter>
	  </SheetContent>;
}

function ApplicationCredentialsSheet({ application, language, onClose }: { application: Application | null; language: Language; onClose: () => void }) {
	const [currentPassword, setCurrentPassword] = useState("");
	const [credentials, setCredentials] = useState<ApplicationCredentials | null>(null);
	const [visibleSecrets, setVisibleSecrets] = useState<Set<string>>(() => new Set());
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState("");
	const [rotationTarget, setRotationTarget] = useState<"management" | "client" | null>(null);
	const [rotationConfirmed, setRotationConfirmed] = useState(false);
	const [rotation, setRotation] = useState<ApplicationCredentialRotation | null>(null);

	useEffect(() => {
		setCurrentPassword("");
		setCredentials(null);
		setVisibleSecrets(new Set());
		setBusy(false);
		setError("");
		setRotationTarget(null);
		setRotationConfirmed(false);
		setRotation(null);
	}, [application?.id]);

	useEffect(() => {
		const applicationID = application?.id;
		const rotationID = rotation?.id;
		const rotationState = rotation?.state;
		if (!applicationID || !rotationID || rotationState !== "preparing" && rotationState !== "pending") return;
		const controller = new AbortController();
		let timer = 0;
		const poll = async () => {
			try {
				const current = await api.applicationCredentialRotation(applicationID, rotationID, controller.signal);
				setRotation(current);
				if (current.state === "succeeded") {
					clearSecretOperation(`application-credential-rotation:${applicationID}:${current.target}`);
					return;
				}
				if (current.state === "preparing" || current.state === "pending") timer = window.setTimeout(() => void poll(), 1000);
			} catch (pollError) {
				if (!controller.signal.aborted) setError(userError(language, pollError));
			}
		};
		timer = window.setTimeout(() => void poll(), 1000);
		return () => {
			controller.abort();
			window.clearTimeout(timer);
		};
	}, [application?.id, language, rotation?.id, rotation?.state]);

	const close = () => {
		setCurrentPassword("");
		setCredentials(null);
		setVisibleSecrets(new Set());
		setError("");
		setRotationTarget(null);
		setRotationConfirmed(false);
		setRotation(null);
		onClose();
	};
	const toggleSecret = (name: string) => setVisibleSecrets((current) => {
		const next = new Set(current);
		if (next.has(name)) next.delete(name); else next.add(name);
		return next;
	});
	const reveal = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		if (!application || busy) return;
		const reauthentication = currentPassword;
		setCurrentPassword("");
		setBusy(true);
		setError("");
		try {
			setCredentials(await api.revealApplicationCredentials(application.id, reauthentication));
		} catch (revealError) {
			setError(userError(language, revealError));
		} finally {
			setBusy(false);
		}
	};
	const beginRotation = (target: "management" | "client") => {
		setCredentials(null);
		setVisibleSecrets(new Set());
		setCurrentPassword("");
		setRotationTarget(target);
		setRotationConfirmed(false);
		setRotation(null);
		setError("");
	};
	const rotate = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		if (!application || !rotationTarget || busy || !rotationConfirmed) return;
		const reauthentication = currentPassword;
		const scope = `application-credential-rotation:${application.id}:${rotationTarget}`;
		setCurrentPassword("");
		setBusy(true);
		setError("");
		try {
			const result = await api.rotateApplicationCredentials(application.id, rotationTarget, reauthentication, secretOperation(scope));
			setRotation(result);
			if (result.state === "succeeded") clearSecretOperation(scope);
		} catch (rotationError) {
			setError(userError(language, rotationError));
		} finally {
			setBusy(false);
		}
	};
	const secretField = (id: string, label: string, value: string) => <Field><FieldLabel htmlFor={id}>{label}</FieldLabel><div className="flex items-center gap-2"><div className="relative min-w-0 flex-1"><Input className="pr-11 font-mono" id={id} readOnly type={visibleSecrets.has(id) ? "text" : "password"} value={value} /><Button aria-label={visibleSecrets.has(id) ? copy(language, "隐藏凭据", "Hide credential") : copy(language, "显示凭据", "Show credential")} className="absolute top-1/2 right-1 -translate-y-1/2" onClick={() => toggleSecret(id)} size="icon-sm" type="button" variant="ghost">{visibleSecrets.has(id) ? <EyeOffIcon aria-hidden="true" /> : <EyeIcon aria-hidden="true" />}</Button></div><CopyButton language={language} size="icon" value={value} /></div></Field>;
	const isCPA = application?.appKey === "vastora-official/cpa";

	return <Sheet onOpenChange={(open) => { if (!open) close(); }} open={Boolean(application)}>
		<SheetContent className="sm:max-w-md">
			<SheetHeader>
				<SheetTitle>{isCPA ? copy(language, "CPA 凭据", "CPA credentials") : copy(language, "3x-ui 管理账号", "3x-ui administrator account")}</SheetTitle>
				<SheetDescription>{copy(language, "凭据由 Center 加密保管。每次查看或轮换都需要重新输入当前 Center 管理员密码，并会记录安全审计事件。", "Center stores these credentials encrypted. Every reveal or rotation requires your current Center administrator password and creates a security audit event.")}</SheetDescription>
			</SheetHeader>
			{rotationTarget ? <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void rotate(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup>
				{rotation ? <Alert variant={rotation.state === "failed" || rotation.state === "action_required" ? "destructive" : "default"}>{rotation.state === "succeeded" ? <CheckCircle2Icon /> : rotation.state === "pending" || rotation.state === "preparing" ? <Spinner /> : <ShieldAlertIcon />}<AlertTitle>{rotation.state === "succeeded" ? copy(language, "凭据轮换完成", "Credential rotation completed") : rotation.state === "pending" || rotation.state === "preparing" ? copy(language, "凭据轮换已排队", "Credential rotation is queued") : copy(language, "凭据轮换需要重试", "Credential rotation needs retry")}</AlertTitle><AlertDescription>{rotation.lastError || copy(language, "CPA 与依赖组件会按顺序应用同一轮换结果。完成前不会生成第二个值。", "CPA and dependent components apply the same rotation in order. No second value is generated while it is pending.")}</AlertDescription></Alert> : null}
				<Field><FieldLabel>{copy(language, "轮换项目", "Credential to rotate")}</FieldLabel><FieldDescription>{rotationTarget === "management" ? copy(language, "CPA 管理密钥；已安装 Keeper 时会同步更新。", "CPA management key; an installed Keeper is updated with it.") : copy(language, "客户端 API 密钥；管理密钥保持不变。", "Client API key; the management key remains unchanged.")}</FieldDescription></Field>
				<Field><FieldLabel htmlFor="application-credential-rotation-password">{copy(language, "Center 管理员密码", "Center administrator password")}</FieldLabel><Input autoComplete="current-password" autoFocus id="application-credential-rotation-password" minLength={administratorPasswordMinLength} onChange={(event) => setCurrentPassword(event.target.value)} required type="password" value={currentPassword} /><FieldDescription>{copy(language, "同一个轮换操作会复用原请求标识；网络响应不明确时重试不会生成第二个密钥。", "The same rotation reuses its original request identity; retrying an ambiguous response does not generate a second key.")}</FieldDescription></Field>
				<Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="application-credential-rotation-confirm">{copy(language, "确认立即轮换", "Confirm immediate rotation")}</FieldLabel><FieldDescription>{copy(language, "旧密钥将在节点应用新配置后失效。", "The previous key becomes invalid after the node applies the new configuration.")}</FieldDescription></div><Switch checked={rotationConfirmed} id="application-credential-rotation-confirm" onCheckedChange={setRotationConfirmed} /></Field>
				{error ? <FieldError role="alert">{error}</FieldError> : null}
			</FieldGroup></div><SheetFooter><Button onClick={() => { setRotationTarget(null); setRotation(null); setCurrentPassword(""); setError(""); }} type="button" variant="outline">{copy(language, "返回", "Back")}</Button><Button disabled={busy || !rotationConfirmed || currentPassword.length < administratorPasswordMinLength || rotation?.state === "succeeded"} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{rotation?.state === "pending" || rotation?.state === "preparing" ? copy(language, "验证并检查", "Verify and check") : copy(language, "验证并轮换", "Verify and rotate")}</Button></SheetFooter></form> : credentials ? <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4">
				<Alert><KeyRoundIcon /><AlertTitle>{copy(language, "凭据仅在当前面板中显示", "Credentials are shown only in this panel")}</AlertTitle><AlertDescription>{copy(language, "关闭后会立即从页面状态中清除；以后仍可再次验证并查看。", "They are cleared from page state as soon as this panel closes. You can reauthenticate to reveal them again later.")}</AlertDescription></Alert>
				<FieldGroup>
					{credentials.kind === "three_x_ui" ? <><Field><FieldLabel htmlFor="three-x-ui-credential-username">{copy(language, "账号", "Username")}</FieldLabel><div className="flex items-center gap-2"><Input className="font-mono" id="three-x-ui-credential-username" readOnly value={credentials.username} /><CopyButton language={language} size="icon" value={credentials.username} /></div></Field>{secretField("three-x-ui-credential-password", copy(language, "密码", "Password"), credentials.password)}</> : <>{secretField("cpa-management-key", copy(language, "管理密钥", "Management key"), credentials.managementKey)}{secretField("cpa-client-api-key", copy(language, "客户端 API 密钥", "Client API key"), credentials.clientApiKey)}</>}
				</FieldGroup>
			</div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void reveal(event)}>
				<div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="application-credential-reauthentication">{copy(language, "Center 管理员密码", "Center administrator password")}</FieldLabel><Input autoComplete="current-password" autoFocus id="application-credential-reauthentication" minLength={administratorPasswordMinLength} onChange={(event) => setCurrentPassword(event.target.value)} required type="password" value={currentPassword} /><FieldDescription>{copy(language, "密码只随本次验证请求发送，不会持久化到浏览器。", "This password is sent only with this verification request and is not persisted in the browser.")}</FieldDescription>{error ? <FieldError role="alert">{error}</FieldError> : null}</Field></FieldGroup></div>
				<SheetFooter><Button onClick={close} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || currentPassword.length < administratorPasswordMinLength} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <KeyRoundIcon data-icon="inline-start" />}{busy ? copy(language, "正在验证…", "Verifying…") : copy(language, "验证并查看", "Verify and reveal")}</Button></SheetFooter>
			</form>}
			{credentials ? <SheetFooter>{credentials.kind === "cpa" ? <><Button onClick={() => beginRotation("management")} type="button" variant="outline">{copy(language, "轮换管理密钥", "Rotate management key")}</Button><Button onClick={() => beginRotation("client")} type="button" variant="outline">{copy(language, "轮换客户端密钥", "Rotate client key")}</Button></> : null}<Button onClick={close} type="button">{copy(language, "关闭", "Close")}</Button></SheetFooter> : null}
		</SheetContent>
	</Sheet>;
}

function ServiceRow({ data, language, service, locked, onPublish, onRename, onTraffic, mutate }: { data: AppData; language: Language; service: Service; locked: boolean; onPublish: () => void; onRename: () => void; onTraffic: () => void; mutate: Mutate }) {
	const publications = data.publications.filter((value) => value.serviceId === service.id && (value.status !== "stopped" || value.actionRequired));
	const cloudflareReady = data.integrations.some((value) => value.kind === "cloudflare" && value.status === "configured");
	const isReality = service.appProtocol === "vless/tcp/reality";
	const application = data.applications.find((value) => value.id === service.applicationId);
	const managedSubscription = application?.appKey === "vastora-official/3x-ui" && application.role === "master" && service.name === "subscription";
	const trafficControllerAvailable = Boolean(application?.controllerApplicationId && data.applications.some((value) => value.id === application.controllerApplicationId && value.role === "master" && value.status === "running"));
	const trafficHintID = `traffic-controller-${service.id}`;
	const summary = isReality
		? copy(language, "VLESS 节点 · 单独计量流量", "VLESS node · independently metered")
		: service.management
			? copy(language, "管理页面", "Admin page")
			: copy(language, "应用服务", "App service");
	return <div className="rounded-xl border p-3">
		<div className="flex flex-wrap items-center gap-2">
			<div className="min-w-0 flex-1">
				<div className="flex flex-wrap items-center gap-2"><p className="truncate text-sm font-medium">{service.displayName || service.name}</p>{service.management ? <Badge variant="destructive">{copy(language, "管理页", "Admin")}</Badge> : null}</div>
				<p className="mt-1 text-xs text-muted-foreground">{summary}</p>
			</div>
			{isReality ? <><Button aria-describedby={!trafficControllerAvailable ? trafficHintID : undefined} disabled={locked || !trafficControllerAvailable} onClick={onTraffic} size="sm" variant="outline">{copy(language, "节点套餐", "Node plan")}</Button><Button disabled={locked} onClick={onRename} size="icon-sm" title={copy(language, "重命名", "Rename")} variant="ghost"><PencilIcon /><span className="sr-only">{copy(language, "重命名", "Rename")}</span></Button></> : null}
			{!managedSubscription ? <Button disabled={locked} onClick={onPublish} size="sm" variant="outline"><Globe2Icon data-icon="inline-start" />{copy(language, "添加入口", "Add access")}</Button> : null}
		</div>
		{isReality && !trafficControllerAvailable ? <p className="mt-2 text-xs text-destructive" id={trafficHintID}>{copy(language, "订阅主机当前不可用，暂时不能修改节点套餐。", "The subscription controller is unavailable, so this node plan cannot be changed yet.")}</p> : null}
		{service.lastError || service.actionRequiredReason ? <div className="mt-2"><TechnicalError error={service.lastError || service.actionRequiredReason} language={language} /></div> : null}
		{publications.length ? <div className="mt-3 flex flex-col gap-2">{publications.map((publication) => <PublicationRow cloudflareReady={cloudflareReady} key={publication.id} language={language} locked={locked} mutate={mutate} publication={publication} />)}</div> : <p className="mt-3 text-xs text-muted-foreground">{managedSubscription ? copy(language, "请使用上方“开启订阅”配置唯一的公网订阅地址。", "Use Enable subscription above to configure the single public subscription URL.") : copy(language, "仅在节点内部运行，添加入口后才能访问。", "Runs privately on the node until you add an access point.")}</p>}
		<details className="mt-3 rounded-lg bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
			<summary className="cursor-pointer font-medium text-foreground">{copy(language, "技术信息", "Technical details")}</summary>
			<dl className="mt-2 grid gap-1.5"><div className="flex gap-2"><dt>{copy(language, "源站", "Origin")}</dt><dd className="min-w-0 break-all font-mono">{service.protocol} · {service.endpoint}</dd></div>{isReality && service.displayName ? <div className="flex gap-2"><dt>{copy(language, "内部名称", "Internal name")}</dt><dd className="min-w-0 break-all font-mono">{service.name}</dd></div> : null}</dl>
		</details>
	</div>;
}

function PublicationRow({ publication, language, locked, mutate, cloudflareReady }: { publication: Publication; language: Language; locked: boolean; mutate: Mutate; cloudflareReady: boolean }) {
	const [busyAction, setBusyAction] = useState<"tls" | "verify" | "security" | "stop" | null>(null);
	const run = async (action: "tls" | "verify" | "security" | "stop", operation: () => Promise<unknown>, success: string) => { setBusyAction(action); try { await mutate(operation, success); } catch { /* The shared notice already explains the failure. */ } finally { setBusyAction(null); } };
	const privateWeb = publication.kind === "lan_gateway" || publication.kind === "headscale_gateway";
	const managedReality = publication.kind === "public_shared_443" && publication.ingress.owner === "application_node";
	const security = publication.securityCheck;
	const securityLabel = !security
		? copy(language, "尚未检查", "Never checked")
		: security.status === "affected"
			? copy(language, "检测到风险", "Risk detected")
			: security.status === "inconclusive"
				? copy(language, "无法确定", "Could not determine")
				: security.scope === "same_host"
					? copy(language, "本机检查通过", "Same-host check passed")
					: copy(language, "上次检查安全", "Last check safe");
	const securityHint = security?.status === "affected"
		? copy(language, "节点能够转发不允许的 TLS 目标，请停止入口并检查 SNI 规则。", "The node forwarded a disallowed TLS target. Stop the access point and inspect its SNI rules.")
		: security?.status === "inconclusive"
			? copy(language, "没有得到完整结果，请确认 Center 到节点 443 的网络后重试。", "The check did not finish conclusively. Confirm connectivity from Center to node port 443, then retry.")
			: security?.scope === "same_host"
				? copy(language, "结果来自 Center 本机路径，不代表外部网络。", "This result used Center's same-host path and does not represent an external network.")
				: "";
	const tlsUnavailable = !publication.tlsEnabled && !cloudflareReady;
	const tlsLabel = publication.tlsEnabled ? copy(language, "关闭 HTTPS", "Turn off HTTPS") : copy(language, "开启 HTTPS", "Turn on HTTPS");
	const tlsStatus = busyAction === "tls" ? publication.tlsEnabled ? copy(language, "正在关闭…", "Turning off…") : copy(language, "正在申请证书…", "Issuing certificate…") : "HTTPS";
	const ingressLabel = publication.ingress.owner === "application_node" ? copy(language, "应用节点", "Application node") : publication.ingress.owner === "tunnel_connector" ? "Tunnel Connector" : "Site Gateway";
	const SecurityIcon = security?.status === "affected" || security?.status === "inconclusive" ? ShieldAlertIcon : ShieldCheckIcon;
	return <div className="flex flex-col gap-2 rounded-lg bg-muted/60 p-3 text-xs sm:flex-row sm:items-center"><StateBadge value={publication.actionRequired ? "action_required" : publication.status} /><div className="min-w-0 flex-1"><p className="break-all font-medium" title={publication.accessUrl ?? publication.hostname}>{publication.accessUrl ?? publication.hostname}</p><p className="mt-0.5 text-muted-foreground">{publicationKindLabel(language, publication.kind)} · {ingressLabel}{privateWeb ? ` · ${publication.tlsEnabled ? "HTTPS" : "HTTP"}` : ""}</p>{publication.sniHostname ? <p className="mt-0.5 truncate font-mono text-muted-foreground">SNI → {publication.sniHostname}</p> : null}{managedReality ? <div className="mt-1.5 flex flex-wrap items-center gap-1.5"><Badge variant={security?.status === "affected" ? "destructive" : security?.status === "safe" ? "secondary" : "outline"}><SecurityIcon data-icon="inline-start" />{securityLabel}</Badge>{security ? <span className="text-muted-foreground">{formatDate(language, security.checkedAt)}</span> : null}</div> : null}{securityHint ? <p className={security?.status === "affected" ? "mt-1 text-destructive" : "mt-1 text-muted-foreground"}>{securityHint}</p> : null}{publication.lastError ? <div className="mt-1"><TechnicalError error={publication.lastError} language={language} /></div> : null}{publication.dnsRecord && publication.dnsProvider !== "cloudflare" ? <code className="mt-1 block break-all text-muted-foreground">{publication.dnsRecord.type} {publication.dnsRecord.name} → {publication.dnsRecord.value}</code> : null}</div><div className="flex flex-wrap items-center gap-2">{privateWeb && publication.status !== "stopped" ? <div aria-busy={busyAction === "tls"} className="flex min-h-9 items-center gap-2 rounded-lg border bg-background px-2.5" title={tlsUnavailable ? copy(language, "连接 Cloudflare 后才能申请可信证书", "Connect Cloudflare to issue a trusted certificate") : undefined}><label className="font-medium" htmlFor={`publication-tls-${publication.id}`} role={busyAction === "tls" ? "status" : undefined}>{tlsStatus}</label>{busyAction === "tls" ? <Spinner aria-hidden="true" /> : null}<Switch aria-label={tlsLabel} checked={publication.tlsEnabled} disabled={locked || busyAction !== null || tlsUnavailable} id={`publication-tls-${publication.id}`} onCheckedChange={(enabled) => void run("tls", () => api.updatePublicationTLS(publication.id, enabled), enabled ? copy(language, "正在启用 HTTPS，入口配置已提交。", "HTTPS is being enabled and the access configuration was submitted.") : copy(language, "HTTPS 已关闭，入口将使用 HTTP。", "HTTPS was turned off; the access point will use HTTP."))} /></div> : null}{publication.accessUrl ? <Button aria-label={copy(language, "打开服务", "Open service")} nativeButton={false} render={<a href={publication.accessUrl} rel="noreferrer" target="_blank" />} size="icon-sm" variant="ghost"><ExternalLinkIcon /></Button> : null}{managedReality && publication.status !== "stopped" ? <Button disabled={locked || busyAction !== null || publication.status !== "ready"} onClick={() => void run("security", () => api.checkRealitySecurity(publication.id), copy(language, "安全检查已完成。", "Security check completed."))} size="sm" variant="outline">{busyAction === "security" ? <Spinner data-icon="inline-start" /> : <ShieldCheckIcon data-icon="inline-start" />}{copy(language, "安全检查", "Security check")}</Button> : null}{publication.status !== "ready" && publication.status !== "stopped" ? <Button disabled={locked || busyAction !== null} onClick={() => void run("verify", () => api.verifyPublication(publication.id), copy(language, "入口检查已完成。", "Access point checked."))} size="sm" variant="outline">{busyAction === "verify" ? <Spinner data-icon="inline-start" /> : null}{copy(language, "检查", "Check")}</Button> : null}{publication.status !== "stopped" ? <Button disabled={busyAction !== null} onClick={() => void run("stop", () => api.stopPublication(publication.id), copy(language, "入口已停止。", "Access point stopped."))} size="sm" variant="ghost">{busyAction === "stop" ? <Spinner data-icon="inline-start" /> : null}{copy(language, "停止", "Stop")}</Button> : null}</div></div>;
}

function DeploymentSheet({ data, editor, language, onClose, onSubmit }: { data: AppData; editor: DeploymentEditor; language: Language; onClose: () => void; onSubmit: (agent: AgentView, app: AppView, config: Record<string, string | boolean | number>, operation: "install" | "upgrade" | "configure", role?: ThreeXUIRole, registryCredentialId?: string) => Promise<void> }) {
  const [agentID, setAgentID] = useState("");
  const [config, setConfig] = useState<Record<string, string | boolean | number>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [registryCredentialID, setRegistryCredentialID] = useState("");
  const candidates = editor ? data.agents.filter((agent) => editor.operation !== "install" ? agent.id === editor.agent?.id : canInstall(agent) && !data.applications.some((application) => application.nodeId === agent.id && application.appKey === editor.app.key && (isInstalledApplication(application) || isActiveApplication(application.status)))) : [];
  const isThreeXUIInstall = editor?.operation === "install" && editor.app.key === "vastora-official/3x-ui";
  const globalController = isThreeXUIInstall ? data.applications.find((application) => application.appKey === "vastora-official/3x-ui" && application.role === "master" && application.id === application.controllerApplicationId && isActiveApplication(application.status)) : undefined;
  const role: ThreeXUIRole | undefined = isThreeXUIInstall ? globalController ? "worker" : "master" : undefined;
  const controllerReady = !globalController || globalController.status === "running";
  const controllerNode = globalController ? data.agents.find((agent) => agent.id === globalController.nodeId) : undefined;
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
    setRegistryCredentialID(editor.operation === "install" ? "" : "__preserve__");
    setError("");
  }, [editor]);
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); if (!editor) return; const agent = data.agents.find((value) => value.id === agentID); if (!agent) { setError(copy(language, "请选择节点。", "Select a node.")); return; } if (!controllerReady) { setError(copy(language, "请等待全局订阅主机启动完成。", "Wait for the global subscription controller to finish starting.")); return; } setBusy(true); setError(""); try { await onSubmit(agent, editor.app, config, editor.operation, role, registryCredentialID === "__preserve__" ? undefined : registryCredentialID); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); } };
  const verbs = editor?.operation === "install" ? ["安装", "Install"] : editor?.operation === "upgrade" ? ["升级", "Upgrade"] : ["修改配置", "Change settings"];
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(editor)}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{editor ? `${copy(language, verbs[0], verbs[1])} ${localized(editor.app, language, "name")}` : ""}</SheetTitle><SheetDescription>{editor?.operation === "install" ? copy(language, "应用先作为私有源站启动；访问入口稍后单独添加。", "The app starts as a private origin. Add access points separately afterward.") : editor?.operation === "upgrade" ? copy(language, "升级到目录中的新版本；可同时填写需要修改的配置。", "Upgrade to the newer catalog version and optionally change settings.") : copy(language, "只填写至少一项要修改的配置；留空项保持原值。", "Enter at least one setting to change; omitted values keep their previous value.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="deployment-agent">{copy(language, "节点", "Node")}</FieldLabel><SelectControl disabled={editor?.operation !== "install"} id="deployment-agent" onValueChange={setAgentID} options={[{ value: "", label: copy(language, "选择节点", "Select a node"), disabled: true }, ...candidates.map((agent) => ({ value: agent.id, label: agent.name }))]} required value={agentID} /><FieldDescription>{candidates.length === 0 ? copy(language, "没有可用节点。请先确认节点网络。", "No eligible node. Confirm node networking first.") : editor?.operation === "install" ? copy(language, "同一应用在同一节点只能安装一次。", "An app can be installed only once on the same node.") : copy(language, "现有安装会在原节点上更新。", "The existing installation is changed on its current node.")}</FieldDescription></Field>{isThreeXUIInstall && role === "master" ? <Alert><UsersIcon /><AlertTitle>{copy(language, "将作为全局订阅主机", "This will be the global subscription controller")}</AlertTitle><AlertDescription>{copy(language, "这是 Center 中第一台 3x-ui。它提供唯一管理面板和订阅地址，所有地区后续添加的节点都会自动接入这里。", "This is the first 3x-ui in this Center. It provides the only admin panel and subscription URL; later nodes in every region connect here automatically.")}</AlertDescription></Alert> : null}{isThreeXUIInstall && role === "worker" ? <Alert><RadioTowerIcon /><AlertTitle>{copy(language, "将作为 VLESS 节点", "This will be a VLESS node")}</AlertTitle><AlertDescription>{copy(language, `安装后自动接入 ${controllerNode?.name ?? "全局订阅主机"}；不会再创建独立面板或订阅地址。`, `After installation it connects to ${controllerNode?.name ?? "the global subscription controller"}; no separate panel or subscription URL is created.`)}</AlertDescription></Alert> : null}{isThreeXUIInstall && !controllerReady ? <FieldError role="alert">{copy(language, "全局订阅主机尚未就绪，请稍后再安装节点。", "The global subscription controller is not ready yet. Install the node after it is running.")}</FieldError> : null}{editor?.app.app.config.map((field) => <ConfigField config={config} field={field} key={field.key} language={language} operation={editor.operation} setConfig={setConfig} />)}<Field><FieldLabel htmlFor="deployment-registry">{copy(language, "镜像仓库凭据", "Image Registry credential")}</FieldLabel><SelectControl id="deployment-registry" onValueChange={setRegistryCredentialID} options={[...(editor?.operation === "install" ? [{ value: "", label: copy(language, "不使用凭据（公开镜像）", "No credential (public image)") }] : [{ value: "__preserve__", label: copy(language, "保持当前凭据", "Keep current credential") }, { value: "", label: copy(language, "清除凭据，改用公开镜像", "Clear credential and use public image") }]), ...data.registryCredentials.map((credential) => ({ value: credential.id, label: `${credential.host} — ${credential.username}` }))]} value={registryCredentialID} /><FieldDescription>{copy(language, "令牌不会显示或写入节点 Docker 配置；仅在本次拉取时使用。", "Tokens are never displayed or written to the node Docker config; they are used only for this pull.")}</FieldDescription></Field>{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !agentID || !controllerReady || editor?.operation === "configure" && Object.keys(config).length === 0} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{editor?.operation === "install" ? copy(language, "开始安装", "Install") : editor?.operation === "upgrade" ? copy(language, "开始升级", "Upgrade") : copy(language, "应用修改", "Apply changes")}</Button></SheetFooter></form></SheetContent></Sheet>;
}

function ConfigField({ config, field, language, operation, setConfig }: { config: Record<string, string | boolean | number>; field: AppView["app"]["config"][number]; language: Language; operation: "install" | "upgrade" | "configure"; setConfig: React.Dispatch<React.SetStateAction<Record<string, string | boolean | number>>> }) {
  const label = field.label[language] || field.label.en;
  const description = field.description[language] || field.description.en;
  if (field.type === "boolean" && operation !== "install") return <Field><FieldLabel htmlFor={`config-${field.key}`}>{label}</FieldLabel><SelectControl id={"config-" + field.key} onValueChange={(value) => setConfig((current) => { const next = { ...current }; if (!value) delete next[field.key]; else next[field.key] = value === "true"; return next; })} options={[{ value: "", label: copy(language, "保持当前设置", "Keep current setting") }, { value: "true", label: copy(language, "开启", "On") }, { value: "false", label: copy(language, "关闭", "Off") }]} value={config[field.key] === undefined ? "" : String(config[field.key])} /><FieldDescription>{description}</FieldDescription></Field>;
  if (field.type === "boolean") return <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor={`config-${field.key}`}>{label}</FieldLabel><FieldDescription>{description}</FieldDescription></div><Switch checked={Boolean(config[field.key])} id={`config-${field.key}`} onCheckedChange={(value) => setConfig((current) => ({ ...current, [field.key]: value }))} /></Field>;
  return <Field><FieldLabel htmlFor={`config-${field.key}`}>{label}</FieldLabel><Input id={`config-${field.key}`} min={field.type === "integer" ? 1 : undefined} onChange={(event) => setConfig((current) => { const next = { ...current }; if (event.target.value === "") delete next[field.key]; else next[field.key] = field.type === "integer" ? Number(event.target.value) : event.target.value; return next; })} placeholder={operation !== "install" ? copy(language, "留空以保持原值", "Leave blank to keep the current value") : undefined} required={operation === "install" && field.required} type={field.secret ? "password" : field.type === "integer" ? "number" : "text"} value={config[field.key] === undefined ? "" : String(config[field.key])} /><FieldDescription>{description}</FieldDescription></Field>;
}

function PublicationSheet({ data, language, onClose, onSubmit, service }: { data: AppData; language: Language; onClose: () => void; onSubmit: (input: CreatePublicationInput) => Promise<void>; service: Service | null }) {
  const [intent, setIntent] = useState<PublicationIntent>("private");
  const [kind, setKind] = useState<PublicationKind>("headscale_gateway");
  const [gatewayID, setGatewayID] = useState("");
  const [hostname, setHostname] = useState("");
  const [hostnameTouched, setHostnameTouched] = useState(false);
  const hostnameInput = useRef<HTMLInputElement>(null);
  const [sniHostname, setSNIHostname] = useState("");
  const [dnsProvider, setDNSProvider] = useState<"manual" | "cloudflare" | "headscale">("manual");
  const [highRisk, setHighRisk] = useState(false);
  const [tlsEnabled, setTLSEnabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [cloudflareAccessStatus, setCloudflareAccessStatus] = useState(data.centerRemoteAccess);
  const [cloudflareAccessLoadError, setCloudflareAccessLoadError] = useState(data.centerRemoteAccessError);
  const [cloudflareAccessLoading, setCloudflareAccessLoading] = useState(false);
  const cloudflare = data.integrations.find((value) => value.kind === "cloudflare" && value.status === "configured");
  const cloudflareReady = Boolean(cloudflare);
  const cloudflareZone = normalizeHostname(cloudflare?.endpoint ?? "");
  const headscaleReady = data.integrations.some((value) => value.kind === "headscale" && value.status === "configured" && value.mode === "builtin");
  const options = useMemo(() => publicationOptions(data, service, language), [data, service, language]);
  const intents = useMemo(() => publicationIntentOptions(data, service, language), [data, service, language]);
  const gateways = service ? gatewaysForKind(data, service, kind) : [];
  const availableKinds = service ? publicationKindsForIntent(service, intent) : [];
  const advancedOptions = options.filter((option) => availableKinds.includes(option.kind));
  const selectedOption = options.find((option) => option.kind === kind);
  const application = service ? data.applications.find((value) => value.id === service.applicationId) : undefined;
  const applicationNode = data.agents.find((value) => value.id === application?.nodeId);
  const applicationNodeIngress = Boolean(service && (service.protocol !== "http" && service.protocol !== "https" || kind === "public_shared_443"));
  const ingressOwner = applicationNodeIngress ? "application_node" : kind === "cloudflare_tunnel" ? "tunnel_connector" : "site_gateway";
  const managedRealityOnOwnNode = Boolean(service?.appProtocol === "vless/tcp/reality" && application?.appKey === "vastora-official/3x-ui");
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
    setHostname(defaultPublicationHostname(data, service, nextKind));
    setHostnameTouched(false);
    setSNIHostname("");
    setDNSProvider(defaultDNS(nextKind));
    setTLSEnabled(cloudflareReady && (nextKind === "lan_gateway" || nextKind === "headscale_gateway"));
    setHighRisk(false); setError("");
  }, [service?.id]);
  useEffect(() => {
    setCloudflareAccessStatus(data.centerRemoteAccess);
    setCloudflareAccessLoadError(data.centerRemoteAccessError);
    if (!service || data.centerRemoteAccess || data.centerRemoteAccessError) {
      setCloudflareAccessLoading(false);
      return;
    }
    const controller = new AbortController();
    setCloudflareAccessLoading(true);
    void api.centerRemoteAccess(controller.signal).then((status) => {
      setCloudflareAccessStatus(status);
      setCloudflareAccessLoadError(undefined);
    }).catch((loadError) => {
      if (!controller.signal.aborted) setCloudflareAccessLoadError(loadError instanceof Error ? loadError.message : "Center remote access status request failed");
    }).finally(() => {
      if (!controller.signal.aborted) setCloudflareAccessLoading(false);
    });
    return () => controller.abort();
  }, [service?.id, data.centerRemoteAccess, data.centerRemoteAccessError]);
  const selectKind = (next: PublicationKind) => {
    setKind(next); const nodes = service ? gatewaysForKind(data, service, next) : [];
    setGatewayID(nodes[0]?.id ?? "");
    if (service) setHostname(defaultPublicationHostname(data, service, next));
    setHostnameTouched(false);
    setError("");
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
  const highRiskRequired = Boolean(service?.management && kind === "public_direct");
  const cloudflareAccessRequired = kind === "cloudflare_tunnel" && (cloudflareAccessLoading || Boolean(cloudflareAccessLoadError) || cloudflareAccessStatus?.status !== "configured" || cloudflareAccessStatus?.protectionMode !== "access");
  const automaticPublicHostname = kind === "public_direct" || kind === "cloudflare_tunnel";
  const normalizedHostname = normalizeHostname(hostname);
  const hostnameZone = dnsProvider === "cloudflare" ? cloudflareZone : "";
  const hostnameExample = `app.${hostnameZone || data.sites.find((site) => site.id === service?.siteId)?.domainSuffix || "example.com"}`;
  const hostnameError = !normalizedHostname && automaticPublicHostname ? ""
    : !validHostname(hostname)
      ? copy(language, `请输入完整域名，例如 ${hostnameExample}；不能只填前缀，也不要包含 https://、端口或路径。`, `Enter a complete hostname, such as ${hostnameExample}, not just a prefix. Do not include https://, a port, or a path.`)
      : hostnameZone && normalizedHostname !== hostnameZone && !normalizedHostname.endsWith(`.${hostnameZone}`)
        ? copy(language, `域名必须属于当前 Cloudflare 域名 ${hostnameZone}，例如 ${hostnameExample}。`, `Use the configured Cloudflare zone ${hostnameZone} or one of its subdomains, such as ${hostnameExample}.`)
        : "";
  const hostnameInvalid = hostnameTouched && Boolean(hostnameError);
  const canSubmit = Boolean(selectedOption?.enabled && !hostnameError && (applicationNodeIngress ? applicationNode : gatewayID) && (kind !== "public_shared_443" || sniHostname) && (!tlsEnabled || cloudflareReady) && !cloudflareAccessRequired && (!highRiskRequired || highRisk));
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!service) return;
    setError("");
    setHostnameTouched(true);
    if (hostnameError) { hostnameInput.current?.focus(); return; }
    if (!canSubmit || busy) return;
    setBusy(true);
    try {
      const ingress: PublicationIngressInput = applicationNodeIngress ? { owner: "application_node" } : ingressOwner === "tunnel_connector" ? { owner: "tunnel_connector", entryNodeId: gatewayID } : { owner: "site_gateway", entryNodeId: gatewayID };
      await onSubmit({ serviceId: service.id, kind, ingress, hostname: normalizedHostname || undefined, sniHostname: kind === "public_shared_443" ? sniHostname : undefined, dnsProvider, tlsEnabled: (kind === "lan_gateway" || kind === "headscale_gateway") && tlsEnabled, confirmHighRisk: highRisk });
    } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); }
  };
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
              <Field data-invalid={hostnameInvalid}>
                <FieldLabel htmlFor="publication-hostname">{copy(language, kind === "public_direct" || kind === "cloudflare_tunnel" ? "公网入口域名（可自定义）" : "访问域名", kind === "public_direct" || kind === "cloudflare_tunnel" ? "Public hostname (customizable)" : "Access hostname")}</FieldLabel>
                <Input aria-describedby={hostnameInvalid ? "publication-hostname-help publication-hostname-error" : "publication-hostname-help"} aria-invalid={hostnameInvalid} autoCapitalize="none" autoCorrect="off" id="publication-hostname" onBlur={(event) => { setHostnameTouched(true); setHostname(normalizeHostname(event.target.value)); }} onChange={(event) => { setHostname(event.target.value.toLowerCase()); setError(""); }} placeholder={automaticPublicHostname ? copy(language, "留空时自动生成", "Generated automatically when empty") : hostnameExample} ref={hostnameInput} required={!automaticPublicHostname} spellCheck={false} value={hostname} />
                <FieldDescription id="publication-hostname-help">{automaticPublicHostname ? copy(language, `留空由 Center 生成 128-bit 随机域名，不包含应用或节点信息。自定义请填写完整域名，例如 ${hostnameExample}。`, `Leave empty for a random 128-bit hostname without app or node details. To customize it, enter a complete hostname such as ${hostnameExample}.`) : copy(language, `填写完整域名，例如 ${hostnameExample}，不要只填前缀。`, `Enter a complete hostname such as ${hostnameExample}, not just a prefix.`)}</FieldDescription>
                {hostnameInvalid ? <FieldError id="publication-hostname-error">{hostnameError}</FieldError> : null}
              </Field>
              {kind === "public_shared_443" ? <Field><FieldLabel htmlFor="publication-sni">{copy(language, "协议 SNI", "Protocol SNI")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="publication-sni" onChange={(event) => setSNIHostname(event.target.value.toLowerCase())} placeholder="www.example.com" required spellCheck={false} value={sniHostname} /><FieldDescription>{copy(language, "客户端握手中使用的 SNI；它与上面的连接域名是两个不同地址。", "The SNI sent in the client handshake. It is different from the connection hostname above.")}</FieldDescription></Field> : null}
              {intent === "protocol" ? <Alert><ShieldAlertIcon /><AlertTitle>{copy(language, "这是高级公网入口", "This is an advanced public access method")}</AlertTitle><AlertDescription>{copy(language, "应用负责协议和端口配置；Vastora 只检查公网能力与运行状态。", "The app controls protocol and ports. Vastora only checks public reachability and runtime status.")}</AlertDescription></Alert> : null}
              {kind === "cloudflare_tunnel" && cloudflareAccessLoading ? <Alert><Spinner /><AlertTitle>{copy(language, "正在读取 Center 远程入口状态", "Loading the Center remote entry status")}</AlertTitle><AlertDescription>{copy(language, "正在确认 Cloudflare Access 配置。", "Checking the Cloudflare Access configuration.")}</AlertDescription></Alert> : null}
              {kind === "cloudflare_tunnel" && !cloudflareAccessLoading && cloudflareAccessLoadError ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "无法读取 Center 远程入口状态", "Could not load the Center remote entry status")}</AlertTitle><AlertDescription>{copy(language, "当前无法确认 Cloudflare Access 是否已配置，因此暂不允许创建入口。请重试页面同步。", "Cloudflare Access configuration cannot be confirmed, so this entry cannot be created yet. Retry the page sync.")}<code className="mt-2 block break-all text-xs">{cloudflareAccessLoadError}</code></AlertDescription></Alert> : null}
              {kind === "cloudflare_tunnel" && !cloudflareAccessLoading && !cloudflareAccessLoadError && !cloudflareAccessStatus ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "尚未读取 Center 远程入口状态", "Center remote entry status has not loaded")}</AlertTitle><AlertDescription>{copy(language, "当前无法确认 Cloudflare Access 是否已配置，因此暂不允许创建入口。请重试页面同步。", "Cloudflare Access configuration cannot be confirmed, so this entry cannot be created yet. Retry the page sync.")}</AlertDescription></Alert> : null}
              {kind === "cloudflare_tunnel" && !cloudflareAccessLoadError && cloudflareAccessStatus?.status === "pending" ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "Center 远程入口正在配置", "The Center remote entry is being configured")}</AlertTitle><AlertDescription>{copy(language, "请等待网络页面完成 Cloudflare Access 配置后再创建应用入口。", "Wait for Cloudflare Access setup to finish on the Network page before creating this application entry.")}</AlertDescription></Alert> : null}
              {kind === "cloudflare_tunnel" && !cloudflareAccessLoadError && cloudflareAccessStatus?.status === "failed" ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "Center 远程入口配置失败", "Center remote entry setup failed")}</AlertTitle><AlertDescription>{copy(language, "请先在网络页面修复 Cloudflare Access 配置。", "Fix the Cloudflare Access configuration on the Network page first.")}{cloudflareAccessStatus.lastError ? <code className="mt-2 block break-all text-xs">{cloudflareAccessStatus.lastError}</code> : null}</AlertDescription></Alert> : null}
              {kind === "cloudflare_tunnel" && !cloudflareAccessLoadError && cloudflareAccessStatus?.status === "disabled" ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, cloudflareAccessStatus.available ? "请先启用 Center 远程入口" : "Center 远程入口不可用", cloudflareAccessStatus.available ? "Enable the Center remote entry first" : "The Center remote entry is unavailable")}</AlertTitle><AlertDescription>{copy(language, cloudflareAccessStatus.available ? "Cloudflare 模式会复用相同的 Access 登录限制；请先在网络页面完成配置。" : "当前 Center 无法管理 Cloudflare Access，因此不能创建此入口。", cloudflareAccessStatus.available ? "Cloudflare mode reuses the same Access login restriction. Configure it on the Network page first." : "This Center cannot manage Cloudflare Access, so this entry cannot be created.")}</AlertDescription></Alert> : null}
              {kind === "cloudflare_tunnel" && !cloudflareAccessLoadError && cloudflareAccessStatus?.status === "configured" && cloudflareAccessStatus.protectionMode !== "access" ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "应用入口需要 Cloudflare Access 模式", "Application entry requires Cloudflare Access mode")}</AlertTitle><AlertDescription>{copy(language, "Center 当前使用 Turnstile 直达登录。请在网络页面把远程备用入口切换为“Cloudflare Access 双层登录”，再创建受 Access 保护的应用入口。", "Center currently uses direct Turnstile sign-in. On the Network page, switch the remote fallback to Cloudflare Access two-layer sign-in before creating an Access-protected application entry.")}</AlertDescription></Alert> : null}
              {highRiskRequired ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "管理页面公网发布风险较高", "Publishing an admin page publicly is high risk")}</AlertTitle><AlertDescription>{copy(language, "请确认应用已设置强密码。Vastora 第一版不会代管额外的访问认证。", "Confirm that the app has a strong password. Vastora v1 does not manage an additional access login.")}<Field className="mt-3" orientation="horizontal"><FieldLabel htmlFor="confirm-high-risk">{copy(language, "我确认继续公网发布", "I understand and want to publish")}</FieldLabel><Switch checked={highRisk} id="confirm-high-risk" onCheckedChange={setHighRisk} /></Field></AlertDescription></Alert> : null}
              {kind === "lan_gateway" || kind === "headscale_gateway" ? <Field className="rounded-xl border p-3" orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="publication-tls">HTTPS</FieldLabel><FieldDescription>{cloudflareReady ? copy(language, "默认开启。使用 Cloudflare DNS 验证申请可信证书，服务仍只在私网开放。", "On by default. Cloudflare DNS validation issues a trusted certificate while the service remains private.") : copy(language, "连接 Cloudflare 后可以开启；当前入口将使用私网 HTTP。", "Connect Cloudflare to enable it. This access point will use private HTTP for now.")}</FieldDescription></div><Switch aria-label={copy(language, "使用 HTTPS", "Use HTTPS")} checked={tlsEnabled} disabled={!cloudflareReady} id="publication-tls" onCheckedChange={setTLSEnabled} /></Field> : null}
              <details className="rounded-xl border p-3">
                <summary className="cursor-pointer text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}</summary>
                <div className="mt-4 flex flex-col gap-4">
                  <Field>
                    <FieldLabel htmlFor="publication-kind">{copy(language, "底层入口方式", "Underlying access method")}</FieldLabel>
                    <SelectControl id="publication-kind" onValueChange={(value) => selectKind(value as PublicationKind)} options={advancedOptions.map((option) => ({ value: option.kind, label: publicationKindLabel(language, option.kind) + " — " + option.reason, disabled: !option.enabled }))} value={kind} />
                    <FieldDescription>{selectedOption?.reason}</FieldDescription>
                  </Field>
                  {applicationNodeIngress ? <Field><FieldLabel>{copy(language, "入口所有者", "Ingress owner")}</FieldLabel><div className="rounded-lg border bg-muted/40 px-3 py-2 text-sm">{copy(language, "应用节点", "Application node")} · {applicationNode?.name ?? copy(language, "节点不可用", "Node unavailable")}</div><FieldDescription>{copy(language, "协议入口固定由运行应用的节点提供，不能选择其他 Site Gateway。", "Protocol ingress is always provided by the node running the application; another Site Gateway cannot be selected.")}</FieldDescription></Field> : <Field><FieldLabel htmlFor="publication-node">{copy(language, ingressOwner === "tunnel_connector" ? "Tunnel Connector" : "Site Gateway", ingressOwner === "tunnel_connector" ? "Tunnel connector" : "Site Gateway")}</FieldLabel><SelectControl id="publication-node" onValueChange={setGatewayID} options={[{ value: "", label: copy(language, "没有可用节点", "No node available"), disabled: true }, ...gateways.map((agent) => ({ value: agent.id, label: agent.name }))]} required value={gatewayID} /><FieldDescription>{copy(language, ingressOwner === "tunnel_connector" ? "选择负责连接 Cloudflare Tunnel 的节点。" : "选择负责站点 Web 反向代理的网关。", ingressOwner === "tunnel_connector" ? "Select the node that connects the Cloudflare Tunnel." : "Select the gateway that provides Site Web reverse proxying.")}</FieldDescription></Field>}
                  <Field>
                    <FieldLabel htmlFor="publication-dns">DNS</FieldLabel>
                    <SelectControl disabled={kind === "cloudflare_tunnel"} id="publication-dns" onValueChange={(value) => setDNSProvider(value as "manual" | "cloudflare" | "headscale")} options={[{ value: "manual", label: copy(language, "手动配置", "Manual") }, ...(kind === "headscale_gateway" && headscaleReady ? [{ value: "headscale", label: "Headscale DNS" }] : []), ...((kind === "public_direct" || kind === "public_shared_443") && cloudflareReady ? [{ value: "cloudflare", label: "Cloudflare DNS-only" }] : []), ...(kind === "cloudflare_tunnel" ? [{ value: "cloudflare", label: "Cloudflare Tunnel" }] : [])]} value={dnsProvider} />
                  </Field>
                  {kind === "public_shared_443" ? <Alert><ShieldAlertIcon /><AlertTitle>{copy(language, "共享公网 443", "Shared public 443")}</AlertTitle><AlertDescription>{managedRealityOnOwnNode ? copy(language, "这是 Vastora 管理的 3x-ui REALITY：容器内部 443 合法且不会映射到宿主机；宿主机公网 443 由 HAProxy 独占并按精确 SNI 转发。", "This is Vastora-managed 3x-ui REALITY: container port 443 is valid and is not published on the host; HAProxy exclusively owns the host's public port 443 and routes by exact SNI.") : copy(language, "连接域名只负责解析到节点，HAProxy 会按协议 SNI 分流。普通应用与入口位于同一节点时，应用监听端口必须避开宿主机 443。", "The connection hostname only resolves to the node, and HAProxy routes by protocol SNI. When a regular app and its entry are on the same node, the app listener must not occupy host port 443.")}</AlertDescription></Alert> : null}
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
  const [busyAction, setBusyAction] = useState<"configure" | "verify" | null>(null);
  const [error, setError] = useState("");
  const { execute } = useApplicationCommandExecutor(application?.id);
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
    setGatewayID(publication?.ingress.entryNodeId ?? preferredGateways[0]?.id ?? "");
    setHostname(publication?.hostname ?? defaultPublicationHostname(data, service, preferredKind));
    setCommand(null); setBusyAction(null); setError("");
    if (!publication) return;
    let cancelled = false;
    void api.latestApplicationCommand(application.id, "3xui.subscription.configure").then((latest) => {
      if (cancelled) return;
      setCommand(latest);
      if (latest.state === "pending" || latest.state === "running") {
        void execute(() => Promise.resolve(latest), setCommand).catch((pollError) => setError(userError(language, pollError)));
      }
    }).catch(() => { /* A ready publication can outlive its completed command record. */ });
    return () => { cancelled = true; };
  }, [application?.id, service?.id, publication?.id, execute, language]);
  const selectKind = (next: "cloudflare_tunnel" | "public_direct") => {
    setKind(next);
    const nextGateways = next === "cloudflare_tunnel" ? tunnelGateways : directGateways;
    setGatewayID(nextGateways[0]?.id ?? "");
    if (service) setHostname(defaultPublicationHostname(data, service, next));
  };
  const configure = async () => {
    if (!application) return;
    setBusyAction("configure"); setError("");
    try {
      let created: ApplicationCommand | undefined;
      const dnsProvider = publication?.dnsProvider === "manual" || publication?.dnsProvider === "cloudflare"
        ? publication.dnsProvider
        : (kind === "cloudflare_tunnel" || cloudflareReady ? "cloudflare" : "manual");
      await mutate(async () => { created = await api.createSubscriptionCommand({ applicationId: application.id, gatewayNodeId: gatewayID, hostname: hostname || undefined, kind, dnsProvider }); }, copy(language, "公网订阅配置已开始。", "Public subscription setup started."));
      if (created) {
        const completed = await execute(() => Promise.resolve(created!), setCommand);
        if (completed?.state === "failed") throw new Error(completed.error || "The 3x-ui subscription configuration failed");
      }
    } catch (submitError) {
      setError(userError(language, submitError));
    } finally {
      setBusyAction(null);
    }
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    void configure();
  };
  const verify = async () => {
    if (!publication) return;
    setBusyAction("verify"); setError("");
    try {
      await mutate(() => api.verifyPublication(publication.id), copy(language, "订阅入口检查已完成。", "Subscription access point checked."));
    } catch (verifyError) {
      setError(userError(language, verifyError));
    } finally {
      setBusyAction(null);
    }
  };
  const ready = publication?.status === "ready";
  const publicationProblem = publication?.status === "failed" || publication?.status === "degraded";
  const commandActive = command?.state === "pending" || command?.state === "running";
  const commandFailed = command?.state === "failed";
  const configured = command?.state === "succeeded";
  const statusTitle = ready
    ? copy(language, "公网订阅已开启", "Public subscription is enabled")
    : commandFailed
      ? copy(language, "3x-ui 配置失败", "3x-ui configuration failed")
      : publicationProblem
        ? copy(language, "订阅入口需要处理", "Subscription access needs attention")
        : commandActive
          ? copy(language, "正在自动配置…", "Configuring automatically…")
          : configured
            ? copy(language, "3x-ui 已配置，入口尚未确认", "3x-ui is configured; access is not verified")
            : copy(language, "订阅入口尚未确认", "Subscription access is not verified");
  const statusDescription = ready
    ? copy(language, "同一个订阅地址会自动适配 OpenClash、Mihomo 和其他客户端。", "One subscription URL automatically adapts to OpenClash, Mihomo, and other clients.")
    : commandFailed
      ? command?.error || copy(language, "3x-ui 没有接受这次配置。", "3x-ui did not accept this configuration.")
      : publicationProblem
        ? publication?.lastError || copy(language, "域名或 HTTPS 检查没有通过。", "The hostname or HTTPS check did not pass.")
        : commandActive
          ? copy(language, "正在创建 HTTPS 入口并同步 3x-ui 设置。", "Creating the HTTPS access point and syncing 3x-ui settings.")
          : configured
            ? copy(language, "配置命令已经成功，不需要继续等待。请立即检查入口；如仍失败，可重试配置。", "The configuration command succeeded, so you do not need to keep waiting. Check the access point now, or retry configuration if it still fails.")
            : copy(language, "入口尚未报告就绪。你可以立即检查，不需要停留在此页面等待。", "The access point has not reported ready. Check it now; you do not need to keep this sheet open.");
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(application)}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, "公网订阅", "Public subscription")}</SheetTitle><SheetDescription>{copy(language, "Vastora 会发布独立订阅服务，并把公网域名自动写入 3x-ui。管理面板仍只在私网开放。", "Vastora publishes the separate subscription service and writes its public hostname into 3x-ui. The admin panel stays private.")}</SheetDescription></SheetHeader>{command || publication ? <div aria-live="polite" className="flex flex-1 flex-col gap-4 overflow-y-auto px-4"><Alert variant={commandFailed || publicationProblem ? "destructive" : "default"}>{ready ? <Globe2Icon /> : commandActive ? <Spinner /> : commandFailed || publicationProblem ? <ShieldAlertIcon /> : <RefreshCwIcon />}<AlertTitle>{statusTitle}</AlertTitle><AlertDescription>{statusDescription}</AlertDescription></Alert>{publication ? <div className="rounded-xl border bg-muted/40 p-4"><div className="flex items-start justify-between gap-3"><div className="min-w-0"><p className="truncate text-sm font-medium">{publication.hostname}</p><p className="mt-1 text-xs text-muted-foreground">{publicationKindLabel(language, publication.kind)}</p></div><StateBadge value={publication.status} /></div>{publication.lastError ? <div className="mt-3"><TechnicalError error={publication.lastError} language={language} /></div> : null}</div> : null}{ready ? <div className="rounded-xl border bg-muted/40 p-4"><p className="text-sm font-medium">{copy(language, "一个地址，自动适配", "One URL, automatic format")}</p><div className="mt-3 flex flex-wrap gap-2"><Badge variant="secondary">OpenClash · Mihomo · {copy(language, "其他客户端", "Other clients")}</Badge></div><p className="mt-3 text-xs leading-5 text-muted-foreground">{copy(language, "关闭此页后，在“管理客户端”中为每台设备复制完整地址。不要复制只有域名的服务基址。", "Close this sheet, then copy the complete per-device URL from Manage clients. Do not copy the hostname-only service base.")}</p><Button className="mt-3" disabled={busyAction !== null || !gatewayID} onClick={() => void configure()} size="sm" variant="outline">{busyAction === "configure" ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "同步订阅设置", "Sync subscription settings")}</Button></div> : null}{!ready ? <div className="flex flex-wrap gap-2"><Button disabled={busyAction !== null || !publication} onClick={() => void verify()} size="sm">{busyAction === "verify" ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "立即检查入口", "Check access now")}</Button>{commandFailed || publicationProblem ? <Button disabled={busyAction !== null || !gatewayID} onClick={() => void configure()} size="sm" variant="outline">{busyAction === "configure" ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{copy(language, "重试配置", "Retry configuration")}</Button> : null}</div> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={submit}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Alert><Globe2Icon /><AlertTitle>{copy(language, "推荐使用 Cloudflare 安全通道", "Cloudflare secure tunnel is recommended")}</AlertTitle><AlertDescription>{copy(language, "无需开放新的公网端口，并自动提供 HTTPS。", "It needs no new public port and provides HTTPS automatically.")}</AlertDescription></Alert><Field><FieldLabel htmlFor="subscription-hostname">{copy(language, "订阅域名（可自定义）", "Subscription hostname (customizable)")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="subscription-hostname" onChange={(event) => setHostname(event.target.value.toLowerCase())} placeholder={copy(language, "留空时自动生成", "Generated automatically when empty")} spellCheck={false} value={hostname} /><FieldDescription>{copy(language, "Center 默认生成不包含 3x-ui 或节点信息的 128-bit 随机域名；只用于订阅下载。", "Center generates a random 128-bit hostname without 3x-ui or node details by default; it is used only for subscription downloads.")}</FieldDescription></Field><details className="rounded-xl border p-3"><summary className="cursor-pointer text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}</summary><div className="mt-4 flex flex-col gap-4"><Field><FieldLabel htmlFor="subscription-kind">{copy(language, "公网方式", "Public method")}</FieldLabel><SelectControl id="subscription-kind" onValueChange={(value) => selectKind(value as "cloudflare_tunnel" | "public_direct")} options={[...(cloudflareReady && tunnelGateways.length ? [{ value: "cloudflare_tunnel", label: "Cloudflare Tunnel · HTTPS" }] : []), { value: "public_direct", label: copy(language, "公网网关 · HTTPS", "Public gateway · HTTPS"), disabled: !directGateways.length }]} value={kind} /></Field><Field><FieldLabel htmlFor="subscription-gateway">{copy(language, "入口节点", "Entry node")}</FieldLabel><SelectControl id="subscription-gateway" onValueChange={setGatewayID} options={[{ value: "", label: copy(language, "没有可用节点", "No node available"), disabled: true }, ...gateways.map((agent) => ({ value: agent.id, label: agent.name }))]} required value={gatewayID} /></Field></div></details>{!cloudflareReady ? <FieldError>{copy(language, "请先连接 Cloudflare，才能自动管理订阅域名和 HTTPS。", "Connect Cloudflare first to manage the subscription hostname and HTTPS automatically.")}</FieldError> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busyAction !== null || !cloudflareReady || !gatewayID} type="submit">{busyAction === "configure" ? <Spinner data-icon="inline-start" /> : <Globe2Icon data-icon="inline-start" />}{copy(language, "开启公网订阅", "Enable public subscription")}</Button></SheetFooter></form>}<SheetFooter>{command || publication ? <Button onClick={onClose}>{copy(language, "关闭", "Close")}</Button> : null}</SheetFooter></SheetContent></Sheet>;
}

function RealityRenameSheet({ data, language, mutate, onClose, service }: { data: AppData; language: Language; mutate: Mutate; onClose: () => void; service: Service | null }) {
	const [name, setName] = useState("");
	const [regionCode, setRegionCode] = useState("");
	const [regionMatch, setRegionMatch] = useState<"idle" | "matching" | "matched" | "manual" | "unavailable">("idle");
	const [command, setCommand] = useState<ApplicationCommand | null>(null);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState("");
	const regionRequest = useRef(0);
	const { execute } = useApplicationCommandExecutor(service?.id);
	const publication = service ? data.publications.find((value) => value.serviceId === service.id && value.kind === "public_shared_443" && value.status !== "stopped") : undefined;
	const application = service ? data.applications.find((value) => value.id === service.applicationId) : undefined;
	const gatewayID = publication?.ingress.entryNodeId ?? application?.nodeId ?? "";
	const gateway = data.agents.find((value) => value.id === gatewayID);
	useEffect(() => {
		if (!service) return;
		setName(regionBaseName(service.displayName || service.name, service.regionCode));
		setRegionCode(service.regionCode ?? "");
		setRegionMatch(service.regionCode ? "manual" : "idle");
		setCommand(null);
		setBusy(false);
		setError("");
	}, [service?.id]);
	useEffect(() => {
		const request = ++regionRequest.current;
		if (!service || service.regionCode || !gatewayID) return;
		setRegionMatch("matching");
		void api.agentRegionSuggestion(gatewayID).then((suggestion) => {
			if (request !== regionRequest.current) return;
			setRegionCode(suggestion.regionCode);
			setRegionMatch("matched");
		}).catch(() => {
			if (request !== regionRequest.current) return;
			setRegionMatch("unavailable");
		});
	}, [gatewayID, service?.id]);
	const submit = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		if (!service) return;
		setBusy(true);
		setError("");
		try {
			const next = await execute(() => api.renameRealityCommand(service.id, regionCode, name.trim()), setCommand);
			if (next?.state === "succeeded") {
				await mutate(async () => undefined, copy(language, `订阅节点已重命名为“${next.displayName ?? regionDisplayName(regionCode, name)}”。`, `Subscription node renamed to “${next.displayName ?? regionDisplayName(regionCode, name)}”.`));
			}
		} catch (submitError) {
			setError(userError(language, submitError));
		} finally {
			setBusy(false);
		}
	};
	const active = command?.state === "pending" || command?.state === "running";
	const displayName = regionDisplayName(regionCode, name);
	return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(service)}><SheetContent className="sm:max-w-md"><SheetHeader><SheetTitle>{copy(language, "修改订阅节点", "Edit subscription node")}</SheetTitle><SheetDescription>{copy(language, "地区前缀和名称会同步到 3x-ui、VLESS 链接和统一订阅；连接参数不会改变。", "The region prefix and name sync to 3x-ui, VLESS links, and shared subscriptions. Connection settings stay unchanged.")}</SheetDescription></SheetHeader>{command ? <div aria-live="polite" className="flex flex-1 flex-col gap-4 px-4"><Alert variant={command.state === "failed" ? "destructive" : "default"}>{active ? <Spinner /> : command.state === "succeeded" ? <CheckCircle2Icon /> : <ShieldAlertIcon />}<AlertTitle>{active ? copy(language, "正在同步名称…", "Syncing the name…") : command.state === "succeeded" ? copy(language, "名称已更新", "Name updated") : copy(language, "重命名失败", "Rename failed")}</AlertTitle><AlertDescription>{active ? copy(language, "等待订阅主机更新 3x-ui 入站。", "Waiting for the subscription controller to update the 3x-ui inbound.") : command.state === "succeeded" ? copy(language, `现在显示为“${command.displayName ?? displayName}”。客户端刷新订阅后会看到新名称。`, `It is now shown as “${command.displayName ?? displayName}”. Clients see it after refreshing the subscription.`) : command.error}</AlertDescription></Alert>{error ? <FieldError role="alert">{error}</FieldError> : null}</div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 px-4"><FieldGroup><Field><FieldLabel htmlFor="reality-rename-region">{copy(language, "地区前缀", "Region prefix")}</FieldLabel><RegionCombobox id="reality-rename-region" language={language} onValueChange={(code) => { regionRequest.current += 1; setRegionCode(code); setRegionMatch("manual"); }} value={regionCode} /><FieldDescription aria-live="polite">{regionMatch === "matching" ? copy(language, "正在根据公网 IP 识别…", "Detecting from the public IP…") : regionMatch === "matched" ? copy(language, `已根据 ${gateway?.networkProfile?.publicAddress ?? "公网 IP"} 自动匹配，可手动修改。`, `Matched from ${gateway?.networkProfile?.publicAddress ?? "the public IP"}; you can change it.`) : regionMatch === "unavailable" ? copy(language, "未能自动识别，请搜索并选择地区。", "Automatic detection was unavailable. Search and choose a region.") : copy(language, "支持搜索中文、英文和 ISO 国家/地区代码。", "Search by localized name, English name, or ISO code.")}</FieldDescription></Field><Field><FieldLabel htmlFor="reality-rename-name">{copy(language, "节点名称", "Node name")}</FieldLabel><Input id="reality-rename-name" maxLength={48} onChange={(event) => setName(event.target.value)} required value={name} /><FieldDescription>{copy(language, "填写线路或主机名，例如“洛杉矶 9929”。", "Enter the route or host name, for example “Los Angeles 9929”.")}</FieldDescription></Field>{displayName ? <div className="rounded-xl border bg-muted/40 p-3"><p className="text-xs text-muted-foreground">{copy(language, "订阅中显示", "Shown in subscriptions")}</p><p className="mt-1 font-medium">{displayName}</p></div> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !regionCode || !name.trim() || displayName === (service?.displayName || service?.name)} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <PencilIcon data-icon="inline-start" />}{copy(language, "保存名称", "Save name")}</Button></SheetFooter></form>}<SheetFooter>{command ? <Button disabled={active} onClick={onClose}>{copy(language, "完成", "Done")}</Button> : null}</SheetFooter></SheetContent></Sheet>;
}

function UninstallSheet({ application, app, language, onClose, onSubmit }: { application: Application | null; app?: AppView; language: Language; onClose: () => void; onSubmit: (application: Application, deleteData: boolean) => Promise<void> }) {
  const [deleteData, setDeleteData] = useState(false); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  useEffect(() => { if (application) { setDeleteData(false); setError(""); } }, [application]);
  const submit = async () => { if (!application) return; setBusy(true); setError(""); try { await onSubmit(application, deleteData); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(application)}><SheetContent><SheetHeader><SheetTitle>{copy(language, `卸载 ${app ? localized(app, language, "name") : application?.name ?? ""}`, `Uninstall ${app ? localized(app, language, "name") : application?.name ?? ""}`)}</SheetTitle><SheetDescription>{copy(language, "卸载会停止应用并移除所有访问入口。默认保留持久数据，便于以后重新安装。", "Uninstalling stops the app and removes all access points. Persistent data is kept by default for a later reinstall.")}</SheetDescription></SheetHeader><div className="px-4"><Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="delete-data">{copy(language, "同时永久删除应用数据", "Permanently delete app data too")}</FieldLabel><FieldDescription>{copy(language, "此操作不可恢复，包括配置、账号和历史数据。", "This cannot be undone and includes configuration, accounts, and history.")}</FieldDescription></div><Switch checked={deleteData} id="delete-data" onCheckedChange={setDeleteData} /></Field>{deleteData ? <Alert className="mt-4" variant="destructive"><Trash2Icon /><AlertTitle>{copy(language, "应用数据将永久删除", "App data will be permanently deleted")}</AlertTitle></Alert> : null}{error ? <FieldError className="mt-3">{error}</FieldError> : null}</div><SheetFooter><Button onClick={onClose} variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} onClick={() => void submit()} variant={deleteData ? "destructive" : "default"}>{busy ? <Spinner data-icon="inline-start" /> : null}{deleteData ? copy(language, "卸载并删除数据", "Uninstall and delete data") : copy(language, "卸载并保留数据", "Uninstall and keep data")}</Button></SheetFooter></SheetContent></Sheet>;
}
