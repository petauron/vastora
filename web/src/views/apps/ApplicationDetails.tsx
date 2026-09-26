import { useEffect, useState, type FormEvent } from "react";
import { ArrowRightLeftIcon, ArrowUpCircleIcon, CheckCircle2Icon, ExternalLinkIcon, EyeIcon, EyeOffIcon, Globe2Icon, KeyRoundIcon, PencilIcon, RadioTowerIcon, RotateCcwIcon, Settings2Icon, ShieldAlertIcon, ShieldCheckIcon, Trash2Icon, UsersIcon } from "lucide-react";
import { api } from "../../api";
import type { AppData, Mutate } from "../../App";
import type { Application, ApplicationCredentialRotation, ApplicationCredentials, Publication, Service } from "../../types";
import type { Language } from "../../translations";
import { administratorPasswordMinLength } from "../../lib/security";
import { clearSecretOperation, secretOperation } from "../../secret-delivery";
import { localized, operationLabel, publicationKindLabel } from "../appAccess";
import { AppHostAccessNote, AppIdentityBadge } from "../AppIdentity";
import { canCreateRealityNode, type InstalledAppInstance } from "../installed-apps-model";
import { PulseSetupNotice } from "../PulseSetupNotice";
import { catalogInstallBlocked, CopyButton, copy, formatDate, StateBadge, TechnicalError, userError } from "../shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

export function InstalledAppDetails({ instance, data, language, onClients, onConfigure, onCredentials, onMigrate, onAppManager, onPublish, onReality, onRenameReality, onRemoveReality, onSubscription, onTraffic, onUninstall, onUpgrade, mutate }: { instance: InstalledAppInstance; data: AppData; language: Language; onClients: () => void; onConfigure: () => void; onCredentials: () => void; onMigrate: () => void; onAppManager: (moduleKey: string) => void; onPublish: (service: Service) => void; onReality: () => void; onRenameReality: (service: Service) => void; onRemoveReality: (service: Service) => void; onSubscription: () => void; onTraffic: (service: Service) => void; onUninstall: () => void; onUpgrade: () => void; mutate: Mutate }) {
  const [syncingNode, setSyncingNode] = useState(false);
  const { application, app, agent, services, deployment, activeChange, locked: serviceAccessLocked } = instance;
  const subscriptionService = services.find((service) => service.name === "subscription");
  const subscriptionPublication = subscriptionService ? data.publications.find((value) => value.serviceId === subscriptionService.id && value.status !== "stopped" && (value.kind === "cloudflare_tunnel" || value.kind === "public_direct")) : undefined;
  const isThreeXUI = application.appKey === "vastora-official/3x-ui";
	const isMeridian = application.appKey === "vastora-official/meridian";
	const isCPA = application.appKey === "vastora-official/cpa";
  const isController = isThreeXUI && application.role === "master" && application.id === application.controllerApplicationId;
  const ownsMeridianCutover = isController && data.meridian.cutover.legacyControllerApplicationId === application.id && data.meridian.cutover.state !== "complete";
  const canStartMeridianCutover = ownsMeridianCutover && data.meridian.cutover.subscriptionAuthority === "legacy";
  const isLegacyController = isThreeXUI && application.role === "master" && Boolean(application.controllerApplicationId) && application.id !== application.controllerApplicationId;
  const isWorker = isThreeXUI && application.role === "worker";
  const nodeSyncing = application.nodeSyncStatus === "pending" || application.nodeSyncStatus === "applying";
  const nodeReady = application.nodeSyncStatus === "ready";
  const visibleServices = isWorker || isLegacyController
    ? services.filter((service) => service.protocol !== "http" && service.protocol !== "https")
    : isMeridian
      ? services.filter((service) => service.name !== "subscription")
      : services;
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
  return <SheetContent className="apps-workspace data-[side=right]:w-full data-[side=right]:sm:max-w-xl">
    <SheetHeader className="pr-12">
      <SheetTitle className="flex flex-wrap items-center gap-2">{app ? localized(app, language, "name") : application.name}{app ? <AppIdentityBadge app={app} language={language} /> : null}{isController ? <Badge>{copy(language, "全局订阅主机", "Global subscription controller")}</Badge> : null}{isLegacyController ? <Badge variant="outline">{copy(language, "待替换为 Xray", "Converting to Xray")}</Badge> : null}{isWorker ? <Badge variant="outline">{copy(language, "Xray 节点", "Xray node")}</Badge> : null}</SheetTitle>
      <SheetDescription>{agent?.name ?? application.nodeId} · {application.runtime}{application.installedVersion ? ` · v${application.installedVersion}` : ""}</SheetDescription>
      <div className="mt-2"><StateBadge language={language} value={application.status} /></div>
    </SheetHeader>
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 pb-4">
      {activeChange ? <Alert>{activeChange.reconciliationRequired ? <ShieldAlertIcon /> : <Spinner />}<AlertTitle>{activeChange.reconciliationRequired ? copy(language, "需要继续恢复", "Recovery required") : copy(language, `正在${operationLabel(language, activeChange.operation)}`, `${operationLabel(language, activeChange.operation)} in progress`)}</AlertTitle><AlertDescription>{activeChange.reconciliationRequired ? copy(language, "应用状态尚未确认，恢复完成前不能修改或发布服务；已有入口仍可停止。", "The app state is not confirmed. Services cannot be changed or published until recovery finishes; existing access points can still be stopped.") : copy(language, "完成前暂时不能发起其他应用变更。", "Other app changes are unavailable until this finishes.")}</AlertDescription></Alert> : null}
      {application.status === "failed" ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "最近一次操作失败，应用仍保留", "The last operation failed; the app is still installed")}</AlertTitle><AlertDescription>{copy(language, "原有安装记录和数据仍保留；请查看最近操作，修正后重试或卸载。", "The installed record and data remain available. Review the recent operation, then retry or uninstall.")}</AlertDescription></Alert> : null}
      {application.appKey === "vastora-official/pulse" ? <PulseSetupNotice data={data} language={language} /> : null}
      {app ? <AppHostAccessNote app={app} language={language} /> : null}
      {isController ? <p aria-atomic="true" className="text-sm text-muted-foreground" role="status">{managedVLESSNodeCount > 0 ? copy(language, `当前订阅包含 ${managedVLESSNodeCount} 个节点，由此订阅主机统一管理。`, `The current subscription includes ${managedVLESSNodeCount} node(s), managed by this subscription controller.`) : copy(language, "尚未创建 VLESS 节点；创建后会自动加入统一订阅。", "No VLESS nodes yet. New nodes are added to the shared subscription automatically.")}</p> : null}
      {isController || isLegacyController ? <p className="text-xs text-muted-foreground">{application.restorePointState === "ready" && application.restorePointAt ? copy(language, `恢复点已保存 · ${new Date(application.restorePointAt).toLocaleString()}`, `Restore point saved · ${new Date(application.restorePointAt).toLocaleString()}`) : application.restorePointState === "pending" ? copy(language, "正在保存恢复点…", "Saving a restore point…") : copy(language, "恢复点将在节点在线后自动保存。", "A restore point will be saved automatically when the node is online.")}</p> : null}
      {isWorker && nodeSyncing ? <Alert aria-live="polite"><Spinner /><AlertTitle>{copy(language, "正在接入订阅主机", "Connecting to the subscription controller")}</AlertTitle><AlertDescription>{copy(language, "连接完成后即可在此节点一键创建 VLESS。", "Once connected, you can create VLESS on this node.")}</AlertDescription></Alert> : null}
      {isWorker && (application.nodeSyncStatus === "failed" || !application.nodeSyncStatus) ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "尚未接入订阅主机", "Not connected to the subscription controller")}</AlertTitle><AlertDescription><p>{application.nodeSyncError || copy(language, "升级来的旧节点需要重新连接；新节点请确认两台主机能通过 Headscale 或私网互相访问。", "An upgraded existing node must reconnect. For a new node, confirm both hosts can reach each other over Headscale or the private network.")}</p><Button className="mt-3" disabled={syncingNode} onClick={() => void retryNodeSync()} size="sm" variant="outline">{syncingNode ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{copy(language, "重新连接", "Reconnect")}</Button></AlertDescription></Alert> : null}
      {isWorker && nodeReady ? <p className="text-sm text-muted-foreground">{copy(language, "此节点提供代理服务；客户端和订阅由全局订阅主机统一管理。", "This node provides proxy services. Clients and subscriptions are managed by the global controller.")}</p> : null}
      {isLegacyController ? <Alert><Spinner /><AlertTitle>{copy(language, "正在并入全局订阅", "Joining the global subscription")}</AlertTitle><AlertDescription>{copy(language, "系统会先保存恢复点，再把这台旧订阅主机替换为 Xray-only 节点。", "The system saves a restore point, then replaces this legacy controller with an Xray-only node.")}</AlertDescription></Alert> : null}
	      {isThreeXUI ? <div className={`grid gap-2 ${isController ? "sm:grid-cols-2" : "sm:grid-cols-1"}`}>
	        {isController ? <Button disabled={serviceAccessLocked} onClick={onClients} size="sm"><UsersIcon data-icon="inline-start" />{copy(language, "管理客户端", "Manage clients")}</Button> : null}
	        {isController ? <Button onClick={onCredentials} size="sm" variant="outline"><KeyRoundIcon data-icon="inline-start" />{copy(language, "管理账号", "Admin credentials")}</Button> : null}
	        {!hasVLESSNode ? <Button disabled={!canCreateRealityNode(instance) || syncingNode} onClick={onReality} size="sm" variant={isController ? "outline" : "default"}><RadioTowerIcon data-icon="inline-start" />{copy(language, "创建 VLESS", "Create VLESS")}</Button> : null}
        {isController && subscriptionService ? <Button disabled={serviceAccessLocked} onClick={onSubscription} size="sm" variant="outline"><Globe2Icon data-icon="inline-start" />{subscriptionPublication ? copy(language, "公网订阅", "Public subscription") : copy(language, "开启订阅", "Enable subscription")}</Button> : null}
      </div> : null}
			{isMeridian || ownsMeridianCutover ? <div className="grid gap-2 sm:grid-cols-2"><Button disabled={Boolean(activeChange)} onClick={() => onAppManager("vastora-official/meridian")} size="sm"><UsersIcon data-icon="inline-start" />{canStartMeridianCutover ? copy(language, "迁移到 Meridian", "Migrate to Meridian") : ownsMeridianCutover ? copy(language, "查看 Meridian 迁移", "View Meridian migration") : copy(language, "管理账号与节点", "Manage accounts & nodes")}</Button>{isMeridian && subscriptionService ? <Button disabled={serviceAccessLocked} onClick={onSubscription} size="sm" variant="outline"><Globe2Icon data-icon="inline-start" />{subscriptionPublication ? copy(language, "公网订阅", "Public subscription") : copy(language, "开启订阅", "Enable subscription")}</Button> : null}</div> : null}
			{isCPA ? <Button disabled={Boolean(activeChange)} onClick={onCredentials} size="sm" variant="outline"><KeyRoundIcon data-icon="inline-start" />{copy(language, "凭据", "Credentials")}</Button> : null}
      {!isWorker && !isLegacyController && deployment?.accessUrl ? <Button nativeButton={false} render={<a href={deployment.accessUrl} rel="noreferrer" target="_blank" />} size="sm" variant="outline"><ExternalLinkIcon data-icon="inline-start" />{application.appKey === "vastora-official/pulse" ? copy(language, "打开监控", "Open monitoring") : copy(language, "打开主页", "Open homepage")}</Button> : !isWorker && !isLegacyController && app?.app.homepage ? <p className="text-xs text-muted-foreground">{copy(language, "添加并完成一个访问入口后，这里会出现“打开主页”。", "After an access point is ready, an Open homepage button appears here.")}</p> : null}
      {!isWorker && !isLegacyController && visibleServices.length === 0 ? <p className="text-sm text-muted-foreground">{copy(language, "此应用没有可发布的 Web 服务。", "This app has no publishable Web service.")}</p> : null}
			{visibleServices.map((service) => <ServiceRow data={data} key={service.id} language={language} locked={serviceAccessLocked} onPublish={() => onPublish(service)} onRename={() => onRenameReality(service)} onRemove={isController && service.appProtocol === "vless/tcp/reality" ? () => onRemoveReality(service) : undefined} onTraffic={() => onTraffic(service)} service={service} mutate={mutate} />)}
      {isController && activeWorkers.length > 0 ? <p className="text-xs text-muted-foreground">{copy(language, "移除所有 Xray 节点后才能卸载订阅主机。", "Remove all Xray nodes before uninstalling the subscription controller.")}</p> : null}
    </div>
    <SheetFooter className="border-t">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        {app && !application.updateAvailable ? <Badge variant="secondary">{copy(language, "版本已是最新", "Version up to date")}</Badge> : null}
        <div aria-label={copy(language, "应用操作", "Application actions")} className="ml-auto flex min-w-0 flex-wrap items-center justify-end gap-2" role="group">
          {isLegacyController ? <Button disabled={Boolean(activeChange) || application.restorePointState === "pending"} onClick={onMigrate} size="sm" variant="outline"><ArrowRightLeftIcon data-icon="inline-start" />{copy(language, "查看转换进度", "View conversion")}</Button> : isController && activeWorkers.some((worker) => worker.nodeSyncStatus === "ready") ? <Button disabled={Boolean(activeChange) || application.restorePointState === "pending"} onClick={onMigrate} size="sm" variant="outline"><ArrowRightLeftIcon data-icon="inline-start" />{copy(language, "迁移订阅主机", "Move subscription host")}</Button> : null}
          {application.updateAvailable ? <Button disabled={Boolean(activeChange) || isLegacyController || catalogInstallBlocked(app)} onClick={onUpgrade} size="sm"><ArrowUpCircleIcon data-icon="inline-start" />{copy(language, `升级到 v${application.availableVersion}`, `Upgrade to v${application.availableVersion}`)}</Button> : null}
          {application.updateAvailable && catalogInstallBlocked(app) ? <p className="text-sm text-muted-foreground">{copy(language, "请先在设置中刷新应用目录，再升级。", "Refresh the app catalog in Settings before upgrading.")}</p> : null}
          {app && app.app.config.length > 0 ? <Button disabled={Boolean(activeChange) || isLegacyController} onClick={onConfigure} size="sm" variant="outline"><Settings2Icon data-icon="inline-start" />{copy(language, "修改配置", "Change settings")}</Button> : null}
          <Button disabled={Boolean(activeChange) || isLegacyController || activeWorkers.length > 0} onClick={onUninstall} size="sm" variant="ghost"><Trash2Icon data-icon="inline-start" />{copy(language, "卸载", "Uninstall")}</Button>
        </div>
      </div>
    </SheetFooter>
	  </SheetContent>;
}

export function ApplicationCredentialsSheet({ application, language, onClose }: { application: Application | null; language: Language; onClose: () => void }) {
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
				<SheetTitle>{isCPA ? copy(language, "CPA 凭据", "CPA credentials") : copy(language, "Vastora Proxy 管理账号", "Vastora Proxy administrator account")}</SheetTitle>
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

export function ServiceRow({ data, language, service, locked, onPublish, onRename, onRemove, onTraffic, mutate }: { data: AppData; language: Language; service: Service; locked: boolean; onPublish: () => void; onRename: () => void; onRemove?: () => void; onTraffic: () => void; mutate: Mutate }) {
	const publications = data.publications.filter((value) => value.serviceId === service.id && (value.status !== "stopped" || value.actionRequired));
	const activePublication = publications.find((value) => value.status !== "stopped");
	const cloudflareReady = data.integrations.some((value) => value.kind === "cloudflare" && value.status === "configured");
	const isLegacyReality = service.appProtocol === "vless/tcp/reality";
	const isMeridianEntry = service.appProtocol === "meridian/entry";
	const isManagedEntry = isLegacyReality || isMeridianEntry;
	const application = data.applications.find((value) => value.id === service.applicationId);
	const managedSubscription = application?.appKey === "vastora-official/3x-ui" && application.role === "master" && service.name === "subscription";
	const cpaClientAPI = application?.appKey === "vastora-official/cpa" && service.name === "client-api";
	const trafficControllerAvailable = Boolean(application?.controllerApplicationId && data.applications.some((value) => value.id === application.controllerApplicationId && value.role === "master" && value.status === "running"));
	const trafficHintID = `traffic-controller-${service.id}`;
	const summary = isManagedEntry
		? (service.protocols ?? ["vless"]).map((protocol) => protocol.toUpperCase()).join(" / ")
		: cpaClientAPI
			? copy(language, "对外 API · 仅开放 /v1 · 客户端密钥鉴权", "Public API · /v1 only · client-key authentication")
		: service.management
				? copy(language, "管理页面", "Admin page")
				: copy(language, "应用服务", "App service");
	return <div className="rounded-xl border p-3">
		<div className="flex flex-wrap items-center gap-2">
			<div className="min-w-0 flex-1">
				<div className="flex flex-wrap items-center gap-2"><p className="truncate text-sm font-medium">{cpaClientAPI ? copy(language, "公网 API", "Public API") : service.displayName || service.name}</p>{service.management ? <Badge variant="destructive">{copy(language, "管理页", "Admin")}</Badge> : null}</div>
				<p className="mt-1 text-xs text-muted-foreground">{summary}</p>
			</div>
			{isLegacyReality ? <Button aria-describedby={!trafficControllerAvailable ? trafficHintID : undefined} disabled={locked || !trafficControllerAvailable} onClick={onTraffic} size="sm" variant="outline">{service.protocols?.includes("hy2") ? copy(language, "VLESS 套餐", "VLESS plan") : copy(language, "节点套餐", "Node plan")}</Button> : null}{isManagedEntry ? <Button disabled={locked} onClick={onRename} size="icon-sm" title={copy(language, "编辑节点", "Edit node")} variant="ghost"><PencilIcon /><span className="sr-only">{copy(language, "编辑节点", "Edit node")}</span></Button> : null}
			{onRemove ? <Button disabled={locked} onClick={onRemove} size="sm" variant="outline"><Trash2Icon data-icon="inline-start" />{copy(language, "移除本机节点", "Remove local node")}</Button> : null}
			{!managedSubscription ? <Button disabled={locked || Boolean(cpaClientAPI && activePublication)} onClick={onPublish} size="sm" variant="outline"><Globe2Icon data-icon="inline-start" />{cpaClientAPI ? activePublication ? copy(language, "API 已开启", "API enabled") : copy(language, "开启公网 API", "Enable public API") : copy(language, "添加入口", "Add access")}</Button> : null}
		</div>
		{isLegacyReality && !trafficControllerAvailable ? <p className="mt-2 text-xs text-destructive" id={trafficHintID}>{copy(language, "订阅主机当前不可用，暂时不能修改节点套餐。", "The subscription controller is unavailable, so this node plan cannot be changed yet.")}</p> : null}
		{service.lastError || service.actionRequiredReason ? <div className="mt-2"><TechnicalError error={service.lastError || service.actionRequiredReason} language={language} /></div> : null}
		{publications.length ? <div className="mt-3 flex flex-col gap-2">{publications.map((publication) => <PublicationRow hy2Only={Boolean(isManagedEntry && service.protocols?.includes("hy2") && !service.protocols?.includes("vless"))} cloudflareReady={cloudflareReady} copyAccessURL={cpaClientAPI} key={publication.id} language={language} locked={locked} mutate={mutate} publication={publication} />)}</div> : <p className="mt-3 text-xs text-muted-foreground">{managedSubscription ? copy(language, "请使用上方“开启订阅”配置唯一的公网订阅地址。", "Use Enable subscription above to configure the single public subscription URL.") : cpaClientAPI ? copy(language, "开启后会生成 HTTPS API 地址；管理页面不会通过这个域名暴露。", "Enabling this creates an HTTPS API URL; the management page is not exposed on that hostname.") : copy(language, "仅在节点内部运行，添加入口后才能访问。", "Runs privately on the node until you add an access point.")}</p>}
		<details className="mt-3 rounded-lg bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
			<summary className="cursor-pointer font-medium text-foreground">{copy(language, "技术信息", "Technical details")}</summary>
			<dl className="mt-2 grid gap-1.5"><div className="flex gap-2"><dt>{copy(language, "源站", "Origin")}</dt><dd className="min-w-0 break-all font-mono">{service.protocol} · {service.endpoint}</dd></div>{isManagedEntry && service.displayName ? <div className="flex gap-2"><dt>{copy(language, "内部名称", "Internal name")}</dt><dd className="min-w-0 break-all font-mono">{service.name}</dd></div> : null}</dl>
		</details>
	</div>;
}

export function PublicationRow({ publication, language, locked, mutate, cloudflareReady, copyAccessURL = false, hy2Only = false }: { publication: Publication; language: Language; locked: boolean; mutate: Mutate; cloudflareReady: boolean; copyAccessURL?: boolean; hy2Only?: boolean }) {
	const [busyAction, setBusyAction] = useState<"tls" | "verify" | "security" | "stop" | null>(null);
	const run = async (action: "tls" | "verify" | "security" | "stop", operation: () => Promise<unknown>, success: string) => { setBusyAction(action); try { await mutate(operation, success); } catch { /* The shared notice already explains the failure. */ } finally { setBusyAction(null); } };
	const privateWeb = publication.kind === "lan_gateway" || publication.kind === "headscale_gateway";
	const managedReality = !hy2Only && publication.kind === "public_shared_443" && publication.ingress.owner === "application_node";
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
	return <div className="flex flex-col gap-2 rounded-lg bg-muted/60 p-3 text-xs sm:flex-row sm:items-center">{hy2Only ? <Badge variant="outline">HY2 · UDP 443</Badge> : <StateBadge value={publication.actionRequired ? "action_required" : publication.status} />}<div className="min-w-0 flex-1"><p className="break-all font-medium" title={publication.accessUrl ?? publication.hostname}>{publication.accessUrl ?? publication.hostname}</p><p className="mt-0.5 text-muted-foreground">{publicationKindLabel(language, publication.kind)} · {ingressLabel}{privateWeb ? ` · ${publication.tlsEnabled ? "HTTPS" : "HTTP"}` : ""}</p>{!hy2Only && publication.sniHostname ? <p className="mt-0.5 truncate font-mono text-muted-foreground">SNI → {publication.sniHostname}</p> : null}{managedReality ? <div className="mt-1.5 flex flex-wrap items-center gap-1.5"><Badge variant={security?.status === "affected" ? "destructive" : security?.status === "safe" ? "secondary" : "outline"}><SecurityIcon data-icon="inline-start" />{securityLabel}</Badge>{security ? <span className="text-muted-foreground">{formatDate(language, security.checkedAt)}</span> : null}</div> : null}{securityHint ? <p className={security?.status === "affected" ? "mt-1 text-destructive" : "mt-1 text-muted-foreground"}>{securityHint}</p> : null}{publication.lastError ? <div className="mt-1"><TechnicalError error={publication.lastError} language={language} /></div> : null}{publication.dnsRecord && publication.dnsProvider !== "cloudflare" ? <code className="mt-1 block break-all text-muted-foreground">{publication.dnsRecord.type} {publication.dnsRecord.name} → {publication.dnsRecord.value}</code> : null}</div><div className="flex flex-wrap items-center gap-2">{privateWeb && publication.status !== "stopped" ? <div aria-busy={busyAction === "tls"} className="flex min-h-9 items-center gap-2 rounded-lg border bg-background px-2.5" title={tlsUnavailable ? copy(language, "连接 Cloudflare 后才能申请可信证书", "Connect Cloudflare to issue a trusted certificate") : undefined}><label className="font-medium" htmlFor={`publication-tls-${publication.id}`} role={busyAction === "tls" ? "status" : undefined}>{tlsStatus}</label>{busyAction === "tls" ? <Spinner aria-hidden="true" /> : null}<Switch aria-label={tlsLabel} checked={publication.tlsEnabled} disabled={locked || busyAction !== null || tlsUnavailable} id={`publication-tls-${publication.id}`} onCheckedChange={(enabled) => void run("tls", () => api.updatePublicationTLS(publication.id, enabled), enabled ? copy(language, "正在启用 HTTPS，入口配置已提交。", "HTTPS is being enabled and the access configuration was submitted.") : copy(language, "HTTPS 已关闭，入口将使用 HTTP。", "HTTPS was turned off; the access point will use HTTP."))} /></div> : null}{copyAccessURL && publication.accessUrl ? <CopyButton language={language} size="icon" value={publication.accessUrl} /> : null}{publication.accessUrl ? <Button aria-label={copy(language, "打开服务", "Open service")} nativeButton={false} render={<a href={publication.accessUrl} rel="noreferrer" target="_blank" />} size="icon-sm" variant="ghost"><ExternalLinkIcon /></Button> : null}{managedReality && publication.status !== "stopped" ? <Button disabled={locked || busyAction !== null || publication.status !== "ready"} onClick={() => void run("security", () => api.checkRealitySecurity(publication.id), copy(language, "安全检查已完成。", "Security check completed."))} size="sm" variant="outline">{busyAction === "security" ? <Spinner data-icon="inline-start" /> : <ShieldCheckIcon data-icon="inline-start" />}{copy(language, "安全检查", "Security check")}</Button> : null}{!hy2Only && publication.status !== "ready" && publication.status !== "stopped" ? <Button disabled={locked || busyAction !== null} onClick={() => void run("verify", () => api.verifyPublication(publication.id), copy(language, "入口检查已完成。", "Access point checked."))} size="sm" variant="outline">{busyAction === "verify" ? <Spinner data-icon="inline-start" /> : null}{copy(language, "检查", "Check")}</Button> : null}{publication.status !== "stopped" ? <Button disabled={busyAction !== null || hy2Only} onClick={() => void run("stop", () => api.stopPublication(publication.id), copy(language, "入口已停止。", "Access point stopped."))} size="sm" variant="ghost">{busyAction === "stop" ? <Spinner data-icon="inline-start" /> : null}{copy(language, "停止", "Stop")}</Button> : null}</div></div>;
}
