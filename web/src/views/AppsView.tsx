import { useEffect, useMemo, useRef, useState } from "react";
import { AppWindowIcon, KeyRoundIcon, RotateCcwIcon } from "lucide-react";
import { api } from "../api";
import type { AppData, Mutate } from "../App";
import type { Application, Deployment, Service } from "../types";
import type { Language } from "../translations";
import { latestOperations, localized, operationLabel } from "./appAccess";
import { CopyButton, StateBadge, TechnicalError, copy } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Sheet } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { ThreeXUIClientsSheet } from "./ThreeXUIClientsSheet";
import { ThreeXUIControllerMigrationSheet } from "./ThreeXUIControllerMigrationSheet";
import { ThreeXUIInboundTrafficSheet } from "./ThreeXUIInboundTrafficSheet";
import { RealitySheet } from "./RealitySheet";
import { RealityRemoveDialog } from "./RealityRemoveDialog";
import { clearSecretOperation, deploymentSecretScope, readSecretOperation, secretOperation } from "../secret-delivery";
import { InstalledApps } from "./InstalledApps";
import { ApplicationStore } from "./ApplicationStore";
import { installedAppGroups } from "./installed-apps-model";
import { ApplicationCredentialsSheet, InstalledAppDetails } from "./apps/ApplicationDetails";
import { DeploymentSheet, PublicationSheet, type DeploymentEditor } from "./apps/DeploymentSheets";
import { RealityRenameSheet, SubscriptionSheet, UninstallSheet } from "./apps/SubscriptionSheets";
import { OfficialAppManagerHost } from "@/app-workspaces/OfficialAppWorkspaceHost";

type CredentialDelivery = NonNullable<Deployment["oneTimeCredentials"]> & { deploymentId: string; operationKey: string; scope: string };

export function AppsView({ data, language, mutate, workspaceKey, onOpenApp, onStore, onSettings }: { data: AppData; language: Language; mutate: Mutate; workspaceKey: string | null; onOpenApp: (key: string) => void; onStore: () => void; onSettings: () => void }) {
  const [deploymentEditor, setDeploymentEditor] = useState<DeploymentEditor>(null);
  const [publicationService, setPublicationService] = useState<Service | null>(null);
  const [uninstallApplication, setUninstallApplication] = useState<Application | null>(null);
	const [credentials, setCredentials] = useState<CredentialDelivery | undefined>(undefined);
	const [credentialAckBusy, setCredentialAckBusy] = useState(false);
	const credentialRecovery = useRef("");
	const [credentialApplication, setCredentialApplication] = useState<Application | null>(null);
	const [realityApplication, setRealityApplication] = useState<Application | null>(null);
	const [realityRenameService, setRealityRenameService] = useState<Service | null>(null);
  const [realityRemoveService, setRealityRemoveService] = useState<Service | null>(null);
  const [trafficService, setTrafficService] = useState<Service | null>(null);
  const [subscriptionApplication, setSubscriptionApplication] = useState<Application | null>(null);
  const [clientsApplication, setClientsApplication] = useState<Application | null>(null);
  const [migrationApplication, setMigrationApplication] = useState<Application | null>(null);
	const [appManagerApplication, setAppManagerApplication] = useState<Application | null>(null);
	const [managedApplicationID, setManagedApplicationID] = useState<string | null>(null);
	const [recoveringTask, setRecoveringTask] = useState("");
  const catalogByKey = useMemo(() => new Map(data.apps.map((app) => [app.key, app])), [data.apps]);
  const installedGroups = useMemo(() => installedAppGroups({
    applications: data.applications, apps: data.apps, agents: data.agents, sites: data.sites,
    services: data.services, publications: data.publications, deployments: data.deployments,
    threeXUIControllerMigrations: data.threeXUIControllerMigrations,
  }), [data.applications, data.apps, data.agents, data.sites, data.services, data.publications, data.deployments, data.threeXUIControllerMigrations]);
  const selectedGroup = installedGroups.find((group) => group.appKey === workspaceKey);
  const managedInstance = installedGroups.flatMap((group) => group.instances).find((instance) => instance.application.id === managedApplicationID);
  const recentOperations = latestOperations(data.deployments).filter((deployment) => deployment.state === "pending" || deployment.state === "running" || deployment.reconciliationRequired);
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
			await mutate(() => api.acknowledgeDeploymentCredentials(credentials.deploymentId, credentials.operationKey), credentials.setupToken ? copy(language, "Pulse 初始化令牌已确认保存。", "Pulse setup token was acknowledged.") : copy(language, "Vastora Proxy 管理账号已确认保存。", "Vastora Proxy administrator credentials were acknowledged."));
			clearSecretOperation(credentials.scope);
			credentialRecovery.current = "";
			setCredentials(undefined);
		} finally {
			setCredentialAckBusy(false);
		}
	};

  return (
    <section className="apps-workspace flex min-w-0 flex-col gap-5" data-mode={workspaceKey ? "workspace" : "store"}>

      {credentials ? <Alert><KeyRoundIcon /><AlertTitle>{credentials.setupToken ? copy(language, "请保存 Pulse 初始化令牌", "Save the Pulse setup token") : copy(language, "请保存 Vastora Proxy 管理账号", "Save the Vastora Proxy administrator account")}</AlertTitle><AlertDescription><p>{copy(language, "确认保存前可在断线或刷新后重新领取；确认后 Center 会永久关闭再次显示。", "Until you acknowledge it, the same browser can recover these credentials after a disconnect or refresh. Acknowledgement permanently disables disclosure.")}</p>{credentials.setupToken ? <div className="mt-3 rounded-lg bg-muted p-3 text-sm"><p className="text-muted-foreground">{copy(language, "首次访问 Pulse /login 时使用", "Use for the first Pulse /login setup")}</p><div className="mt-1 flex items-center gap-2 break-all font-mono">{credentials.setupToken}<CopyButton language={language} value={credentials.setupToken} /></div></div> : <dl className="mt-3 grid gap-2 rounded-lg bg-muted p-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">{copy(language, "账号", "Username")}</dt><dd className="mt-1 flex items-center gap-2 font-mono">{credentials.username}<CopyButton language={language} value={credentials.username || ""} /></dd></div><div><dt className="text-muted-foreground">{copy(language, "密码", "Password")}</dt><dd className="mt-1 flex items-center gap-2 break-all font-mono">{credentials.password}<CopyButton language={language} value={credentials.password || ""} /></dd></div></dl>}<Button className="mt-3" disabled={credentialAckBusy} onClick={() => void acknowledgeCredentials()} size="sm" variant="outline">{credentialAckBusy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "我已保存并关闭再次显示", "Saved — disable further disclosure")}</Button></AlertDescription></Alert> : null}

      {recentOperations.length ? <details className="rounded-xl border bg-muted/25 p-3" open={recentOperations.some((operation) => operation.reconciliationRequired)}><summary className="cursor-pointer text-sm font-medium">{copy(language, `进行中的任务 · ${recentOperations.length}`, `Active tasks · ${recentOperations.length}`)}</summary><div aria-live="polite" className="mt-3 flex flex-col gap-3">{recentOperations.map((deployment) => {
        const app = catalogByKey.get(deployment.appKey); const agent = data.agents.find((value) => value.id === deployment.agentId); const application = data.applications.find((value) => value.id === deployment.applicationId);
        const retry = () => { if (!app || !agent) return; if (deployment.operation === "uninstall" && application) setUninstallApplication(application); else if (deployment.operation === "upgrade" || deployment.operation === "configure") setDeploymentEditor({ app, agent, operation: deployment.operation }); else setDeploymentEditor({ app, agent, operation: "install" }); };
	        const recover = async () => {
	          setRecoveringTask(deployment.id);
	          try {
	            await mutate(() => api.retryTaskReconciliation(deployment.id), copy(language, "恢复任务已重新排队。", "The recovery task was queued again."));
	          } finally {
	            setRecoveringTask("");
	          }
	        };
	        return <Card key={deployment.id} size="sm"><CardContent className="flex flex-col gap-3 py-4 sm:flex-row sm:items-center"><StateBadge language={language} value={deployment.reconciliationRequired ? "recovery" : deployment.state} /><div className="min-w-0 flex-1"><p className="font-medium">{operationLabel(language, deployment.operation)} · {app ? localized(app, language, "name") : deployment.appKey}</p><p className="mt-1 text-xs text-muted-foreground">{agent?.name ?? deployment.agentId}</p>{deployment.reconciliationRequired ? <p className="mt-2 text-sm text-amber-700 dark:text-amber-300">{copy(language, "节点状态未能完全确认。系统已锁定这项应用，继续恢复会复用原任务，不会重复安装。", "The node state could not be fully confirmed. This app is locked; continuing recovery reuses the original task and will not install a duplicate.")}</p> : null}{deployment.error ? <div className="mt-2"><TechnicalError error={deployment.error} language={language} /></div> : null}</div>{deployment.reconciliationRequired ? <Button disabled={recoveringTask === deployment.id} onClick={() => void recover()} size="sm" variant="outline">{recoveringTask === deployment.id ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{copy(language, "继续恢复", "Continue recovery")}</Button> : deployment.state === "failed" && app && agent ? <Button onClick={retry} size="sm" variant="outline"><RotateCcwIcon data-icon="inline-start" />{copy(language, "重试", "Retry")}</Button> : null}</CardContent></Card>;
      })}</div></details> : null}

      {workspaceKey ? selectedGroup ? <InstalledApps key={selectedGroup.id} group={selectedGroup} data={data} language={language} mutate={mutate} managerApplication={appManagerApplication?.appKey === "vastora-official/meridian" ? appManagerApplication : null} onManagerClose={() => setAppManagerApplication(null)} onClients={(application) => { if (application.appKey === "vastora-official/meridian") setAppManagerApplication(application); else setClientsApplication(application); }} onManage={(application) => setManagedApplicationID(application.id)} onUpgrade={(application) => openChange(application, "upgrade")} onReality={setRealityApplication} />
        : <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><AppWindowIcon /></EmptyMedia><EmptyTitle>{copy(language, "此应用尚未安装或已移除", "This app is not installed")}</EmptyTitle><EmptyDescription>{copy(language, "在应用商店查看当前可用的应用。", "Visit the App Store to see available apps.")}</EmptyDescription><Button onClick={onStore}>{copy(language, "打开应用商店", "Open App Store")}</Button></EmptyHeader></Empty>
        : <ApplicationStore data={data} groups={installedGroups} language={language} onInstall={(app) => setDeploymentEditor({ app, operation: "install" })} onOpen={onOpenApp} onManage={(application) => setManagedApplicationID(application.id)} onUpgrade={(application) => openChange(application, "upgrade")} onSettings={onSettings} />}

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
	          onAppManager={() => openFromDetails(() => { setAppManagerApplication(managedInstance.application); if (managedInstance.application.appKey === "vastora-official/meridian") onOpenApp(managedInstance.application.appKey); })}
          onPublish={(service) => openFromDetails(() => setPublicationService(service))}
          onReality={() => openFromDetails(() => setRealityApplication(managedInstance.application))}
          onRenameReality={(service) => openFromDetails(() => setRealityRenameService(service))}
          onRemoveReality={(service) => openFromDetails(() => setRealityRemoveService(service))}
          onSubscription={() => openFromDetails(() => setSubscriptionApplication(managedInstance.application))}
          onTraffic={(service) => openFromDetails(() => setTrafficService(service))}
          onUninstall={() => openFromDetails(() => setUninstallApplication(managedInstance.application))}
          onUpgrade={() => openFromDetails(() => openChange(managedInstance.application, "upgrade"))}
        /> : null}
      </Sheet>
      <DeploymentSheet data={data} editor={deploymentEditor} language={language} onClose={() => setDeploymentEditor(null)} onSubmit={async (agent, app, config, operation, role, registryCredentialId, authorizedCapabilities) => {
        let result: Deployment | undefined;
		const scope = deploymentSecretScope(agent.id, app.key, operation);
		const operationKey = app.key === "vastora-official/3x-ui" && operation === "install" && role !== "worker" || app.key === "vastora-official/pulse" && (operation === "install" || operation === "upgrade") ? secretOperation(scope) : undefined;
        const messages = { install: copy(language, "安装任务已创建。可在活动中查看进度。", "Install task created. Follow progress in Activity."), upgrade: copy(language, "升级任务已创建。", "Upgrade task created."), configure: copy(language, "配置任务已创建。", "Configuration task created.") };
        await mutate(async () => { result = await api.createDeployment(agent.id, app.key, config, operation, false, role, registryCredentialId, operationKey, authorizedCapabilities, operation === "configure" ? undefined : { packageRevision: app.app.packageRevision ?? 0, manifestSha256: app.manifestSha256 ?? "" }); }, messages[operation]);
        if (result?.oneTimeCredentials && operationKey) setCredentials({ ...result.oneTimeCredentials, deploymentId: result.id, operationKey, scope });
		else if (result && operationKey) clearSecretOperation(scope);
        setDeploymentEditor(null);
      }} />
      <PublicationSheet data={data} language={language} onClose={() => setPublicationService(null)} onSubmit={async (input) => {
		const publicCPAAPI = publicationService?.name === "client-api" && data.applications.find((value) => value.id === publicationService.applicationId)?.appKey === "vastora-official/cpa";
		await mutate(() => api.createPublication(input), publicCPAAPI ? copy(language, "公网 API 已开启。", "Public API enabled.") : copy(language, "访问入口已创建。", "Access point created."), { reportError: false });
		setPublicationService(null);
	}} service={publicationService} />
		<RealitySheet application={realityApplication} data={data} language={language} onClose={() => setRealityApplication(null)} siteTimezone={realityApplication ? data.sites.find((site) => site.id === realityApplication.siteId)?.timezone : undefined} />
		<RealityRenameSheet data={data} language={language} mutate={mutate} onClose={() => setRealityRenameService(null)} service={realityRenameService} />
      {realityRemoveService ? <RealityRemoveDialog key={realityRemoveService.id} service={realityRemoveService} language={language} mutate={mutate} onClose={() => setRealityRemoveService(null)} /> : null}
      <ThreeXUIInboundTrafficSheet controller={trafficController ?? null} language={language} onClose={() => setTrafficService(null)} service={trafficService} siteTimezone={trafficService ? data.sites.find((site) => site.id === trafficService.siteId)?.timezone : undefined} />
      <SubscriptionSheet application={subscriptionApplication} data={data} language={language} mutate={mutate} onClose={() => setSubscriptionApplication(null)} />
	      <ThreeXUIClientsSheet advancedURL={clientsApplication ? data.deployments.find((value) => value.applicationId === clientsApplication.id && value.state === "succeeded" && value.operation !== "uninstall")?.accessUrl : undefined} application={clientsApplication} language={language} onClose={() => setClientsApplication(null)} siteTimezone={clientsApplication ? data.sites.find((site) => site.id === clientsApplication.siteId)?.timezone : undefined} />
	      <ApplicationCredentialsSheet application={credentialApplication} language={language} onClose={() => setCredentialApplication(null)} />
	      <ThreeXUIControllerMigrationSheet application={migrationApplication} data={data} language={language} mutate={mutate} onClose={() => setMigrationApplication(null)} />
      {appManagerApplication && appManagerApplication.appKey !== "vastora-official/meridian" ? <OfficialAppManagerHost application={appManagerApplication} data={data} language={language} mutate={mutate} onClose={() => setAppManagerApplication(null)} version={catalogByKey.get("vastora-official/meridian")?.app.version} /> : null}
      <UninstallSheet application={uninstallApplication} app={uninstallApplication ? catalogByKey.get(uninstallApplication.appKey) : undefined} language={language} onClose={() => setUninstallApplication(null)} onSubmit={async (application, deleteData) => { await mutate(() => api.createDeployment(application.nodeId, application.appKey, {}, "uninstall", deleteData), copy(language, "卸载任务已创建。", "Uninstall task created.")); setUninstallApplication(null); }} />
    </section>
  );
}
