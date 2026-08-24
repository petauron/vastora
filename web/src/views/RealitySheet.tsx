import { useEffect, useRef, useState, type FormEvent } from "react";
import { Globe2Icon, KeyRoundIcon, RadioTowerIcon, RotateCcwIcon, UsersIcon } from "lucide-react";
import { api } from "../api";
import type { AppData, Application, ApplicationCommand, AgentView } from "../types";
import type { Language } from "../translations";
import { useApplicationCommandExecutor } from "../hooks/use-application-command-executor";
import { defaultRealityHostname } from "./appAccess";
import { RegionCombobox, regionDisplayName } from "./RegionCombobox";
import { bytesFromGB, dateInputValueInTimeZone, endOfDayEpochInTimeZone, InboundTrafficPlanFields, nextRenewalDateInTimeZone, SubscriptionTrafficPlanFields } from "./TrafficPlanFields";
import { CopyButton, copy, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";

type RegionMatch = "idle" | "matching" | "matched" | "manual" | "unavailable";
type RealityDraft = {
  name: string;
  regionCode: string;
  clientName: string;
  inboundQuota: string;
  inboundResetDays: string;
  clientQuota: string;
  clientResetDays: string;
  clientExpiry: string;
  gatewayID: string;
  hostname: string;
  dnsProvider: "manual" | "cloudflare";
  target: string;
  sniHostname: string;
};

export function RealitySheet({ application, data, language, onClose, siteTimezone }: { application: Application | null; data: AppData; language: Language; onClose: () => void; siteTimezone?: string }) {
  const [draft, setDraft] = useState<RealityDraft>(emptyDraft());
  const [collectInitialClient, setCollectInitialClient] = useState(false);
  const [regionMatch, setRegionMatch] = useState<RegionMatch>("idle");
  const [command, setCommand] = useState<ApplicationCommand | null>(null);
  const [shareURI, setShareURI] = useState("");
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
      inboundResetDays: "0",
      clientQuota: "",
      clientResetDays: "0",
      clientExpiry: "",
      gatewayID: gateways[0]?.id ?? "",
      hostname: defaultRealityHostname(data, application),
      dnsProvider: cloudflareReady ? "cloudflare" : "manual",
      target: "",
      sniHostname: ""
    };
    regionRequest.current += 1;
    baseline.current = initial;
    setDraft(initial);
    setCollectInitialClient(firstControllerReality);
    setRegionMatch("idle");
    setCommand(null);
    setShareURI("");
    setBusy(false);
    setError("");
    let cancelled = false;
		void api.latestApplicationCommand(application.id, "3xui.reality.create").then((latest) => {
			if (cancelled || latest.state === "failed" && !latest.reconciliationRequired) return;
      setCommand(latest);
      setDraft((current) => ({ ...current, gatewayID: latest.gatewayNodeId, hostname: latest.hostname, dnsProvider: latest.dnsProvider }));
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

  const setField = <K extends keyof RealityDraft>(field: K, value: RealityDraft[K]) => setDraft((current) => ({ ...current, [field]: value }));
  const gateway = data.agents.find((agent) => agent.id === draft.gatewayID);
  const displayName = regionDisplayName(draft.regionCode, draft.name);
  const manualRecordType = gateway?.networkProfile?.publicAddress?.includes(":") ? "AAAA" : "A";

  const requestClose = () => {
    if (dirty && !window.confirm(copy(language, "放弃尚未保存的修改？", "Discard unsaved changes?"))) return;
    onClose();
  };

	const reveal = async () => {
    if (!command || command.state !== "succeeded" || !command.clientCreated || !command.resultAvailable) return;
    setBusy(true);
    setError("");
    try {
      setShareURI((await api.revealApplicationCommand(command.id)).shareUri);
      setCommand({ ...command, resultAvailable: false });
    } catch (revealError) {
      setError(userError(language, revealError));
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
    const inboundRenewalDays = Number(draft.inboundResetDays || 0);
    const clientTotalBytes = bytesFromGB(draft.clientQuota);
    const clientRenewalDays = Number(draft.clientResetDays || 0);
    const clientExpiryTime = draft.clientExpiry ? endOfDayEpochInTimeZone(draft.clientExpiry, siteTimezone) : 0;
    if (!Number.isFinite(inboundTotalBytes) || inboundTotalBytes < 0 || !Number.isInteger(inboundRenewalDays) || inboundRenewalDays < 0 || collectInitialClient && (!Number.isFinite(clientTotalBytes) || clientTotalBytes < 0 || !Number.isInteger(clientRenewalDays) || clientRenewalDays < 0 || clientRenewalDays > 0 && !clientExpiryTime)) {
      setError(copy(language, "请检查订阅额度和节点套餐。", "Check the subscription allowance and node plan."));
      return;
    }
    setBusy(true);
    setError("");
    try {
      await execute(() => api.createRealityCommand({
        applicationId: application.id,
        regionCode: draft.regionCode,
        name: draft.name,
        gatewayNodeId: draft.gatewayID,
        hostname: draft.hostname,
        dnsProvider: draft.dnsProvider,
        target: draft.target || undefined,
        sniHostname: draft.sniHostname || undefined,
        inboundTotalBytes,
        inboundResetDays: inboundRenewalDays,
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
        <SheetDescription>{command ? copy(language, "Vastora 正在节点内配置 3x-ui、共享 443 网关和 DNS。", "Vastora is configuring 3x-ui, the shared 443 gateway, and DNS on the node.") : copy(language, "选择公网入口后，Vastora 会自动识别地区并生成标准节点名。", "After choosing a public entry, Vastora detects its region and creates a standard node name.")}</SheetDescription>
      </SheetHeader>
		{command ? <RealityResult busy={busy} command={command} displayName={displayName} dnsProvider={draft.dnsProvider} error={error} gateway={gateway} language={language} manualRecordType={manualRecordType} onReveal={() => void reveal()} onRetry={() => { if (command.reconciliationRequired) { void resumeReconciliation(); return; } baseline.current = draft; setCommand(null); setError(""); }} shareURI={shareURI} /> : <RealityForm busy={busy} cloudflareReady={cloudflareReady} collectInitialClient={collectInitialClient} displayName={displayName} draft={draft} error={error} gateway={gateway} gateways={gateways} language={language} onCancel={requestClose} onField={setField} onRegion={(code) => { regionRequest.current += 1; setField("regionCode", code); setRegionMatch("manual"); }} onSubmit={submit} regionMatch={regionMatch} siteTimezone={siteTimezone} />}
      {command ? <SheetFooter><Button onClick={requestClose}>{copy(language, shareURI ? "完成" : "关闭", shareURI ? "Done" : "Close")}</Button></SheetFooter> : null}
    </SheetContent>
  </Sheet>;
}

function RealityForm({ busy, cloudflareReady, collectInitialClient, displayName, draft, error, gateway, gateways, language, onCancel, onField, onRegion, onSubmit, regionMatch, siteTimezone }: {
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
}) {
  return <form className="flex min-h-0 flex-1 flex-col" onSubmit={onSubmit}>
    <div className="flex-1 overflow-y-auto px-4">
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="reality-gateway">{copy(language, "公网入口", "Public entry")}</FieldLabel>
          <NativeSelect id="reality-gateway" onChange={(event) => onField("gatewayID", event.target.value)} required value={draft.gatewayID}>
            <option disabled value="">{copy(language, "没有可用的公网网关", "No public gateway available")}</option>
            {gateways.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
          </NativeSelect>
          <FieldDescription>{copy(language, "按这个入口的真实公网 IP 自动识别节点地区。", "The node region is detected from this entry's confirmed public IP.")}</FieldDescription>
        </Field>
        <Field>
          <FieldLabel htmlFor="reality-region">{copy(language, "地区前缀", "Region prefix")}</FieldLabel>
          <RegionCombobox id="reality-region" language={language} onValueChange={onRegion} value={draft.regionCode} />
          <FieldDescription aria-live="polite">{regionMatch === "matching" ? copy(language, "正在根据公网 IP 识别…", "Detecting from the public IP…") : regionMatch === "matched" ? copy(language, `已根据 ${gateway?.networkProfile?.publicAddress ?? "公网 IP"} 自动匹配，可手动修改。`, `Matched from ${gateway?.networkProfile?.publicAddress ?? "the public IP"}; you can change it.`) : regionMatch === "unavailable" ? copy(language, "未能自动识别，请搜索并选择地区。", "Automatic detection was unavailable. Search and choose a region.") : copy(language, "支持搜索中文、英文和 ISO 国家/地区代码。", "Search by localized name, English name, or ISO code.")}</FieldDescription>
        </Field>
        <Field>
          <FieldLabel htmlFor="reality-name">{copy(language, "节点名称", "Node name")}</FieldLabel>
          <Input id="reality-name" maxLength={48} onChange={(event) => onField("name", event.target.value)} required value={draft.name} />
          <FieldDescription>{copy(language, "只填写线路或主机名，例如“Oracle 9929”。", "Enter only the route or host name, for example “Oracle 9929”.")}</FieldDescription>
        </Field>
        {displayName ? <div className="rounded-xl border bg-muted/40 p-3"><p className="text-xs text-muted-foreground">{copy(language, "订阅中显示", "Shown in subscriptions")}</p><p className="mt-1 font-medium">{displayName}</p></div> : null}
        <InboundTrafficPlanFields idPrefix="reality-inbound" language={language} nextResetAt="" onQuotaChange={(value) => onField("inboundQuota", value)} onResetDaysChange={(value) => onField("inboundResetDays", value)} quota={draft.inboundQuota} resetDays={draft.inboundResetDays} />
        {collectInitialClient ? <>
          <Field>
            <FieldLabel htmlFor="reality-client-name">{copy(language, "初始客户端名称", "Initial client name")}</FieldLabel>
            <Input id="reality-client-name" maxLength={64} onChange={(event) => onField("clientName", event.target.value)} required value={draft.clientName} />
            <FieldDescription>{copy(language, "用于第一台设备，例如手机、MacBook 或家庭路由器。", "Used for the first device, for example Phone, MacBook, or Home router.")}</FieldDescription>
          </Field>
          <SubscriptionTrafficPlanFields expiry={draft.clientExpiry} idPrefix="reality-subscription" language={language} minimumDate={dateInputValueInTimeZone(new Date(), siteTimezone)} onExpiryChange={(value) => onField("clientExpiry", value)} onQuotaChange={(value) => onField("clientQuota", value)} onResetDaysChange={(value) => { onField("clientResetDays", value); onField("clientExpiry", nextRenewalDateInTimeZone(Number(value), siteTimezone)); }} quota={draft.clientQuota} resetDays={draft.clientResetDays} />
        </> : <Alert><UsersIcon /><AlertTitle>{copy(language, "订阅额度在主订阅机管理", "Manage subscriber limits on the controller")}</AlertTitle><AlertDescription>{copy(language, "这里只配置当前节点套餐。创建完成后，请在订阅主机的“管理客户端”中配置用户和订阅总额度；如果还没有用户，也可以在那里添加。", "Only the current node plan is configured here. After creation, use Manage clients on the subscription controller to configure subscribers and their total allowances, or add the first subscriber if none exists.")}</AlertDescription></Alert>}
        <Field>
          <FieldLabel htmlFor="reality-hostname">{copy(language, "连接域名", "Connection hostname")}</FieldLabel>
          <Input autoCapitalize="none" autoCorrect="off" id="reality-hostname" onChange={(event) => onField("hostname", event.target.value.toLowerCase())} required spellCheck={false} value={draft.hostname} />
          <FieldDescription>{copy(language, "按“reality.节点.位置.域名空间”自动生成。", "Generated as “reality.node.location.domain-namespace”.")}</FieldDescription>
        </Field>
        <Field>
          <FieldLabel htmlFor="reality-dns">DNS</FieldLabel>
          <NativeSelect id="reality-dns" onChange={(event) => onField("dnsProvider", event.target.value as RealityDraft["dnsProvider"])} value={draft.dnsProvider}>
            <option value="manual">{copy(language, "手动添加 A/AAAA", "Add A/AAAA manually")}</option>
            {cloudflareReady ? <option value="cloudflare">{copy(language, "Cloudflare 自动管理", "Manage with Cloudflare")}</option> : null}
          </NativeSelect>
        </Field>
        <details className="rounded-xl border p-3">
          <summary className="cursor-pointer text-sm font-medium">{copy(language, "高级：自定义伪装目标", "Advanced: custom camouflage target")}</summary>
          <div className="mt-4 flex flex-col gap-4">
            <Field><FieldLabel htmlFor="reality-target">Target</FieldLabel><Input id="reality-target" onChange={(event) => onField("target", event.target.value.toLowerCase())} placeholder="www.example.com:443" value={draft.target} /></Field>
            <Field><FieldLabel htmlFor="reality-sni">SNI</FieldLabel><Input id="reality-sni" onChange={(event) => onField("sniHostname", event.target.value.toLowerCase())} placeholder="www.example.com" value={draft.sniHostname} /><FieldDescription>{copy(language, "留空时由应用节点实时扫描并选择可行目标；自定义时两项必须一起填写。", "Leave both empty for a live node-local scan. Custom values must be provided together.")}</FieldDescription></Field>
          </div>
        </details>
        {gateways.length === 0 ? <FieldError>{copy(language, "此位置还没有已确认公网能力的网关。请先在“网络”中确认公网地址并允许直接公网。", "This location has no gateway with confirmed public ingress. Confirm its public address and direct-public permission in Network first.")}</FieldError> : null}
        {error ? <FieldError role="alert">{error}</FieldError> : null}
      </FieldGroup>
    </div>
    <SheetFooter><Button onClick={onCancel} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !draft.regionCode || !draft.name.trim() || collectInitialClient && !draft.clientName.trim() || !draft.gatewayID || !draft.hostname || Boolean(draft.target) !== Boolean(draft.sniHostname)} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <RadioTowerIcon data-icon="inline-start" />}{copy(language, "自动创建", "Create automatically")}</Button></SheetFooter>
  </form>;
}

function RealityResult({ busy, command, displayName, dnsProvider, error, gateway, language, manualRecordType, onReveal, onRetry, shareURI }: { busy: boolean; command: ApplicationCommand; displayName: string; dnsProvider: "manual" | "cloudflare"; error: string; gateway?: AgentView; language: Language; manualRecordType: "A" | "AAAA"; onReveal: () => void; onRetry: () => void; shareURI: string }) {
	const publicationWarning = command.state === "succeeded" && Boolean(command.error);
	const recoveryRequired = command.state === "failed" && Boolean(command.reconciliationRequired);
  return <div aria-live="polite" className="flex flex-1 flex-col gap-4 px-4">
    <Alert>
      {command.state === "pending" || command.state === "running" ? <Spinner /> : <RadioTowerIcon />}
			<AlertTitle>{recoveryRequired ? copy(language, "需要继续恢复", "Recovery needs to continue") : publicationWarning ? copy(language, "REALITY 已创建，公网入口待处理", "REALITY was created; public access needs attention") : command.state === "succeeded" ? copy(language, "REALITY 已创建", "REALITY is ready") : command.state === "failed" ? copy(language, "创建失败", "Creation failed") : copy(language, "正在自动配置…", "Configuring automatically…")}</AlertTitle>
			<AlertDescription>{command.state === "pending" ? copy(language, "等待 Agent 接收任务。", "Waiting for the Agent to receive the task.") : command.state === "running" ? copy(language, "正在节点上扫描可用目标并创建入站。", "Scanning feasible targets and creating the inbound on the node.") : recoveryRequired ? copy(language, "Agent 无法确认上次操作是否已经写入 3x-ui。Vastora 已锁定原任务；继续恢复会核对原结果，不会创建第二个节点。", "The Agent could not confirm whether the last operation reached 3x-ui. Vastora locked the original task; continuing recovery verifies it instead of creating a second node.") : publicationWarning ? copy(language, `“${command.displayName ?? displayName}”和客户端凭据已安全保留；请在应用中重新添加或检查公网入口。`, `“${command.displayName ?? displayName}” and its client credential were kept safely. Re-add or inspect its public access entry in Apps.`) : command.state === "succeeded" ? copy(language, `“${command.displayName ?? displayName}”已创建独立节点套餐，客户端连接 ${command.hostname}:443。`, `“${command.displayName ?? displayName}” is ready with its independent node plan; clients connect to ${command.hostname}:443.`) : command.error}</AlertDescription>
    </Alert>
		{publicationWarning ? <FieldError>{userError(language, new Error(command.error))}</FieldError> : null}
    {command.state === "succeeded" && command.clientCreated && command.resultAvailable && !shareURI ? <Alert><KeyRoundIcon /><AlertTitle>{copy(language, "客户端链接只显示一次", "The client link is shown once")}</AlertTitle><AlertDescription><p>{copy(language, "准备好立即导入客户端后再显示；显示后 Center 会删除保存的副本。", "Reveal it only when you are ready to import it. Center deletes its saved copy afterward.")}</p><Button className="mt-3" disabled={busy} onClick={onReveal} size="sm">{busy ? <Spinner data-icon="inline-start" /> : <KeyRoundIcon data-icon="inline-start" />}{copy(language, "显示一次性链接", "Reveal one-time link")}</Button></AlertDescription></Alert> : null}
    {shareURI ? <div><p className="mb-2 text-sm font-medium">{copy(language, "一次性客户端链接", "One-time client link")}</p><div className="relative"><code className="block max-h-48 overflow-auto break-all rounded-xl bg-muted p-4 pr-14 text-xs leading-6">{shareURI}</code><CopyButton className="absolute right-2 top-2" label={copy(language, "复制链接", "Copy link")} language={language} size="icon" value={shareURI} /></div><p className="mt-2 text-xs text-muted-foreground">{copy(language, "请立即导入客户端并保存；Center 已删除这份一次性链接。", "Import and save it now. Center has deleted its one-time copy.")}</p></div> : null}
    {command.state === "failed" && command.error ? <FieldError>{userError(language, new Error(command.error))}</FieldError> : null}
		{command.state === "failed" ? <Button className="w-fit" disabled={busy} onClick={onRetry} size="sm" variant="outline">{busy ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{recoveryRequired ? copy(language, "继续恢复", "Continue recovery") : copy(language, "修改后重试", "Edit and retry")}</Button> : null}
    {error ? <FieldError role="alert">{error}</FieldError> : null}
    {dnsProvider === "manual" && gateway?.networkProfile?.publicAddress ? <Alert><Globe2Icon /><AlertTitle>{copy(language, "还需添加一条 DNS 记录", "One DNS record is still needed")}</AlertTitle><AlertDescription><code className="break-all">{manualRecordType} {command.hostname} → {gateway.networkProfile.publicAddress}</code></AlertDescription></Alert> : null}
  </div>;
}

function realityGateways(data: AppData, application: Application) {
  return data.agents.filter((agent) => agent.siteId === application.siteId && agent.connected && agent.capabilities.gateway && agent.networkProfile?.directPublic && agent.networkProfile.enabledKinds.includes("public") && data.sites.some((site) => site.id === application.siteId && site.gatewayNodes.includes(agent.id))).sort((left, right) => Number(right.id === application.nodeId) - Number(left.id === application.nodeId) || left.name.localeCompare(right.name));
}

function emptyDraft(): RealityDraft {
  return { name: "", regionCode: "", clientName: "", inboundQuota: "", inboundResetDays: "0", clientQuota: "", clientResetDays: "0", clientExpiry: "", gatewayID: "", hostname: "", dnsProvider: "manual", target: "", sniHostname: "" };
}

function sameDraft(left: RealityDraft, right: RealityDraft) {
  return (Object.keys(left) as (keyof RealityDraft)[]).every((key) => left[key] === right[key]);
}
