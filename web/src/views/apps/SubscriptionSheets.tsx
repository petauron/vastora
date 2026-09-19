import { useEffect, useRef, useState, type FormEvent } from "react";
import { CheckCircle2Icon, Globe2Icon, PencilIcon, RefreshCwIcon, RotateCcwIcon, ShieldAlertIcon, Trash2Icon } from "lucide-react";
import { api } from "../../api";
import type { AppData, Mutate } from "../../App";
import type { Application, ApplicationCommand, AppView, Service } from "../../types";
import type { Language } from "../../translations";
import { useApplicationCommandExecutor } from "../../hooks/use-application-command-executor";
import { defaultPublicationHostname, gatewaysForKind, localized, publicationKindLabel } from "../appAccess";
import { copy, StateBadge, TechnicalError, userError } from "../shared";
import { NodeProtocolControls } from "../NodeProtocolControls";
import { RegionCombobox, regionBaseName, regionDisplayName } from "../RegionCombobox";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { SelectControl } from "@/components/SelectControl";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

export function SubscriptionSheet({ application, data, language, mutate, onClose }: { application: Application | null; data: AppData; language: Language; mutate: Mutate; onClose: () => void }) {
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
        if (completed?.state === "failed") throw new Error(completed.error || "The Vastora Proxy subscription configuration failed");
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
      ? copy(language, "Vastora Proxy 配置失败", "Vastora Proxy configuration failed")
      : publicationProblem
        ? copy(language, "订阅入口需要处理", "Subscription access needs attention")
        : commandActive
          ? copy(language, "正在自动配置…", "Configuring automatically…")
          : configured
            ? copy(language, "Vastora Proxy 已配置，入口尚未确认", "Vastora Proxy is configured; access is not verified")
            : copy(language, "订阅入口尚未确认", "Subscription access is not verified");
  const statusDescription = ready
    ? copy(language, "同一个订阅地址会自动适配 OpenClash、Mihomo 和其他客户端。", "One subscription URL automatically adapts to OpenClash, Mihomo, and other clients.")
    : commandFailed
      ? command?.error || copy(language, "Vastora Proxy 没有接受这次配置。", "Vastora Proxy did not accept this configuration.")
      : publicationProblem
        ? publication?.lastError || copy(language, "域名或 HTTPS 检查没有通过。", "The hostname or HTTPS check did not pass.")
        : commandActive
          ? copy(language, "正在创建 HTTPS 入口并同步 Vastora Proxy 设置。", "Creating the HTTPS access point and syncing Vastora Proxy settings.")
          : configured
            ? copy(language, "配置命令已经成功，不需要继续等待。请立即检查入口；如仍失败，可重试配置。", "The configuration command succeeded, so you do not need to keep waiting. Check the access point now, or retry configuration if it still fails.")
            : copy(language, "入口尚未报告就绪。你可以立即检查，不需要停留在此页面等待。", "The access point has not reported ready. Check it now; you do not need to keep this sheet open.");
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(application)}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, "公网订阅", "Public subscription")}</SheetTitle><SheetDescription>{copy(language, "Vastora 会发布独立订阅服务，并把公网域名自动写入 3x-ui。管理面板仍只在私网开放。", "Vastora publishes the separate subscription service and writes its public hostname into 3x-ui. The admin panel stays private.")}</SheetDescription></SheetHeader>{command || publication ? <div aria-live="polite" className="flex flex-1 flex-col gap-4 overflow-y-auto px-4"><Alert variant={commandFailed || publicationProblem ? "destructive" : "default"}>{ready ? <Globe2Icon /> : commandActive ? <Spinner /> : commandFailed || publicationProblem ? <ShieldAlertIcon /> : <RefreshCwIcon />}<AlertTitle>{statusTitle}</AlertTitle><AlertDescription>{statusDescription}</AlertDescription></Alert>{publication ? <div className="rounded-xl border bg-muted/40 p-4"><div className="flex items-start justify-between gap-3"><div className="min-w-0"><p className="truncate text-sm font-medium">{publication.hostname}</p><p className="mt-1 text-xs text-muted-foreground">{publicationKindLabel(language, publication.kind)}</p></div><StateBadge value={publication.status} /></div>{publication.lastError ? <div className="mt-3"><TechnicalError error={publication.lastError} language={language} /></div> : null}</div> : null}{ready ? <div className="rounded-xl border bg-muted/40 p-4"><p className="text-sm font-medium">{copy(language, "一个地址，自动适配", "One URL, automatic format")}</p><div className="mt-3 flex flex-wrap gap-2"><Badge variant="secondary">OpenClash · Mihomo · {copy(language, "其他客户端", "Other clients")}</Badge></div><p className="mt-3 text-xs leading-5 text-muted-foreground">{copy(language, "关闭此页后，在“管理客户端”中为每台设备复制完整地址。不要复制只有域名的服务基址。", "Close this sheet, then copy the complete per-device URL from Manage clients. Do not copy the hostname-only service base.")}</p><Button className="mt-3" disabled={busyAction !== null || !gatewayID} onClick={() => void configure()} size="sm" variant="outline">{busyAction === "configure" ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "同步订阅设置", "Sync subscription settings")}</Button></div> : null}{!ready ? <div className="flex flex-wrap gap-2"><Button disabled={busyAction !== null || !publication} onClick={() => void verify()} size="sm">{busyAction === "verify" ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "立即检查入口", "Check access now")}</Button>{commandFailed || publicationProblem ? <Button disabled={busyAction !== null || !gatewayID} onClick={() => void configure()} size="sm" variant="outline">{busyAction === "configure" ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{copy(language, "重试配置", "Retry configuration")}</Button> : null}</div> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={submit}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Alert><Globe2Icon /><AlertTitle>{copy(language, "推荐使用 Cloudflare 安全通道", "Cloudflare secure tunnel is recommended")}</AlertTitle><AlertDescription>{copy(language, "无需开放新的公网端口，并自动提供 HTTPS。", "It needs no new public port and provides HTTPS automatically.")}</AlertDescription></Alert><Field><FieldLabel htmlFor="subscription-hostname">{copy(language, "订阅域名（可自定义）", "Subscription hostname (customizable)")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="subscription-hostname" onChange={(event) => setHostname(event.target.value.toLowerCase())} placeholder={copy(language, "留空时自动生成", "Generated automatically when empty")} spellCheck={false} value={hostname} /><FieldDescription>{copy(language, "Center 默认生成不包含 3x-ui 或节点信息的 128-bit 随机域名；只用于订阅下载。", "Center generates a random 128-bit hostname without 3x-ui or node details by default; it is used only for subscription downloads.")}</FieldDescription></Field><details className="rounded-xl border p-3"><summary className="cursor-pointer text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}</summary><div className="mt-4 flex flex-col gap-4"><Field><FieldLabel htmlFor="subscription-kind">{copy(language, "公网方式", "Public method")}</FieldLabel><SelectControl id="subscription-kind" onValueChange={(value) => selectKind(value as "cloudflare_tunnel" | "public_direct")} options={[...(cloudflareReady && tunnelGateways.length ? [{ value: "cloudflare_tunnel", label: "Cloudflare Tunnel · HTTPS" }] : []), { value: "public_direct", label: copy(language, "公网网关 · HTTPS", "Public gateway · HTTPS"), disabled: !directGateways.length }]} value={kind} /></Field><Field><FieldLabel htmlFor="subscription-gateway">{copy(language, "入口节点", "Entry node")}</FieldLabel><SelectControl id="subscription-gateway" onValueChange={setGatewayID} options={[{ value: "", label: copy(language, "没有可用节点", "No node available"), disabled: true }, ...gateways.map((agent) => ({ value: agent.id, label: agent.name }))]} required value={gatewayID} /></Field></div></details>{!cloudflareReady ? <FieldError>{copy(language, "请先连接 Cloudflare，才能自动管理订阅域名和 HTTPS。", "Connect Cloudflare first to manage the subscription hostname and HTTPS automatically.")}</FieldError> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busyAction !== null || !cloudflareReady || !gatewayID} type="submit">{busyAction === "configure" ? <Spinner data-icon="inline-start" /> : <Globe2Icon data-icon="inline-start" />}{copy(language, "开启公网订阅", "Enable public subscription")}</Button></SheetFooter></form>}<SheetFooter>{command || publication ? <Button onClick={onClose}>{copy(language, "关闭", "Close")}</Button> : null}</SheetFooter></SheetContent></Sheet>;
}

export function RealityRenameSheet({ data, language, mutate, onClose, service }: { data: AppData; language: Language; mutate: Mutate; onClose: () => void; service: Service | null }) {
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
	return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(service)}><SheetContent className="sm:max-w-md"><SheetHeader><SheetTitle>{copy(language, "修改订阅节点", "Edit subscription node")}</SheetTitle><SheetDescription>{copy(language, "修改节点名称和可用协议，订阅地址不变。", "Edit the node name and available protocols without changing the subscription URL.")}</SheetDescription></SheetHeader>{command ? <div aria-live="polite" className="flex flex-1 flex-col gap-4 px-4"><Alert variant={command.state === "failed" ? "destructive" : "default"}>{active ? <Spinner /> : command.state === "succeeded" ? <CheckCircle2Icon /> : <ShieldAlertIcon />}<AlertTitle>{active ? copy(language, "正在同步名称…", "Syncing the name…") : command.state === "succeeded" ? copy(language, "名称已更新", "Name updated") : copy(language, "重命名失败", "Rename failed")}</AlertTitle><AlertDescription>{active ? copy(language, "等待订阅主机更新 3x-ui 入站。", "Waiting for the subscription controller to update the 3x-ui inbound.") : command.state === "succeeded" ? copy(language, `现在显示为“${command.displayName ?? displayName}”。客户端刷新订阅后会看到新名称。`, `It is now shown as “${command.displayName ?? displayName}”. Clients see it after refreshing the subscription.`) : command.error}</AlertDescription></Alert>{error ? <FieldError role="alert">{error}</FieldError> : null}</div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup>{service ? <NodeProtocolControls serviceId={service.id} language={language} onUpdated={async () => { await mutate(async () => undefined, copy(language, "节点协议已更新，请刷新客户端订阅。", "Node protocols updated. Refresh your client subscription.")); }} /> : null}<Field><FieldLabel htmlFor="reality-rename-region">{copy(language, "地区前缀", "Region prefix")}</FieldLabel><RegionCombobox id="reality-rename-region" language={language} onValueChange={(code) => { regionRequest.current += 1; setRegionCode(code); setRegionMatch("manual"); }} value={regionCode} /><FieldDescription aria-live="polite">{regionMatch === "matching" ? copy(language, "正在根据公网 IP 识别…", "Detecting from the public IP…") : regionMatch === "matched" ? copy(language, `已根据 ${gateway?.networkProfile?.publicAddress ?? "公网 IP"} 自动匹配，可手动修改。`, `Matched from ${gateway?.networkProfile?.publicAddress ?? "the public IP"}; you can change it.`) : regionMatch === "unavailable" ? copy(language, "未能自动识别，请搜索并选择地区。", "Automatic detection was unavailable. Search and choose a region.") : copy(language, "支持搜索中文、英文和 ISO 国家/地区代码。", "Search by localized name, English name, or ISO code.")}</FieldDescription></Field><Field><FieldLabel htmlFor="reality-rename-name">{copy(language, "节点名称", "Node name")}</FieldLabel><Input id="reality-rename-name" maxLength={48} onChange={(event) => setName(event.target.value)} required value={name} /><FieldDescription>{copy(language, "填写线路或主机名，例如“洛杉矶优质线路”。", "Enter the route or host name, for example “Los Angeles premium route”.")}</FieldDescription></Field>{displayName ? <div className="rounded-xl border bg-muted/40 p-3"><p className="text-xs text-muted-foreground">{copy(language, "订阅中显示", "Shown in subscriptions")}</p><p className="mt-1 font-medium">{displayName}</p></div> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !regionCode || !name.trim() || displayName === (service?.displayName || service?.name)} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <PencilIcon data-icon="inline-start" />}{copy(language, "保存名称", "Save name")}</Button></SheetFooter></form>}<SheetFooter>{command ? <Button disabled={active} onClick={onClose}>{copy(language, "完成", "Done")}</Button> : null}</SheetFooter></SheetContent></Sheet>;
}

export function UninstallSheet({ application, app, language, onClose, onSubmit }: { application: Application | null; app?: AppView; language: Language; onClose: () => void; onSubmit: (application: Application, deleteData: boolean) => Promise<void> }) {
  const [deleteData, setDeleteData] = useState(false); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  useEffect(() => { if (application) { setDeleteData(false); setError(""); } }, [application]);
  const submit = async () => { if (!application) return; setBusy(true); setError(""); try { await onSubmit(application, deleteData); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(application)}><SheetContent><SheetHeader><SheetTitle>{copy(language, `卸载 ${app ? localized(app, language, "name") : application?.name ?? ""}`, `Uninstall ${app ? localized(app, language, "name") : application?.name ?? ""}`)}</SheetTitle><SheetDescription>{copy(language, "卸载会停止应用并移除所有访问入口。默认保留持久数据，便于以后重新安装。", "Uninstalling stops the app and removes all access points. Persistent data is kept by default for a later reinstall.")}</SheetDescription></SheetHeader><div className="px-4"><Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="delete-data">{copy(language, "同时永久删除应用数据", "Permanently delete app data too")}</FieldLabel><FieldDescription>{copy(language, "此操作不可恢复，包括配置、账号和历史数据。", "This cannot be undone and includes configuration, accounts, and history.")}</FieldDescription></div><Switch checked={deleteData} id="delete-data" onCheckedChange={setDeleteData} /></Field>{deleteData ? <Alert className="mt-4" variant="destructive"><Trash2Icon /><AlertTitle>{copy(language, "应用数据将永久删除", "App data will be permanently deleted")}</AlertTitle></Alert> : null}{error ? <FieldError className="mt-3">{error}</FieldError> : null}</div><SheetFooter><Button onClick={onClose} variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} onClick={() => void submit()} variant={deleteData ? "destructive" : "default"}>{busy ? <Spinner data-icon="inline-start" /> : null}{deleteData ? copy(language, "卸载并删除数据", "Uninstall and delete data") : copy(language, "卸载并保留数据", "Uninstall and keep data")}</Button></SheetFooter></SheetContent></Sheet>;
}
