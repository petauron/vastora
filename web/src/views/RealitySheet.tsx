import { useEffect, useRef, useState, type FormEvent } from "react";
import { Globe2Icon, KeyRoundIcon, RadioTowerIcon, RotateCcwIcon, ShieldCheckIcon, UsersIcon } from "lucide-react";
import { api } from "../api";
import type { AppData, Application, ApplicationCommand, AgentView } from "../types";
import type { Language } from "../translations";
import { useApplicationCommandExecutor } from "../hooks/use-application-command-executor";
import { clearSecretOperation, commandSecretScope, readSecretOperation, secretOperation } from "../secret-delivery";
import { defaultRealityHostname } from "./appAccess";
import { RegionCombobox, regionDisplayName } from "./RegionCombobox";
import { bytesFromGB, dateInputValueInTimeZone, endOfDayEpochInTimeZone, InboundTrafficPlanFields, nextRenewalDateInTimeZone, SubscriptionTrafficPlanFields } from "./TrafficPlanFields";
import { CopyButton, copy, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SelectControl } from "@/components/SelectControl";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";

type RegionMatch = "idle" | "matching" | "matched" | "manual" | "unavailable";
type RealityDraft = {
  name: string;
  regionCode: string;
  clientName: string;
  inboundQuota: string;
  inboundResetDay: string;
  clientQuota: string;
  clientResetDays: string;
  clientExpiry: string;
  gatewayID: string;
  hostname: string;
  dnsProvider: "manual" | "cloudflare";
  targetHost: string;
  serverName: string;
};

export function RealitySheet({ application, data, language, onClose, siteTimezone }: { application: Application | null; data: AppData; language: Language; onClose: () => void; siteTimezone?: string }) {
  const [draft, setDraft] = useState<RealityDraft>(emptyDraft());
  const [collectInitialClient, setCollectInitialClient] = useState(false);
  const [regionMatch, setRegionMatch] = useState<RegionMatch>("idle");
  const [command, setCommand] = useState<ApplicationCommand | null>(null);
  const [verification, setVerification] = useState<ApplicationCommand | null>(null);
  const [shareURI, setShareURI] = useState("");
	const [shareOperationKey, setShareOperationKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const baseline = useRef<RealityDraft>(emptyDraft());
  const regionRequest = useRef(0);
  const { execute } = useApplicationCommandExecutor(application?.id);
  const cloudflareReady = data.integrations.some((integration) => integration.kind === "cloudflare" && integration.status === "configured");
  const targetAgent = application ? data.agents.find((agent) => agent.id === application.nodeId) : undefined;
  const gateways = application ? realityGateways(data, application) : [];
  const dirty = Boolean(application && !command && !sameDraft(draft, baseline.current));

  useEffect(() => {
    if (!application) return;
    const firstControllerReality = application.role === "master" && !data.services.some((service) => service.applicationId === application.id && service.appProtocol === "vless/tcp/reality" && service.status !== "stopped");
    const initial: RealityDraft = {
      name: targetAgent?.name ?? copy(language, "VLESS 节点", "VLESS node"),
      regionCode: "",
      clientName: copy(language, "我的设备", "My device"),
      inboundQuota: "",
      inboundResetDay: "1",
      clientQuota: "",
      clientResetDays: "0",
      clientExpiry: "",
      gatewayID: gateways[0]?.id ?? "",
      hostname: defaultRealityHostname(data, application),
      dnsProvider: cloudflareReady ? "cloudflare" : "manual",
      targetHost: "",
      serverName: ""
    };
    regionRequest.current += 1;
    baseline.current = initial;
    setDraft(initial);
    setCollectInitialClient(firstControllerReality);
    setRegionMatch("idle");
    setCommand(null);
    setVerification(null);
    setShareURI("");
		setShareOperationKey("");
    setBusy(false);
    setError("");
    let cancelled = false;
		void api.latestApplicationCommand(application.id, "3xui.reality.create").then((latest) => {
			if (cancelled || latest.state === "failed" && !latest.reconciliationRequired) return;
      setCommand(latest);
      setDraft((current) => ({ ...current, gatewayID: latest.gatewayNodeId, hostname: latest.hostname, dnsProvider: latest.dnsProvider }));
			if (latest.resultAvailable) {
				const operationKey = readSecretOperation(commandSecretScope(application.id, latest.id));
				if (operationKey) void api.revealApplicationCommand(latest.id, operationKey).then((result) => {
					if (cancelled) return;
					setShareOperationKey(operationKey);
					setShareURI(result.shareUri);
				}).catch(() => { /* The durable result remains available for a later retry. */ });
			}
      if (latest.state === "pending" || latest.state === "running") {
        void execute(() => Promise.resolve(latest), setCommand).catch((pollError) => setError(userError(language, pollError)));
      }
    }).catch(() => { /* No resumable operation is the normal first-use state. */ });
    return () => { cancelled = true; };
  }, [application?.id, execute, language]);

  useEffect(() => {
    const request = ++regionRequest.current;
    if (!application || !draft.gatewayID || command) {
      if (!draft.gatewayID) setRegionMatch("idle");
      return;
    }
    const updateBaseline = baseline.current.gatewayID === draft.gatewayID && baseline.current.regionCode === "";
    setRegionMatch("matching");
    void api.agentRegionSuggestion(draft.gatewayID).then((suggestion) => {
      if (request !== regionRequest.current) return;
      setDraft((current) => ({ ...current, regionCode: suggestion.regionCode }));
      if (updateBaseline) baseline.current = { ...baseline.current, regionCode: suggestion.regionCode };
      setRegionMatch("matched");
    }).catch(() => {
      if (request !== regionRequest.current) return;
      setDraft((current) => ({ ...current, regionCode: "" }));
      setRegionMatch("unavailable");
    });
  }, [application?.id, command?.id, draft.gatewayID]);

  const setField = <K extends keyof RealityDraft>(field: K, value: RealityDraft[K]) => {
    if (field === "targetHost" || field === "serverName") setVerification(null);
    setDraft((current) => ({ ...current, [field]: value }));
  };
  const gateway = data.agents.find((agent) => agent.id === draft.gatewayID);
  const displayName = regionDisplayName(draft.regionCode, draft.name);

  const requestClose = () => {
    if (dirty && !window.confirm(copy(language, "放弃尚未保存的修改？", "Discard unsaved changes?"))) return;
    onClose();
  };

	const reveal = async () => {
    if (!command || command.state !== "succeeded" || !command.clientCreated || !command.resultAvailable) return;
    setBusy(true);
    setError("");
    try {
			const operationKey = secretOperation(commandSecretScope(command.applicationId, command.id));
			setShareOperationKey(operationKey);
			setShareURI((await api.revealApplicationCommand(command.id, operationKey)).shareUri);
    } catch (revealError) {
      setError(userError(language, revealError));
    } finally {
      setBusy(false);
    }
	};

	const acknowledgeShareURI = async () => {
		if (!command || !shareOperationKey) return;
		setBusy(true);
		setError("");
		try {
			await api.acknowledgeApplicationCommand(command.id, shareOperationKey);
			clearSecretOperation(commandSecretScope(command.applicationId, command.id));
			setCommand({ ...command, resultAvailable: false });
			setShareURI("");
			setShareOperationKey("");
		} catch (ackError) {
			setError(userError(language, ackError));
		} finally {
			setBusy(false);
		}
	};

	const resumeReconciliation = async () => {
		if (!command?.reconciliationRequired) return;
		setBusy(true);
		setError("");
		let queued = false;
		try {
			await api.retryTaskReconciliation(command.id);
			queued = true;
			setCommand((current) => current?.id === command.id ? { ...current, state: "pending", reconciliationRequired: false, error: "" } : current);
			await execute(() => api.applicationCommand(command.id), setCommand);
		} catch (resumeError) {
			setError(userError(language, resumeError));
			if (!queued) setCommand(command);
		} finally {
			setBusy(false);
		}
	};

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!application) return;
    const inboundTotalBytes = bytesFromGB(draft.inboundQuota);
    const inboundResetDay = Number(draft.inboundResetDay || 0);
    const clientTotalBytes = bytesFromGB(draft.clientQuota);
    const clientRenewalDays = Number(draft.clientResetDays || 0);
    const clientExpiryTime = draft.clientExpiry ? endOfDayEpochInTimeZone(draft.clientExpiry, siteTimezone) : 0;
    const manualTarget = Boolean(draft.targetHost.trim() || draft.serverName.trim());
    if (!Number.isFinite(inboundTotalBytes) || inboundTotalBytes < 0 || !Number.isInteger(inboundResetDay) || inboundResetDay < 0 || inboundResetDay > 31 || collectInitialClient && (!Number.isFinite(clientTotalBytes) || clientTotalBytes < 0 || !Number.isInteger(clientRenewalDays) || clientRenewalDays < 0 || clientRenewalDays > 0 && !clientExpiryTime)) {
      setError(copy(language, "请检查订阅额度和节点套餐。", "Check the subscription allowance and node plan."));
      return;
    }
    if (manualTarget && (!draft.targetHost.trim() || !draft.serverName.trim())) {
      setError(copy(language, "手动指定 REALITY 目标时，需要同时填写目标域名和 SNI；也可以全部留空让 Agent 自动选择。", "When setting a REALITY target manually, enter both the target hostname and SNI, or leave both empty for automatic selection."));
      return;
    }
    setBusy(true);
    setError("");
    try {
      if (manualTarget) {
        const checked = await execute(() => api.verifyRealityTarget(application.id, draft.targetHost, draft.serverName), setVerification);
        if (!checked || checked.state !== "succeeded" || !checked.targetIp || checked.nodeAsn !== checked.targetAsn) {
          throw new Error(checked?.error || copy(language, "伪装目标未通过节点侧安全校验。", "The camouflage target did not pass node-side security validation."));
        }
      }
      await execute(() => api.createRealityCommand({
        applicationId: application.id,
        regionCode: draft.regionCode,
        name: draft.name,
        gatewayNodeId: draft.gatewayID,
        hostname: draft.hostname,
        dnsProvider: draft.dnsProvider,
        targetHost: draft.targetHost,
        serverName: draft.serverName,
        inboundTotalBytes,
        inboundResetDay,
        ...(collectInitialClient ? { clientName: draft.clientName, clientTotalBytes, clientResetDays: clientRenewalDays, clientExpiryTime } : {})
      }), setCommand);
    } catch (submitError) {
      setError(userError(language, submitError));
    } finally {
      setBusy(false);
    }
  };

  return <Sheet onOpenChange={(next) => { if (!next) requestClose(); }} open={Boolean(application)}>
    <SheetContent className="sm:max-w-xl">
      <SheetHeader>
        <SheetTitle>{copy(language, "创建 VLESS REALITY", "Create VLESS REALITY")}</SheetTitle>
        <SheetDescription>{command ? copy(language, "Vastora 正在节点内配置 3x-ui、共享 443 网关和 DNS。", "Vastora is configuring 3x-ui, the shared 443 gateway, and DNS on the node.") : copy(language, "填写名称即可。入口、地区、域名、DNS 和安全目标会自动处理。", "Enter a name. The entry, region, hostname, DNS, and secure target are handled automatically.")}</SheetDescription>
      </SheetHeader>
		{command ? <RealityResult busy={busy} command={command} displayName={displayName} dnsProvider={draft.dnsProvider} error={error} gateway={gateway} language={language} onAcknowledge={() => void acknowledgeShareURI()} onReveal={() => void reveal()} onRetry={() => { if (command.reconciliationRequired) { void resumeReconciliation(); return; } baseline.current = draft; setCommand(null); setVerification(null); setError(""); }} shareURI={shareURI} /> : <RealityForm busy={busy} cloudflareReady={cloudflareReady} collectInitialClient={collectInitialClient} displayName={displayName} draft={draft} error={error} gateway={gateway} gateways={gateways} language={language} onCancel={requestClose} onField={setField} onRegion={(code) => { regionRequest.current += 1; setField("regionCode", code); setRegionMatch("manual"); }} onSubmit={submit} regionMatch={regionMatch} siteTimezone={siteTimezone} targetPublicAddress={targetAgent?.networkProfile?.publicAddress} verification={verification} />}
      {command ? <SheetFooter><Button onClick={requestClose}>{copy(language, shareURI ? "完成" : "关闭", shareURI ? "Done" : "Close")}</Button></SheetFooter> : null}
    </SheetContent>
  </Sheet>;
}

function RealityForm({ busy, cloudflareReady, collectInitialClient, displayName, draft, error, gateway, gateways, language, onCancel, onField, onRegion, onSubmit, regionMatch, siteTimezone, targetPublicAddress, verification }: {
  busy: boolean;
  cloudflareReady: boolean;
  collectInitialClient: boolean;
  displayName: string;
  draft: RealityDraft;
  error: string;
  gateway?: AgentView;
  gateways: AgentView[];
  language: Language;
  onCancel: () => void;
  onField: <K extends keyof RealityDraft>(field: K, value: RealityDraft[K]) => void;
  onRegion: (code: string) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  regionMatch: RegionMatch;
  siteTimezone?: string;
  targetPublicAddress?: string;
  verification: ApplicationCommand | null;
}) {
  const manualTarget = Boolean(draft.targetHost.trim() || draft.serverName.trim());
  return <form className="flex min-h-0 flex-1 flex-col" onSubmit={onSubmit}>
    <div className="flex-1 overflow-y-auto px-4">
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="reality-name">{copy(language, "节点名称", "Node name")}</FieldLabel>
          <Input autoFocus id="reality-name" maxLength={48} onChange={(event) => onField("name", event.target.value)} required value={draft.name} />
          <FieldDescription>{copy(language, "默认使用当前主机名，也可以改成容易识别的名称。", "The current host name is used by default; change it only if you want a friendlier label.")}</FieldDescription>
        </Field>
        {collectInitialClient ? <>
          <Field>
            <FieldLabel htmlFor="reality-client-name">{copy(language, "这台设备", "This device")}</FieldLabel>
            <Input id="reality-client-name" maxLength={64} onChange={(event) => onField("clientName", event.target.value)} required value={draft.clientName} />
            <FieldDescription>{copy(language, "创建完成后会直接给出可复制到 Mac 客户端的链接。", "After creation, Vastora shows a link ready to copy into your Mac client.")}</FieldDescription>
          </Field>
        </> : null}
        <InboundTrafficPlanFields idPrefix="reality-inbound" language={language} nextResetAt="" onQuotaChange={(value) => onField("inboundQuota", value)} onResetDayChange={(value) => onField("inboundResetDay", value)} quota={draft.inboundQuota} resetDay={draft.inboundResetDay} />
        <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "其余配置自动完成", "Everything else is automatic")}</AlertTitle><AlertDescription><dl className="mt-1 grid gap-1 text-xs"><div><dt className="inline text-muted-foreground">{copy(language, "公网入口：", "Public entry: ")}</dt><dd className="inline">{gateway?.name ?? copy(language, "等待可用入口", "Waiting for an available entry")}</dd></div><div><dt className="inline text-muted-foreground">{copy(language, "订阅名称：", "Subscription name: ")}</dt><dd className="inline">{displayName || copy(language, "正在识别地区", "Detecting region")}</dd></div><div><dt className="inline text-muted-foreground">{copy(language, "安全目标：", "Secure target: ")}</dt><dd className="inline">{manualTarget ? copy(language, "使用手动设置", "Use manual settings") : copy(language, "Agent 自动发现并校验", "Agent discovers and verifies it")}</dd></div></dl></AlertDescription></Alert>
        <details className="group rounded-xl border p-3">
          <summary className="flex min-h-11 cursor-pointer list-none items-center text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}<span className="ml-auto text-xs font-normal text-muted-foreground">{copy(language, "通常无需修改", "Usually leave unchanged")}</span></summary>
          <FieldGroup className="border-t pt-4">
            <Field><FieldLabel htmlFor="reality-gateway">{copy(language, "节点公网入口", "Node public entry")}</FieldLabel><SelectControl id="reality-gateway" onValueChange={(value) => onField("gatewayID", value)} options={[{ value: "", label: copy(language, "当前节点没有可用的公网入口", "This node has no public entry"), disabled: true }, ...gateways.map((agent) => ({ value: agent.id, label: agent.name }))]} required value={draft.gatewayID} /><FieldDescription>{copy(language, "VLESS 始终通过当前节点自己的公网 443 提供服务，不会借用其他节点中继。", "VLESS always uses this node's own public port 443 and never relays through another node.")}</FieldDescription></Field>
            <Field><FieldLabel htmlFor="reality-region">{copy(language, "地区", "Region")}</FieldLabel><RegionCombobox id="reality-region" language={language} onValueChange={onRegion} value={draft.regionCode} /><FieldDescription aria-live="polite">{regionMatch === "matching" ? copy(language, "正在根据公网 IP 识别…", "Detecting from the public IP…") : regionMatch === "matched" ? copy(language, `已根据 ${gateway?.networkProfile?.publicAddress ?? "公网 IP"} 自动匹配。`, `Matched from ${gateway?.networkProfile?.publicAddress ?? "the public IP"}.`) : regionMatch === "unavailable" ? copy(language, "未能自动识别，请手动选择。", "Automatic detection was unavailable. Select it manually.") : copy(language, "只在自动识别不正确时修改。", "Change only when automatic detection is incorrect.")}</FieldDescription></Field>
            <Field><FieldLabel htmlFor="reality-hostname">{copy(language, "连接域名", "Connection hostname")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="reality-hostname" onChange={(event) => onField("hostname", event.target.value.toLowerCase())} required spellCheck={false} value={draft.hostname} /><FieldDescription>{copy(language, "已按节点与位置自动生成。", "Generated automatically from the node and location.")}</FieldDescription></Field>
            <Field><FieldLabel htmlFor="reality-dns">DNS</FieldLabel><SelectControl id="reality-dns" onValueChange={(value) => onField("dnsProvider", value as RealityDraft["dnsProvider"])} options={[{ value: "manual", label: copy(language, "手动添加 A 记录", "Add an A record manually") }, ...(cloudflareReady ? [{ value: "cloudflare", label: copy(language, "Cloudflare 自动管理", "Manage with Cloudflare") }] : [])]} value={draft.dnsProvider} /></Field>
            {collectInitialClient ? <SubscriptionTrafficPlanFields expiry={draft.clientExpiry} idPrefix="reality-subscription" language={language} minimumDate={dateInputValueInTimeZone(new Date(), siteTimezone)} onExpiryChange={(value) => onField("clientExpiry", value)} onQuotaChange={(value) => onField("clientQuota", value)} onResetDaysChange={(value) => { onField("clientResetDays", value); onField("clientExpiry", nextRenewalDateInTimeZone(Number(value), siteTimezone)); }} quota={draft.clientQuota} resetDays={draft.clientResetDays} /> : <Alert><UsersIcon /><AlertTitle>{copy(language, "订阅额度在主订阅机管理", "Manage subscriber limits on the controller")}</AlertTitle><AlertDescription>{copy(language, "这里只配置当前节点套餐。创建完成后，请在订阅主机的“管理客户端”中配置用户和订阅总额度；如果还没有用户，也可以在那里添加。", "Only the current node plan is configured here. After creation, use Manage clients on the subscription controller to configure subscribers and their total allowances, or add the first subscriber if none exists.")}</AlertDescription></Alert>}
            <div className="rounded-xl border p-4"><div className="mb-4 flex items-start gap-3"><ShieldCheckIcon aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-muted-foreground" /><div><p className="text-sm font-medium">{copy(language, "手动指定 REALITY 安全目标", "Manual REALITY secure target")}</p><p className="mt-1 text-xs leading-5 text-muted-foreground">{copy(language, "默认留空。只有自动发现失败时，才同时填写目标域名和 SNI。", "Leave both empty by default. Enter a target hostname and SNI only if automatic discovery fails.")}</p></div></div><div className="flex flex-col gap-4"><Field><FieldLabel htmlFor="reality-target-host">Target host</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="reality-target-host" onChange={(event) => onField("targetHost", event.target.value.toLowerCase())} placeholder={copy(language, "留空自动发现", "Leave empty for automatic discovery")} spellCheck={false} value={draft.targetHost} /><FieldDescription>{targetPublicAddress ? <>{copy(language, "Agent 会按节点公网网络进行安全校验：", "The Agent validates it against the node's public network: ")}<a className="underline underline-offset-4" href={`https://bgp.he.net/ip/${encodeURIComponent(targetPublicAddress)}`} rel="noreferrer" target="_blank">bgp.he.net · {targetPublicAddress}</a></> : copy(language, "节点缺少已确认公网 IP，无法创建。", "The node has no confirmed public IP, so creation is unavailable.")}</FieldDescription></Field><Field><FieldLabel htmlFor="reality-server-name">Server name (SNI)</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="reality-server-name" onChange={(event) => onField("serverName", event.target.value.toLowerCase())} placeholder={copy(language, "留空自动发现", "Leave empty for automatic discovery")} spellCheck={false} value={draft.serverName} /></Field>{verification?.state === "pending" || verification?.state === "running" ? <Alert><Spinner /><AlertTitle>{copy(language, "正在节点侧校验", "Validating on the node")}</AlertTitle><AlertDescription>{copy(language, "正在检查 ASN、CDN/WAF 与 TLS 能力。", "Checking ASN, CDN/WAF, and TLS capabilities.")}</AlertDescription></Alert> : null}{verification?.state === "succeeded" ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "目标校验通过", "Target verified")}</AlertTitle><AlertDescription><code className="break-all">{verification.targetHost} → {verification.targetIp}</code><span className="mt-1 block">ASN {verification.targetAsn} · TLS 1.3 · X25519 · H2 · {copy(language, "证书有效", "certificate valid")}</span></AlertDescription></Alert> : null}</div></div>
          </FieldGroup>
        </details>
        {gateways.length === 0 ? <FieldError>{copy(language, "当前节点还不能独立提供公网 VLESS。请确认 Agent 启用了 Gateway 角色、已选为当前地点的网关，并在“网络”中确认公网地址和直接公网入口。", "This node cannot provide public VLESS independently yet. Enable its Agent Gateway role, select it as a gateway for this location, then confirm its public address and direct-public ingress in Network.")}</FieldError> : null}
        {!targetPublicAddress ? <FieldError>{copy(language, "当前 3x-ui 节点还没有已确认的公网 IPv4，请先在“网络”中确认。", "The current 3x-ui node has no confirmed public IPv4 address. Confirm it in Network first.")}</FieldError> : null}
        {regionMatch === "unavailable" && !draft.regionCode ? <FieldError>{copy(language, "地区自动识别失败，请在高级设置中选择地区。", "Automatic region detection failed. Select a region in Advanced settings.")}</FieldError> : null}
        {error ? <FieldError role="alert">{error}</FieldError> : null}
      </FieldGroup>
    </div>
    <SheetFooter><Button onClick={onCancel} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !draft.regionCode || !draft.name.trim() || collectInitialClient && !draft.clientName.trim() || !draft.gatewayID || !draft.hostname || manualTarget && (!draft.targetHost.trim() || !draft.serverName.trim()) || !targetPublicAddress} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <ShieldCheckIcon data-icon="inline-start" />}{manualTarget ? copy(language, "校验并创建", "Verify and create") : copy(language, "自动创建", "Create automatically")}</Button></SheetFooter>
  </form>;
}

function RealityResult({ busy, command, displayName, dnsProvider, error, gateway, language, onAcknowledge, onReveal, onRetry, shareURI }: { busy: boolean; command: ApplicationCommand; displayName: string; dnsProvider: "manual" | "cloudflare"; error: string; gateway?: AgentView; language: Language; onAcknowledge: () => void; onReveal: () => void; onRetry: () => void; shareURI: string }) {
	const publicationWarning = command.state === "succeeded" && Boolean(command.error);
	const recoveryRequired = command.state === "failed" && Boolean(command.reconciliationRequired);
  return <div aria-live="polite" className="flex flex-1 flex-col gap-4 px-4">
    <Alert>
      {command.state === "pending" || command.state === "running" ? <Spinner /> : <RadioTowerIcon />}
			<AlertTitle>{recoveryRequired ? copy(language, "需要继续恢复", "Recovery needs to continue") : publicationWarning ? copy(language, "REALITY 已创建，公网入口待处理", "REALITY was created; public access needs attention") : command.state === "succeeded" ? copy(language, "REALITY 已创建", "REALITY is ready") : command.state === "failed" ? copy(language, "创建失败", "Creation failed") : copy(language, "正在自动配置…", "Configuring automatically…")}</AlertTitle>
			<AlertDescription>{command.state === "pending" ? copy(language, "等待 Agent 接收任务。", "Waiting for the Agent to receive the task.") : command.state === "running" ? copy(language, "正在节点上扫描可用目标并创建入站。", "Scanning feasible targets and creating the inbound on the node.") : recoveryRequired ? copy(language, "Agent 无法确认上次操作是否已经写入 3x-ui。Vastora 已锁定原任务；继续恢复会核对原结果，不会创建第二个节点。", "The Agent could not confirm whether the last operation reached 3x-ui. Vastora locked the original task; continuing recovery verifies it instead of creating a second node.") : publicationWarning ? copy(language, `“${command.displayName ?? displayName}”和客户端凭据已安全保留；请在应用中重新添加或检查公网入口。`, `“${command.displayName ?? displayName}” and its client credential were kept safely. Re-add or inspect its public access entry in Apps.`) : command.state === "succeeded" ? copy(language, `“${command.displayName ?? displayName}”已创建独立节点套餐，客户端连接 ${command.hostname}:443。`, `“${command.displayName ?? displayName}” is ready with its independent node plan; clients connect to ${command.hostname}:443.`) : command.error}</AlertDescription>
    </Alert>
		{publicationWarning ? <FieldError>{userError(language, new Error(command.error))}</FieldError> : null}
    {command.state === "succeeded" && command.guardStatus === "ready" ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "防盗保护已启用", "Fallback guard enabled")}</AlertTitle><AlertDescription><code className="break-all">{command.targetHost} → {command.targetIp}:443</code><span className="mt-1 block">SNI {command.serverName} · ASN {command.targetAsn}</span></AlertDescription></Alert> : null}
    {command.state === "succeeded" && command.clientCreated && command.resultAvailable && !shareURI ? <Alert><KeyRoundIcon /><AlertTitle>{copy(language, "领取客户端链接", "Reveal the client link")}</AlertTitle><AlertDescription><p>{copy(language, "领取后在确认保存前可用同一浏览器恢复；Center 不会因一次断线提前销毁。", "After reveal, the same browser can recover it until you acknowledge saving it. A disconnect no longer destroys the copy.")}</p><Button className="mt-3" disabled={busy} onClick={onReveal} size="sm">{busy ? <Spinner data-icon="inline-start" /> : <KeyRoundIcon data-icon="inline-start" />}{copy(language, "显示客户端链接", "Reveal client link")}</Button></AlertDescription></Alert> : null}
    {shareURI ? <div><p className="mb-2 text-sm font-medium">{copy(language, "客户端链接", "Client link")}</p><div className="relative"><code className="block max-h-48 overflow-auto break-all rounded-xl bg-muted p-4 pr-14 text-xs leading-6">{shareURI}</code><CopyButton className="absolute right-2 top-2" label={copy(language, "复制链接", "Copy link")} language={language} size="icon" value={shareURI} /></div><p className="mt-2 text-xs text-muted-foreground">{copy(language, "导入并保存后请确认，Center 随后永久关闭再次显示。", "After importing and saving it, acknowledge below so Center permanently disables disclosure.")}</p><Button className="mt-3" disabled={busy} onClick={onAcknowledge} size="sm" variant="outline">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "我已保存", "I saved it")}</Button></div> : null}
    {command.state === "failed" && command.error ? <FieldError>{userError(language, new Error(command.error))}</FieldError> : null}
		{command.state === "failed" ? <Button className="w-fit" disabled={busy} onClick={onRetry} size="sm" variant="outline">{busy ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{recoveryRequired ? copy(language, "继续恢复", "Continue recovery") : copy(language, "修改后重试", "Edit and retry")}</Button> : null}
    {error ? <FieldError role="alert">{error}</FieldError> : null}
    {dnsProvider === "manual" && gateway?.networkProfile?.publicAddress ? <Alert><Globe2Icon /><AlertTitle>{copy(language, "还需添加一条 DNS 记录", "One DNS record is still needed")}</AlertTitle><AlertDescription><code className="break-all">A {command.hostname} → {gateway.networkProfile.publicAddress}</code></AlertDescription></Alert> : null}
  </div>;
}

function realityGateways(data: AppData, application: Application) {
  // A VLESS node is its own ingress and egress. HAProxy and 3x-ui share the
  // node's Docker network; selecting another Gateway would turn it into a relay.
  return data.agents.filter((agent) => agent.id === application.nodeId && agent.siteId === application.siteId && agent.connected && agent.capabilities.gateway && agent.networkProfile?.directPublic && agent.networkProfile.enabledKinds.includes("public") && data.sites.some((site) => site.id === application.siteId && site.gatewayNodes.includes(agent.id)));
}

function emptyDraft(): RealityDraft {
  return { name: "", regionCode: "", clientName: "", inboundQuota: "", inboundResetDay: "1", clientQuota: "", clientResetDays: "0", clientExpiry: "", gatewayID: "", hostname: "", dnsProvider: "manual", targetHost: "", serverName: "" };
}

function sameDraft(left: RealityDraft, right: RealityDraft) {
  return (Object.keys(left) as (keyof RealityDraft)[]).every((key) => left[key] === right[key]);
}
