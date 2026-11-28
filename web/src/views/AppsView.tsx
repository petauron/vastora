import { useEffect, useMemo, useRef, useState } from "react";
import { AppWindowIcon, CrosshairIcon, KeyRoundIcon } from "lucide-react";
import { api, isAPIError } from "../api";
import type { AppData, Mutate, Screen } from "../App";
import type { AgentView, AppView, Application, Deployment, Service, ThreeXUIRole } from "../types";
import type { Language } from "../translations";
import { latestOperations, localized, operationLabel, packageNodeBlocker } from "./appAccess";
import { CopyButton, StateBadge, TechnicalError, copy, catalogInstallBlocked, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { AppIcon } from "@/components/desktop/AppIcon";
import { ThreeXUIClientsSheet } from "./ThreeXUIClientsSheet";
import { ThreeXUIControllerMigrationSheet } from "./ThreeXUIControllerMigrationSheet";
import { ThreeXUIInboundTrafficSheet } from "./ThreeXUIInboundTrafficSheet";
import { RealitySheet } from "./RealitySheet";
import { RealityRemoveDialog } from "./RealityRemoveDialog";
import { clearSecretOperation, deploymentSecretScope, readSecretOperation, secretOperation } from "../secret-delivery";
import { InstalledApps } from "./InstalledApps";
import { ApplicationStore, type StoreNavigationTarget } from "./ApplicationStore";
import { installedAppGroups, type InstalledAppInstance } from "./installed-apps-model";
import { ApplicationCredentialsSheet, InstalledAppDetails } from "./apps/ApplicationDetails";
import { BatchUpgradeSheet, createBatchUpgradeRequest, type BatchUpgradeOutcome, type BatchUpgradeRequest, type BatchUpgradeTarget } from "./apps/BatchUpgradeSheet";
import { upgradeApprovalCapabilities } from "./apps/capabilities";
import { DeploymentSheet, PublicationSheet, type DeploymentEditor } from "./apps/DeploymentSheets";
import { RealityRenameSheet, SubscriptionSheet, UninstallSheet } from "./apps/SubscriptionSheets";
import { OfficialAppManagerHost } from "@/app-workspaces/OfficialAppWorkspaceHost";
import { daylightInstalledGroup, isDaylightAppKey } from "./daylight";
import { TaskRecoverySheet } from "./TaskRecoverySheet";
import { glivelaControllerKey, glivelaRoleLabel, isGlivelaAppKey, suiteIdForAppKey } from "./apps/store-suites-model";

type CredentialDelivery = NonNullable<Deployment["oneTimeCredentials"]> & { deploymentId: string; operationKey: string; scope: string };

export function AppsView({ data, language, mutate, workspaceKey, onNavigate, onOpenApp, onStore, onSettings, onRefresh, refreshing = false, active = true }: { data: AppData; language: Language; mutate: Mutate; active?: boolean; workspaceKey: string | null; onNavigate?: (screen: Screen) => void; onOpenApp: (key: string) => void; onStore: () => void; onSettings: () => void; onRefresh?: () => void; refreshing?: boolean }) {
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
	const [recoveryTask, setRecoveryTask] = useState<Deployment | null>(null);
	const [batchUpgrade, setBatchUpgrade] = useState<BatchUpgradeRequest | null>(null);
	const [tasksSheetOpen, setTasksSheetOpen] = useState(false);
  const [storeNavTarget, setStoreNavTarget] = useState<StoreNavigationTarget | null>(null);
  const catalogByKey = useMemo(() => new Map(data.apps.map((app) => [app.key, app])), [data.apps]);
  const installedGroups = useMemo(() => installedAppGroups({
    applications: data.applications, apps: data.apps, agents: data.agents, sites: data.sites,
    services: data.services, publications: data.publications, deployments: data.deployments,
    threeXUIControllerMigrations: data.threeXUIControllerMigrations,
  }), [data.applications, data.apps, data.agents, data.sites, data.services, data.publications, data.deployments, data.threeXUIControllerMigrations]);
  const installedInstancesByID = useMemo(
    () => new Map(installedGroups.flatMap((group) => group.instances).map((instance) => [instance.application.id, instance])),
    [installedGroups],
  );
  const selectedGroup = workspaceKey && isDaylightAppKey(workspaceKey) ? daylightInstalledGroup(installedGroups) : installedGroups.find((group) => group.appKey === workspaceKey);
  const managedInstance = managedApplicationID ? installedInstancesByID.get(managedApplicationID) : undefined;
  const recentOperations = latestOperations(data.deployments).filter((deployment) => deployment.state === "pending" || deployment.state === "running" || deployment.reconciliationRequired);
  const recoveryOperations = recentOperations.filter((deployment) => deployment.reconciliationRequired);
  const inProgressOperations = recentOperations.filter((deployment) => !deployment.reconciliationRequired);
  const trafficApplication = trafficService ? data.applications.find((application) => application.id === trafficService.applicationId) : undefined;
  const trafficController = data.applications.find((application) => application.id === trafficApplication?.controllerApplicationId && application.role === "master" && application.status === "running");

  const upgradeRequests = useRef(new Set<string>());
  const [upgradeError, setUpgradeError] = useState("");
  const [submittingUpgrade, setSubmittingUpgrade] = useState<string | null>(null);
  const submitDeployment = async (
    agent: AgentView, app: AppView, config: Record<string, string | boolean | number>,
    operation: "install" | "upgrade" | "configure", role?: ThreeXUIRole,
    registryCredentialId?: string, authorizedCapabilities?: string[], backupsConfirmed?: boolean,
  ) => {
    let result: Deployment | undefined;
    const scope = deploymentSecretScope(agent.id, app.key, operation);
    const operationKey = app.key === "vastora-official/3x-ui" && operation === "install" && role !== "worker" || app.key === "vastora-official/senlun" && (operation === "install" || operation === "upgrade") ? secretOperation(scope) : undefined;
    const messages = { install: copy(language, "安装任务已创建。可在活动中查看进度。", "Install task created. Follow progress in Activity."), upgrade: copy(language, "升级任务已创建。", "Upgrade task created."), configure: copy(language, "配置任务已创建。", "Configuration task created.") };
    await mutate(async () => { result = await api.createDeployment(agent.id, app.key, config, operation, false, role, registryCredentialId, operationKey, authorizedCapabilities, operation === "configure" ? undefined : { packageRevision: app.app.packageRevision ?? 0, manifestSha256: app.manifestSha256 ?? "", ...(backupsConfirmed ? { backupsConfirmed: true } : {}) }); }, messages[operation], { reportError: false });
    if (result?.oneTimeCredentials && operationKey) setCredentials({ ...result.oneTimeCredentials, deploymentId: result.id, operationKey, scope });
    else if (result && operationKey) clearSecretOperation(scope);
  };
  const upgrade = async (application: Application, app: AppView, agent: AgentView) => {
    if (upgradeRequests.current.has(application.id)) return;
    upgradeRequests.current.add(application.id);
    setSubmittingUpgrade(application.id);
    setUpgradeError("");
    try {
      await submitDeployment(agent, app, {}, "upgrade");
    } catch (error) {
      // Only missing settings need an upgrade form; other failures stay inline.
      if (error instanceof Error && /configuration field "[^"]+" is required/.test(error.message)) {
        setDeploymentEditor({ app, agent, operation: "upgrade", initialError: error.message });
      } else {
        setUpgradeError(userError(language, error));
      }
    } finally {
      upgradeRequests.current.delete(application.id);
      setSubmittingUpgrade((current) => current === application.id ? null : current);
    }
  };
  const openBatchUpgrade = (title: string, instances: InstalledAppInstance[], initialSelectedIds?: string[]) => {
    setUpgradeError("");
    setBatchUpgrade(createBatchUpgradeRequest(title, instances, data, initialSelectedIds));
  };
  const submitBatchUpgrade = async (
    targets: BatchUpgradeTarget[],
    approvedCapabilities: string[],
    onProgress: (done: number, total: number) => void,
  ): Promise<BatchUpgradeOutcome> => {
    let queued = 0;
    const failed: BatchUpgradeOutcome["failed"] = [];
    for (let index = 0; index < targets.length; index++) {
      const target = targets[index];
      const pending = upgradeApprovalCapabilities(target.app, target.instance.application);
      const authorizedCapabilities = pending.length > 0
        ? pending.filter((capability) => approvedCapabilities.includes(capability))
        : undefined;
      const scope = deploymentSecretScope(target.agent.id, target.app.key, "upgrade");
      const operationKey = target.app.key === "vastora-official/senlun" ? secretOperation(scope) : undefined;
      try {
        const result = await api.createDeployment(
          target.agent.id,
          target.app.key,
          {},
          "upgrade",
          false,
          undefined,
          undefined,
          operationKey,
          authorizedCapabilities,
          { packageRevision: target.app.app.packageRevision ?? 0, manifestSha256: target.app.manifestSha256 ?? "" },
        );
        if (result?.oneTimeCredentials && operationKey) {
          setCredentials({ ...result.oneTimeCredentials, deploymentId: result.id, operationKey, scope });
        } else if (operationKey) {
          clearSecretOperation(scope);
        }
        queued++;
      } catch (error) {
        if (isAPIError(error) && error.status === 401) {
          await mutate(async () => { throw error; }, undefined, { reportError: false }).catch(() => undefined);
          return { queued, failed };
        }
        failed.push({
          applicationId: target.instance.application.id,
          name: target.label,
          error: userError(language, error),
        });
      }
      onProgress(index + 1, targets.length);
    }
    if (queued > 0) {
      const summary = failed.length === 0
        ? copy(language, `已提交 ${queued} 台更新任务。`, `Queued updates for ${queued} host(s).`)
        : undefined;
      await mutate(async () => undefined, summary, { reportError: false });
    }
    return { queued, failed };
  };
  const openChange = (application: Application, operation: "upgrade" | "configure") => {
    const app = catalogByKey.get(application.appKey);
    const agent = data.agents.find((value) => value.id === application.nodeId);
    if (!app || !agent) return;
    if (operation === "configure") { setDeploymentEditor({ app, agent, operation }); return; }
    if (application.adoptionState === "pending" || application.adoptionState === "blocked") {
      setManagedApplicationID(application.id);
      return;
    }
    if (data.deployments.some((task) => task.agentId === agent.id && task.appKey === app.key &&
      (task.state === "pending" || task.state === "running" || task.reconciliationRequired))) return;
    const blocker = packageNodeBlocker(agent, app, language);
    if (!agent.connected || agent.credentialRevoked || blocker || catalogInstallBlocked(app)) {
      setUpgradeError(blocker || copy(language, "请先确认节点在线并刷新应用目录。", "Check node connectivity and refresh the app catalog first."));
      return;
    }
    if (upgradeApprovalCapabilities(app, application).length > 0) {
      setDeploymentEditor({ app, agent, operation });
    } else {
      void upgrade(application, app, agent);
    }
  };

  const openFromDetails = (action: () => void) => {
    setManagedApplicationID(null);
    action();
  };

	useEffect(() => {
		if (!active || credentials) return;
		let cancelled = false;
		for (const deployment of data.deployments) {
			if (!deployment.oneTimeCredentialsAvailable) continue;
			const scope = deploymentSecretScope(deployment.agentId, deployment.appKey, deployment.operation);
			const operationKey = readSecretOperation(scope);
			if (!operationKey) continue;
			const recoveryKey = `${deployment.id}:${operationKey}`;
			if (credentialRecovery.current === recoveryKey) return;
			credentialRecovery.current = recoveryKey;
			void api.revealDeploymentCredentials(deployment.id, operationKey).then((value) => {
				if (!cancelled) setCredentials({ ...value, deploymentId: deployment.id, operationKey, scope });
			}).catch(() => { /* The next screen refresh may retry the same durable delivery. */ }).finally(() => {
				if (credentialRecovery.current === recoveryKey) credentialRecovery.current = "";
			});
			return () => { cancelled = true; };
		}
	}, [active, credentials, data.deployments]);

	const acknowledgeCredentials = async () => {
		if (!credentials) return;
		setCredentialAckBusy(true);
		try {
			await mutate(() => api.acknowledgeDeploymentCredentials(credentials.deploymentId, credentials.operationKey), copy(language, "Vastora Proxy 管理账号已确认保存。", "Vastora Proxy administrator credentials were acknowledged."));
			clearSecretOperation(credentials.scope);
			credentialRecovery.current = "";
			setCredentials(undefined);
		} finally {
			setCredentialAckBusy(false);
		}
	};

  const groupedActiveTasks = useMemo(() => {
    const siteByID = new Map(data.sites.map((site) => [site.id, site.name]));
    const agentByID = new Map(data.agents.map((agent) => [agent.id, agent]));
    const groups = new Map<string, {
      suiteId: string;
      iconKey: string;
      name: string;
      running: number;
      pending: number;
      tasks: Array<{ deployment: Deployment; agentName: string; siteName: string; roleLabel: string }>;
    }>();
    for (const deployment of inProgressOperations) {
      const suiteId = suiteIdForAppKey(deployment.appKey);
      const app = catalogByKey.get(deployment.appKey);
      const agent = agentByID.get(deployment.agentId);
      const agentName = agent?.name ?? deployment.agentId;
      const siteName = agent ? (siteByID.get(agent.siteId) ?? agent.siteId) : "";
      const roleLabel = isGlivelaAppKey(deployment.appKey) ? glivelaRoleLabel(deployment.appKey, language, true) : "";
      const suiteName = isGlivelaAppKey(deployment.appKey)
        ? "Glivela"
        : isDaylightAppKey(deployment.appKey)
          ? "Daylight"
          : app ? localized(app, language, "name") : deployment.appKey;
      const existing = groups.get(suiteId);
      if (existing) {
        if (deployment.state === "running") existing.running += 1;
        else existing.pending += 1;
        existing.tasks.push({ deployment, agentName, siteName, roleLabel });
      } else {
        groups.set(suiteId, {
          suiteId,
          iconKey: suiteId,
          name: suiteName,
          running: deployment.state === "running" ? 1 : 0,
          pending: deployment.state === "running" ? 0 : 1,
          tasks: [{ deployment, agentName, siteName, roleLabel }],
        });
      }
    }
    return [...groups.values()];
  }, [inProgressOperations, data.sites, data.agents, catalogByKey, language]);

  const activeTasksControl = inProgressOperations.length > 0 ? (
    <Button
      aria-label={copy(language, `查看 ${inProgressOperations.length} 个进行中的任务`, `View ${inProgressOperations.length} active tasks`)}
      className="shrink-0 tabular-nums"
      onClick={() => setTasksSheetOpen(true)}
      size="sm"
      variant="outline"
    >
      <Spinner aria-hidden="true" className="motion-reduce:animate-none" data-icon="inline-start" />
      {copy(language, `进行中 · ${inProgressOperations.length}`, `Active · ${inProgressOperations.length}`)}
    </Button>
  ) : null;

  return (
    <section className="apps-workspace flex min-w-0 flex-col gap-5" data-mode={workspaceKey ? "workspace" : "store"}>
      {upgradeError ? <Alert variant="destructive" role="alert"><AlertTitle className="flex items-start justify-between gap-3"><span>{copy(language, "更新请求需要检查", "Check the update request")}</span><Button aria-label={copy(language, "关闭错误提示", "Dismiss error")} onClick={() => setUpgradeError("")} size="xs" variant="ghost">{copy(language, "关闭", "Dismiss")}</Button></AlertTitle><AlertDescription>{upgradeError}</AlertDescription></Alert> : null}

      {credentials ? <Alert><KeyRoundIcon /><AlertTitle>{copy(language, "请保存 Vastora Proxy 管理账号", "Save the Vastora Proxy administrator account")}</AlertTitle><AlertDescription><p>{copy(language, "确认保存前可在断线或刷新后重新领取；确认后 Center 会永久关闭再次显示。", "Until you acknowledge it, the same browser can recover these credentials after a disconnect or refresh. Acknowledgement permanently disables disclosure.")}</p><dl className="mt-3 grid gap-2 rounded-lg bg-muted p-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">{copy(language, "账号", "Username")}</dt><dd className="mt-1 flex items-center gap-2 font-mono">{credentials.username}<CopyButton language={language} value={credentials.username || ""} /></dd></div><div><dt className="text-muted-foreground">{copy(language, "密码", "Password")}</dt><dd className="mt-1 flex items-center gap-2 break-all font-mono">{credentials.password}<CopyButton language={language} value={credentials.password || ""} /></dd></div></dl><Button className="mt-3" disabled={credentialAckBusy} onClick={() => void acknowledgeCredentials()} size="sm" variant="outline">{credentialAckBusy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "我已保存并关闭再次显示", "Saved — disable further disclosure")}</Button></AlertDescription></Alert> : null}

      {recoveryOperations.length ? <details className="rounded-xl border bg-muted/25 p-3" open><summary className="cursor-pointer text-sm font-medium">{copy(language, `需要核对并恢复 · ${recoveryOperations.length}`, `Recovery required · ${recoveryOperations.length}`)}</summary><div aria-live="polite" className="mt-3 flex flex-col gap-3">{recoveryOperations.map((deployment) => {
        const app = catalogByKey.get(deployment.appKey); const agent = data.agents.find((value) => value.id === deployment.agentId);
        return <Card key={deployment.id} size="sm"><CardContent className="flex flex-col gap-3 py-4 sm:flex-row sm:items-center"><StateBadge language={language} value="recovery" /><div className="min-w-0 flex-1"><p className="font-medium">{operationLabel(language, deployment.operation)} · {app ? localized(app, language, "name") : deployment.appKey}</p><p className="mt-1 text-xs text-muted-foreground">{agent?.name ?? deployment.agentId}</p><p className="mt-2 text-sm text-muted-foreground">{copy(language, "应用结果尚未确认，当前变更已锁定。先核对执行记录与实际资源，再决定是否重新执行；打开核对不会重试任务。", "The app outcome is unconfirmed and changes are locked. Review the execution and actual resources before deciding whether to execute again; opening review does not retry the task.")}</p>{deployment.error ? <div className="mt-2"><TechnicalError error={deployment.error} language={language} /></div> : null}</div><Button onClick={() => setRecoveryTask(deployment)} size="sm" variant="outline">{copy(language, "核对并恢复", "Review and recover")}</Button></CardContent></Card>;
      })}</div></details> : null}

      {recoveryTask ? <TaskRecoverySheet key={recoveryTask.id} taskId={recoveryTask.id} agentId={recoveryTask.agentId} name={data.agents.find((agent) => agent.id === recoveryTask.agentId)?.name ?? recoveryTask.agentId} language={language} onClose={() => setRecoveryTask(null)} onSaved={() => { setRecoveryTask(null); void mutate(async () => undefined, copy(language, "任务处置已记录。", "Task disposition recorded.")); }} /> : null}

      {workspaceKey ? selectedGroup ? <InstalledApps key={selectedGroup.id} group={selectedGroup} data={data} language={language} mutate={mutate} activeTasksControl={activeTasksControl} submittingUpgradeID={submittingUpgrade} managerApplication={appManagerApplication?.appKey === "vastora-official/meridian" ? appManagerApplication : null} onManagerClose={() => setAppManagerApplication(null)} onClients={(application) => { if (application.appKey === "vastora-official/meridian") setAppManagerApplication(application); else setClientsApplication(application); }} onManage={(application) => setManagedApplicationID(application.id)} onUpgrade={(application) => openChange(application, "upgrade")} onBatchUpgrade={openBatchUpgrade} onReality={setRealityApplication} />
        : <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><AppWindowIcon /></EmptyMedia><EmptyTitle>{copy(language, "此应用尚未安装或已移除", "This app is not installed")}</EmptyTitle><EmptyDescription>{copy(language, "在应用商店查看当前可用的应用。", "Visit the App Store to see available apps.")}</EmptyDescription><Button onClick={onStore}>{copy(language, "打开应用商店", "Open App Store")}</Button></EmptyHeader></Empty>
        : <ApplicationStore
            activeTasksControl={activeTasksControl}
            data={data}
            groups={installedGroups}
            language={language}
            mutate={mutate}
            navigationTarget={storeNavTarget}
            onBatchUpgrade={openBatchUpgrade}
            onInstall={(app) => setDeploymentEditor({ app, operation: "install" })}
            onManage={(application) => setManagedApplicationID(application.id)}
            onOpen={onOpenApp}
            onRecoverTask={setRecoveryTask}
            onRefresh={onRefresh}
            onSettings={onSettings}
            onUpgrade={(application) => openChange(application, "upgrade")}
            refreshing={refreshing}
            submittingUpgradeID={submittingUpgrade}
          />}

      <Sheet onOpenChange={setTasksSheetOpen} open={tasksSheetOpen}>
        <SheetContent className="sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>{copy(language, `进行中的任务 · ${inProgressOperations.length}`, `Active tasks · ${inProgressOperations.length}`)}</SheetTitle>
            <SheetDescription>{copy(language, "节点执行完成后会自动更新状态，关闭此窗口不影响任务继续执行。", "Status updates automatically as hosts finish. Closing this dialog does not interrupt running tasks.")}</SheetDescription>
          </SheetHeader>
          <SheetBody aria-live="polite" className="gap-4">
            {groupedActiveTasks.length > 0 ? groupedActiveTasks.map((group) => (
              <section className="overflow-hidden rounded-xl border bg-card" key={group.suiteId}>
                <div className="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/35 px-3.5 py-2.5">
                  <div className="flex min-w-0 items-center gap-2">
                    <AppIcon appKey={group.iconKey} className="size-6 shrink-0" />
                    <h3 className="truncate text-sm font-medium">{group.name}</h3>
                  </div>
                  <div className="flex items-center gap-1.5 tabular-nums">
                    {group.running > 0 ? (
                      <Badge variant="outline">
                        <Spinner aria-hidden="true" className="motion-reduce:animate-none" data-icon="inline-start" />
                        {copy(language, `执行中 ${group.running}`, `Running ${group.running}`)}
                      </Badge>
                    ) : null}
                    {group.pending > 0 ? (
                      <Badge variant="secondary">
                        {copy(language, `等待中 ${group.pending}`, `Queued ${group.pending}`)}
                      </Badge>
                    ) : null}
                  </div>
                </div>
                <div className="divide-y">
                  {group.tasks.map(({ deployment, agentName, siteName, roleLabel }) => (
                    <div className="flex flex-wrap items-center gap-2.5 px-3.5 py-2.5" key={deployment.id}>
                      <StateBadge language={language} value={deployment.state} />
                      <div className="min-w-0 flex-1">
                        <p className="truncate text-sm font-medium">
                          {operationLabel(language, deployment.operation)} · {agentName}
                          {roleLabel ? <span className="ml-1 font-normal text-muted-foreground">({roleLabel})</span> : null}
                        </p>
                        {siteName ? <p className="text-xs text-muted-foreground">{siteName}</p> : null}
                        {deployment.error ? <div className="mt-1.5"><TechnicalError error={deployment.error} language={language} /></div> : null}
                      </div>
                      {!workspaceKey ? (
                        <Button
                          aria-label={copy(language, `在 ${group.name} 中定位 ${agentName}`, `Locate ${agentName} in ${group.name}`)}
                          onClick={() => {
                            setTasksSheetOpen(false);
                            const filterToHostRow = deployment.operation !== "install"
                              && deployment.appKey !== glivelaControllerKey;
                            setStoreNavTarget((current) => ({
                              suiteId: group.suiteId,
                              statusFilter: filterToHostRow ? "progress" : undefined,
                              query: filterToHostRow ? agentName : undefined,
                              nonce: (current?.nonce ?? 0) + 1,
                            }));
                          }}
                          size="xs"
                          variant="ghost"
                        >
                          <CrosshairIcon aria-hidden="true" data-icon="inline-start" />
                          {copy(language, "定位", "Locate")}
                        </Button>
                      ) : null}
                    </div>
                  ))}
                </div>
              </section>
            )) : <p className="py-6 text-center text-sm text-muted-foreground">{copy(language, "当前没有进行中的任务。", "All active tasks have finished.")}</p>}
          </SheetBody>
        </SheetContent>
      </Sheet>

      <BatchUpgradeSheet
        data={data}
        instancesByID={installedInstancesByID}
        language={language}
        onClose={() => setBatchUpgrade(null)}
        onSubmit={submitBatchUpgrade}
        request={batchUpgrade}
      />

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
      <DeploymentSheet data={data} editor={deploymentEditor} language={language} onClose={() => setDeploymentEditor(null)} onSubmit={async (...args) => {
        await submitDeployment(...args);
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
      <ThreeXUIInboundTrafficSheet controller={trafficController ?? null} language={language} onClose={() => setTrafficService(null)} onNavigate={onNavigate} service={trafficService} siteTimezone={trafficService ? data.sites.find((site) => site.id === trafficService.siteId)?.timezone : undefined} />
      <SubscriptionSheet application={subscriptionApplication} data={data} language={language} mutate={mutate} onClose={() => setSubscriptionApplication(null)} />
	      <ThreeXUIClientsSheet advancedURL={clientsApplication ? data.deployments.find((value) => value.applicationId === clientsApplication.id && value.state === "succeeded" && value.operation !== "uninstall")?.accessUrl : undefined} application={clientsApplication} language={language} onClose={() => setClientsApplication(null)} siteTimezone={clientsApplication ? data.sites.find((site) => site.id === clientsApplication.siteId)?.timezone : undefined} />
	      <ApplicationCredentialsSheet application={credentialApplication} language={language} onClose={() => setCredentialApplication(null)} />
	      <ThreeXUIControllerMigrationSheet application={migrationApplication} data={data} language={language} mutate={mutate} onClose={() => setMigrationApplication(null)} />
      {appManagerApplication && appManagerApplication.appKey !== "vastora-official/meridian" ? <OfficialAppManagerHost application={appManagerApplication} data={data} language={language} mutate={mutate} onClose={() => setAppManagerApplication(null)} version={catalogByKey.get("vastora-official/meridian")?.app.version} /> : null}
      <UninstallSheet application={uninstallApplication} app={uninstallApplication ? catalogByKey.get(uninstallApplication.appKey) : undefined} language={language} onClose={() => setUninstallApplication(null)} onSubmit={async (application, deleteData) => { await mutate(() => api.createDeployment(application.nodeId, application.appKey, {}, "uninstall", deleteData), copy(language, "卸载任务已创建。", "Uninstall task created.")); setUninstallApplication(null); }} />
    </section>
  );
}
