import { useEffect, useMemo, useState, type FormEvent } from "react";
import { ArrowRightLeftIcon, CheckCircle2Icon, KeyRoundIcon, PencilIcon, PlusIcon, RadioTowerIcon, RouteIcon, ShieldAlertIcon, ShieldCheckIcon, Trash2Icon } from "lucide-react";
import { api } from "../api";
import type { AppData, Mutate } from "../App";
import type { MeridianAccount, MeridianAccountCreated } from "../meridian-types";
import type { Application, ApplicationCommand } from "../types";
import type { Language } from "../translations";
import { useApplicationCommandExecutor } from "../hooks/use-application-command-executor";
import { SelectControl } from "@/components/SelectControl";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { CopyButton, StateBadge, TechnicalError, copy, userError } from "./shared";
import { RegionCombobox, regionBaseName } from "./RegionCombobox";
import { IPQualityComparison } from "./IPQualityComparison";
import { assessmentLabel } from "./IPAssessment";
import type { IPQualityCheck } from "../ip-quality-types";

const gibibyte = 1024 ** 3;

export function MeridianManagerSheet({ application, data, language, mutate, onClose }: { application: Application | null; data: AppData; language: Language; mutate: Mutate; onClose: () => void }) {
  const [tab, setTab] = useState("overview");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [created, setCreated] = useState<MeridianAccountCreated | null>(null);
  const inventory = data.meridian;
  const endpoint = inventory.endpoints.find((value) => value.applicationId === application?.id);
  const endpointPublication = endpoint ? data.publications.find((value) => value.serviceId === endpoint.serviceId && value.kind === "public_shared_443" && value.status !== "stopped") : undefined;
  const managementReady = inventory.cutover.subscriptionAuthority === "meridian" && (inventory.cutover.state === "not_required" || inventory.cutover.state === "complete");

  useEffect(() => {
    setTab("overview");
    setBusy("");
    setError("");
    setCreated(null);
  }, [application?.id]);

  const run = async (key: string, operation: () => Promise<unknown>, success: string) => {
    setBusy(key);
    setError("");
    try {
      await mutate(operation, success);
    } catch (caught) {
      setError(userError(language, caught));
    } finally {
      setBusy("");
    }
  };

  return <Sheet open={Boolean(application)} onOpenChange={(open) => { if (!open) onClose(); }}>
    <SheetContent className="w-full sm:max-w-3xl">
      <SheetHeader>
        <SheetTitle>{copy(language, "Meridian 访问管理", "Meridian access management")}</SheetTitle>
        <SheetDescription>{application?.name} · {copy(language, "账号、入口、共享流量与固定落地由 Center 统一管理。", "Center is the authority for accounts, entries, shared usage, and fixed egress routes.")}</SheetDescription>
      </SheetHeader>
      <Tabs className="min-h-0 flex-1 px-4" value={tab} onValueChange={setTab}>
        <TabsList variant="line">
          <TabsTrigger value="overview">{copy(language, "概览", "Overview")}</TabsTrigger>
          <TabsTrigger disabled={!managementReady} value="accounts">{copy(language, "账号", "Accounts")}<Badge variant="secondary">{inventory.accounts.length}</Badge></TabsTrigger>
          <TabsTrigger disabled={!managementReady} value="routes">{copy(language, "落地", "Egress")}<Badge variant="secondary">{inventory.grants.length}</Badge></TabsTrigger>
        </TabsList>
        <div className="mt-4 max-h-[calc(100vh-12rem)] overflow-y-auto pb-6">
          {inventory.cutover.lastError ? <Alert className="mb-4" variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "迁移需要处理", "Migration needs attention")}</AlertTitle><AlertDescription>{inventory.cutover.lastError}</AlertDescription></Alert> : null}
          <TabsContent value="overview"><div className="flex flex-col gap-4"><CutoverPanel cutover={inventory.cutover} language={language} busy={busy} error={error} onRun={run} />{managementReady || endpoint?.status === "failed" ? <EndpointPanel application={application} endpoint={endpoint} publication={endpointPublication} language={language} busy={busy} error={error} onRun={run} /> : null}</div></TabsContent>
          <TabsContent value="accounts"><AccountsPanel created={created} data={data} language={language} busy={busy} error={error} onCreated={setCreated} onRun={run} /></TabsContent>
          <TabsContent value="routes"><RoutesPanel data={data} endpointId={endpoint?.id ?? ""} language={language} busy={busy} error={error} onRun={run} /></TabsContent>
        </div>
      </Tabs>
      <SheetFooter><Button onClick={onClose}>{copy(language, "关闭", "Close")}</Button></SheetFooter>
    </SheetContent>
  </Sheet>;
}

function CutoverPanel({ cutover, language, busy, error, onRun }: { cutover: AppData["meridian"]["cutover"]; language: Language; busy: string; error: string; onRun: (key: string, operation: () => Promise<unknown>, success: string) => Promise<void> }) {
  if (cutover.state === "not_required") return null;
  const endpointTotal = Math.max(1, cutover.expectedEndpoints);
  const routeTotal = Math.max(1, cutover.expectedRoutes);
  const verificationProgress = cutover.expectedRoutes > 0
    ? (cutover.readyEndpoints / endpointTotal + cutover.readyRoutes / routeTotal) / 2
    : cutover.readyEndpoints / endpointTotal;
  const progress = cutover.state === "complete" ? 100
    : cutover.state === "retire" ? 85 + 15 * cutover.retiredEndpoints / endpointTotal
    : cutover.state === "verify" ? 55 + 30 * verificationProgress
        : cutover.state === "project" ? 45
          : cutover.state === "publish" ? 38
            : cutover.state === "import" ? 30
            : cutover.state === "backup" ? 15
              : 5;
  const labels: Record<typeof cutover.state, [string, string]> = {
    inspect: ["等待开始", "Ready to start"],
    backup: ["正在创建恢复点", "Creating restore point"],
    import: ["正在导入账号与节点", "Importing accounts and entries"],
    publish: ["正在将订阅入口切换到 Center", "Moving the subscription entry to Center"],
    project: ["正在部署 Meridian", "Deploying Meridian"],
    verify: ["正在验证全部运行时", "Verifying every runtime"],
    retire: ["正在移除旧运行时", "Retiring legacy runtimes"],
    complete: ["替换完成", "Replacement complete"],
    failed: ["迁移已停止", "Migration stopped"],
  };
  const canStart = cutover.state === "inspect" || cutover.state === "failed" || Boolean(cutover.lastError) && (cutover.state === "publish" || cutover.state === "project" || cutover.state === "verify" || cutover.state === "retire");
  const start = () => onRun("cutover", () => api.startMeridianCutover(), cutover.state === "inspect" || cutover.state === "failed" ? copy(language, "安全替换流程已启动。", "The safe replacement workflow has started.") : copy(language, "失败节点已重新进入当前阶段。", "Failed nodes were requeued in the current phase."));
  return <div className="rounded-xl border p-4">
    <div className="flex items-start justify-between gap-3"><div><p className="font-medium">{copy(language, "3x-ui → Meridian 安全替换", "3x-ui → Meridian safe replacement")}</p><p className="mt-1 text-sm text-muted-foreground">{copy(language, ...labels[cutover.state])}</p></div>{cutover.complete ? <CheckCircle2Icon className="size-5 text-emerald-500" /> : <ArrowRightLeftIcon className="size-5 text-muted-foreground" />}</div>
    <Progress className="mt-4" value={Math.min(100, Math.round(progress))} />
    <dl className="mt-4 grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
      <div><dt className="text-muted-foreground">{copy(language, "订阅权威", "Subscription authority")}</dt><dd className="mt-1 font-medium">{cutover.subscriptionAuthority === "meridian" ? "Meridian" : "3x-ui"}</dd></div>
      <div><dt className="text-muted-foreground">{copy(language, "账号 / 凭据", "Accounts / credentials")}</dt><dd className="mt-1 tabular-nums">{cutover.importedAccounts} / {cutover.expectedAccounts} · {cutover.importedCredentials} / {cutover.expectedCredentials}</dd></div>
      <div><dt className="text-muted-foreground">{cutover.state === "retire" || cutover.state === "complete" ? copy(language, "旧运行时已移除", "Legacy runtimes retired") : copy(language, "可订阅入口已验证", "Subscription entries verified")}</dt><dd className="mt-1 tabular-nums">{cutover.state === "retire" || cutover.state === "complete" ? cutover.retiredEndpoints : cutover.readyEndpoints} / {cutover.expectedEndpoints}</dd></div>
      <div><dt className="text-muted-foreground">{copy(language, "落地路由已验证", "Egress routes verified")}</dt><dd className="mt-1 tabular-nums">{cutover.readyRoutes} / {cutover.expectedRoutes}{cutover.blockedRoutes > 0 ? <span className="ml-2 text-destructive">{copy(language, `${cutover.blockedRoutes} 个不可用`, `${cutover.blockedRoutes} blocked`)}</span> : null}</dd></div>
    </dl>
    <p className="mt-4 text-sm text-muted-foreground">{copy(language, "Center 会先接管并验证原订阅地址，再逐台替换运行时；切换期间以导入快照持续提供相同节点，旧安装仅在新运行时和公网入口都确认后移除。加密恢复点会保留。", "Center takes over and verifies the existing subscription URL before replacing runtimes one by one. The imported snapshot keeps the same entries available during cutover, and legacy installations are removed only after the new runtime and public entry are confirmed. The encrypted restore point is retained.")}</p>
    {error ? <div className="mt-3"><FieldError>{error}</FieldError></div> : null}
    {canStart ? <Button className="mt-4" disabled={busy === "cutover"} onClick={() => void start()}>{busy === "cutover" ? <Spinner data-icon="inline-start" /> : <ArrowRightLeftIcon data-icon="inline-start" />}{cutover.state === "inspect" ? copy(language, "开始安全替换", "Start safe replacement") : copy(language, "检查并继续", "Check and continue")}</Button> : null}
  </div>;
}

function EndpointPanel({ application, endpoint, publication, language, busy, error, onRun }: { application: Application | null; endpoint: AppData["meridian"]["endpoints"][number] | undefined; publication: AppData["publications"][number] | undefined; language: Language; busy: string; error: string; onRun: (key: string, operation: () => Promise<unknown>, success: string) => Promise<void> }) {
  const [targetHost, setTargetHost] = useState("www.microsoft.com");
  const [serverName, setServerName] = useState("www.microsoft.com");
  const [advertiseHost, setAdvertiseHost] = useState("");
  const [verification, setVerification] = useState<ApplicationCommand | null>(null);
  const [checking, setChecking] = useState(false);
  const [verificationError, setVerificationError] = useState("");
  const [entryName, setEntryName] = useState("");
  const [regionCode, setRegionCode] = useState("");
  const [recoveryConfirmed, setRecoveryConfirmed] = useState(false);
  const { execute } = useApplicationCommandExecutor(application?.id);
  useEffect(() => {
    setEntryName(endpoint ? regionBaseName(endpoint.displayName || endpoint.applicationName, endpoint.regionCode) : "");
    setRegionCode(endpoint?.regionCode ?? "");
    setRecoveryConfirmed(false);
  }, [endpoint?.id, endpoint?.displayName, endpoint?.regionCode, endpoint?.applicationName]);

  const checkTarget = async () => {
    if (!application || checking) return;
    setChecking(true);
    setVerification(null);
    setVerificationError("");
    try {
      const checked = await execute(() => api.verifyRealityTarget(application.id, targetHost.trim(), serverName.trim()), setVerification);
      if (!checked || checked.state !== "succeeded" || !checked.targetIp) {
        setVerificationError(copy(language, "此节点无法验证该 REALITY 目标，请更换后重试。", "This node could not verify the REALITY target. Choose another target and retry."));
      }
    } catch (caught) {
      setVerificationError(userError(language, caught));
    } finally {
      setChecking(false);
    }
  };
  if (endpoint) return <div className="flex flex-col gap-4">
    <div className="rounded-xl border p-4">
      <div className="flex items-center justify-between gap-3"><div><p className="font-medium">{endpoint.displayName || endpoint.applicationName}</p><p className="mt-1 text-sm text-muted-foreground">{endpoint.advertiseHost}:{endpoint.advertisePort}</p><div className="mt-2 flex flex-wrap gap-1.5">{endpoint.vless ? <Badge variant="secondary">VLESS / REALITY</Badge> : null}{endpoint.hy2 ? <Badge variant="secondary">Hysteria2 / TLS</Badge> : null}</div></div><StateBadge language={language} value={endpoint.status} /></div>
      <dl className="mt-4 grid gap-3 text-sm sm:grid-cols-2">
        <div><dt className="text-muted-foreground">{copy(language, "REALITY 目标", "REALITY target")}</dt><dd className="mt-1 font-mono">{endpoint.target}</dd></div>
        <div><dt className="text-muted-foreground">SNI</dt><dd className="mt-1 font-mono">{endpoint.serverNames.join(", ")}</dd></div>
        {endpoint.hy2 ? <div><dt className="text-muted-foreground">Hysteria2 SNI</dt><dd className="mt-1 font-mono">{endpoint.hy2ServerName}</dd></div> : null}
        <div><dt className="text-muted-foreground">{copy(language, "配置修订", "Configuration revision")}</dt><dd className="mt-1 tabular-nums">{endpoint.appliedRevision} / {endpoint.desiredRevision}</dd></div>
        <div><dt className="text-muted-foreground">{copy(language, "运行确认", "Runtime receipt")}</dt><dd className="mt-1">{endpoint.runtimeHealthy ? copy(language, "已验证", "Verified") : copy(language, "等待 Agent", "Waiting for Agent")}</dd></div>
      </dl>
      {endpoint.lastError ? <div className="mt-3"><TechnicalError error={endpoint.lastError} language={language} /></div> : null}
		{endpoint.status === "failed" ? <Alert className="mt-4" variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "入口配置需要显式恢复", "The entry needs explicit recovery")}</AlertTitle><AlertDescription><p>{copy(language, "Vastora 会保留失败证据，并以 Center 当前的账号、密钥和路由配置替换 Agent 的不确定 pending 状态。现有可用订阅入口不会因此下线。", "Vastora keeps the failure evidence and replaces the Agent's uncertain pending state with the current Center account, credential, and routing projection. Existing healthy subscription entries stay online.")}</p><Field className="mt-3" orientation="horizontal"><FieldLabel htmlFor={`meridian-recovery-${endpoint.id}`}><span>{copy(language, "我确认上一次执行已停止，并采用 Center 配置", "I confirm the previous execution stopped and select Center state")}</span><span className="text-xs font-normal text-muted-foreground">{copy(language, "不会从未知 Xray 配置反向导入账号或密钥", "Unknown Xray configuration is never imported back into accounts or credentials")}</span></FieldLabel><Switch checked={recoveryConfirmed} id={`meridian-recovery-${endpoint.id}`} onCheckedChange={setRecoveryConfirmed} /></Field><Button className="mt-3" disabled={busy === `endpoint-recovery-${endpoint.id}` || !recoveryConfirmed} onClick={() => void onRun(`endpoint-recovery-${endpoint.id}`, () => api.recoverMeridianEndpoint(endpoint.id), copy(language, "恢复任务已排队。", "The recovery task is queued."))} size="sm" type="button" variant="destructive">{busy === `endpoint-recovery-${endpoint.id}` ? <Spinner data-icon="inline-start" /> : null}{copy(language, "重建运行状态", "Rebuild runtime state")}</Button></AlertDescription></Alert> : null}
		<div className="mt-4 grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]"><Field><FieldLabel>{copy(language, "地区前缀", "Region prefix")}</FieldLabel><RegionCombobox id={`meridian-entry-region-${endpoint.id}`} language={language} onValueChange={setRegionCode} value={regionCode} /></Field><Field><FieldLabel htmlFor={`meridian-entry-name-${endpoint.id}`}>{copy(language, "节点名称", "Node name")}</FieldLabel><Input id={`meridian-entry-name-${endpoint.id}`} maxLength={48} onChange={(event) => setEntryName(event.target.value)} value={entryName} /></Field><Button className="self-end" disabled={busy === "endpoint-name" || !regionCode || !entryName.trim()} onClick={() => void onRun("endpoint-name", () => api.updateMeridianEndpointName(endpoint.id, { regionCode, name: entryName.trim() }), copy(language, "节点名称已更新。", "Node name updated."))} type="button" variant="outline">{busy === "endpoint-name" ? <Spinner data-icon="inline-start" /> : null}{copy(language, "保存名称", "Save name")}</Button></div>
    </div>
    {publication ? <Alert variant={publication.status === "failed" || publication.status === "degraded" ? "destructive" : "default"}><RadioTowerIcon /><AlertTitle><span className="mr-2">{copy(language, "公网入口", "Public entry")}</span><StateBadge language={language} value={publication.status} /></AlertTitle><AlertDescription><p className="break-all font-mono text-xs">{publication.hostname} · SNI {publication.sniHostname}</p>{publication.dnsRecord ? <p className="mt-2 break-all font-mono text-xs">{publication.dnsRecord.type} {publication.dnsRecord.name} → {publication.dnsRecord.value}</p> : null}{publication.lastError ? <div className="mt-2"><TechnicalError error={publication.lastError} language={language} /></div> : null}{publication.status !== "ready" ? <Button className="mt-3" disabled={busy === "endpoint-publication"} onClick={() => void onRun("endpoint-publication", () => api.verifyPublication(publication.id), copy(language, "公网入口检查已完成。", "The public entry check completed."))} size="sm" type="button" variant="outline">{busy === "endpoint-publication" ? <Spinner data-icon="inline-start" /> : null}{copy(language, "检查公网入口", "Check public entry")}</Button> : null}</AlertDescription></Alert> : null}
    <p className="text-sm text-muted-foreground">{copy(language, "HAProxy 保持公网 443 的唯一监听者；Xray 只在 Vastora 私有 Docker bridge 内监听 443。", "HAProxy remains the sole public listener on 443; Xray listens on 443 only inside Vastora's private Docker bridge.")}</p>
  </div>;
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!application) return;
    if (!verification || verification.state !== "succeeded" || !verification.targetIp) {
      setVerificationError(copy(language, "请先在当前节点完成 REALITY 目标检查。", "Verify the REALITY target from this node first."));
      return;
    }
    await onRun("endpoint", () => api.createMeridianEndpoint({ applicationId: application.id, verificationId: verification.id, targetIp: verification.targetIp!, advertiseHost: advertiseHost.trim() || undefined, targetHost: targetHost.trim(), serverName: serverName.trim(), regionCode, name: entryName.trim() }), copy(language, "Meridian 入口已创建，正在等待 Agent 应用。", "The Meridian entry was created and is waiting for the Agent."));
  };
  return <form onSubmit={(event) => void submit(event)}><FieldGroup>
      <Alert><RadioTowerIcon /><AlertTitle>{copy(language, "创建 Meridian 入口", "Create a Meridian entry")}</AlertTitle><AlertDescription>{copy(language, "先创建并发布 VLESS / REALITY 的 TCP 443 基础入口；公网域名就绪后，可添加原生 Hysteria2。账号与流量共用，固定落地仅作用于 VLESS。", "Create and publish the VLESS / REALITY TCP 443 anchor first, then add native Hysteria2 after its public hostname is ready. Accounts and usage are shared; fixed egress applies to VLESS only.")}</AlertDescription></Alert>
    <div className="grid gap-4 sm:grid-cols-2"><Field><FieldLabel>{copy(language, "地区前缀", "Region prefix")}</FieldLabel><RegionCombobox id="meridian-new-entry-region" language={language} onValueChange={setRegionCode} value={regionCode} /></Field><Field><FieldLabel htmlFor="meridian-new-entry-name">{copy(language, "节点名称", "Node name")}</FieldLabel><Input id="meridian-new-entry-name" maxLength={48} onChange={(event) => setEntryName(event.target.value)} value={entryName} required /><FieldDescription>{copy(language, "订阅中显示为“国旗｜名称”，全部 Meridian 入口必须唯一。", "Subscriptions show “flag｜name”; every Meridian entry name must be unique.")}</FieldDescription></Field></div>
    <Field><FieldLabel htmlFor="meridian-target">{copy(language, "REALITY 目标", "REALITY target")}</FieldLabel><Input id="meridian-target" value={targetHost} onChange={(event) => { setTargetHost(event.target.value); setVerification(null); }} required /><FieldDescription>{copy(language, "必须使用可稳定访问的 .com TLS 站点；端口固定为 443。", "Use a stable reachable .com TLS site; port 443 is fixed.")}</FieldDescription></Field>
    <Field><FieldLabel htmlFor="meridian-sni">SNI</FieldLabel><Input id="meridian-sni" value={serverName} onChange={(event) => { setServerName(event.target.value); setVerification(null); }} required /></Field>
    <div className="flex flex-wrap items-center gap-3"><Button disabled={checking || !targetHost.trim() || !serverName.trim()} onClick={() => void checkTarget()} type="button" variant="outline">{checking ? <Spinner data-icon="inline-start" /> : <ShieldCheckIcon data-icon="inline-start" />}{copy(language, "从当前节点检查", "Check from this node")}</Button>{verification?.state === "succeeded" && verification.targetIp ? <span className="text-sm text-emerald-600 dark:text-emerald-400">{targetHost} → {verification.targetIp}</span> : null}</div>
    {verificationError ? <FieldError>{verificationError}</FieldError> : null}
    <Field><FieldLabel htmlFor="meridian-public-host">{copy(language, "公网域名（可选）", "Public hostname (optional)")}</FieldLabel><Input id="meridian-public-host" value={advertiseHost} onChange={(event) => setAdvertiseHost(event.target.value)} placeholder={copy(language, "留空自动分配站点域名", "Leave blank to allocate a Site hostname")} /><FieldDescription>{copy(language, "Vastora 会生成需要指向当前节点公网地址的 DNS A 记录说明；公网监听验证通过前不会写入订阅。", "Vastora provides the DNS A record that must point to this node's public address. The entry stays out of subscriptions until public-listener verification succeeds.")}</FieldDescription></Field>
    {error ? <FieldError>{error}</FieldError> : null}
    <Button disabled={busy === "endpoint" || checking || verification?.state !== "succeeded" || !verification?.targetIp || !regionCode || !entryName.trim()} type="submit">{busy === "endpoint" ? <Spinner data-icon="inline-start" /> : <PlusIcon data-icon="inline-start" />}{copy(language, "创建入口", "Create entry")}</Button>
  </FieldGroup></form>;
}

function AccountsPanel({ created, data, language, busy, error, onCreated, onRun }: { created: MeridianAccountCreated | null; data: AppData; language: Language; busy: string; error: string; onCreated: (value: MeridianAccountCreated | null) => void; onRun: (key: string, operation: () => Promise<unknown>, success: string) => Promise<void> }) {
  const [name, setName] = useState("");
  const [quota, setQuota] = useState("0");
  const [resetDays, setResetDays] = useState("0");
  const [expiry, setExpiry] = useState("");
  const [enabled, setEnabled] = useState(true);
  const meridianApplicationIDs = useMemo(() => new Set(data.applications.filter((application) => application.appKey === "vastora-official/meridian").map((application) => application.id)), [data.applications]);
  const subscriptionService = data.services.find((service) => meridianApplicationIDs.has(service.applicationId) && service.name === "subscription" && service.status !== "stopped");
  const subscriptionPublication = subscriptionService ? data.publications.find((publication) => publication.serviceId === subscriptionService.id && publication.status !== "stopped" && (publication.kind === "cloudflare_tunnel" || publication.kind === "public_direct")) : undefined;
  const subscriptionURL = created ? subscriptionPublication ? `https://${subscriptionPublication.hostname}${created.subscriptionPath}` : created.subscriptionPath : "";
  const create = async (event: FormEvent) => {
    event.preventDefault();
    let result: MeridianAccountCreated | null = null;
    await onRun("account", async () => { result = await api.createMeridianAccount({ displayName: name.trim(), totalBytes: Math.round(Math.max(0, Number(quota)) * gibibyte), expiryTime: expiry ? new Date(expiry).getTime() : 0, resetDays: Math.max(0, Number(resetDays)), enabled }); }, copy(language, "账号已创建；请立即保存订阅地址。", "Account created; save the subscription URL now."));
    if (result) { onCreated(result); setName(""); setExpiry(""); }
  };
  return <div className="flex flex-col gap-5">
    {created ? <Alert><KeyRoundIcon /><AlertTitle>{copy(language, subscriptionPublication ? "仅显示一次的订阅地址" : "仅显示一次的订阅令牌路径", subscriptionPublication ? "One-time subscription URL" : "One-time subscription token path")}</AlertTitle><AlertDescription><div className="mt-2 flex items-center gap-2 rounded-lg bg-muted p-3 font-mono text-xs"><span className="min-w-0 flex-1 break-all">{subscriptionURL}</span><CopyButton language={language} value={subscriptionURL} /></div>{!subscriptionPublication ? <p className="mt-2 text-xs text-muted-foreground">{copy(language, "请先保存此路径；开启公网订阅后，将它追加到订阅域名。Center 私网管理地址不能代替公网订阅域名。", "Save this path first. After enabling the public subscription, append it to the subscription hostname. The private Center management origin is not a public subscription origin.")}</p> : null}<Button className="mt-3" size="sm" variant="outline" onClick={() => onCreated(null)}>{copy(language, "已保存", "Saved")}</Button></AlertDescription></Alert> : null}
    <form onSubmit={(event) => void create(event)}><FieldGroup>
      <Field><FieldLabel htmlFor="meridian-account-name">{copy(language, "账号名称", "Account name")}</FieldLabel><Input id="meridian-account-name" value={name} onChange={(event) => setName(event.target.value)} required /></Field>
      <div className="grid gap-4 sm:grid-cols-2"><Field><FieldLabel htmlFor="meridian-account-quota">{copy(language, "共享流量（GiB）", "Shared quota (GiB)")}</FieldLabel><Input id="meridian-account-quota" min="0" step="0.1" type="number" value={quota} onChange={(event) => setQuota(event.target.value)} /><FieldDescription>{copy(language, "0 表示不限；本机与全部落地共用。", "0 is unlimited; native and all egress routes share it.")}</FieldDescription></Field><Field><FieldLabel htmlFor="meridian-account-reset">{copy(language, "重置周期（天）", "Reset interval (days)")}</FieldLabel><Input id="meridian-account-reset" max="3650" min="0" type="number" value={resetDays} onChange={(event) => setResetDays(event.target.value)} /></Field><Field><FieldLabel htmlFor="meridian-account-expiry">{copy(language, "到期时间（可选）", "Expiry time (optional)")}</FieldLabel><Input id="meridian-account-expiry" min={localDateTimeValue(Date.now())} type="datetime-local" value={expiry} onChange={(event) => setExpiry(event.target.value)} /><FieldDescription>{copy(language, "留空表示永不过期。", "Leave empty for no expiry.")}</FieldDescription></Field></div>
      <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="meridian-account-enabled">{copy(language, "立即启用", "Enable now")}</FieldLabel><FieldDescription>{copy(language, "应用完成前订阅不会提前发布。", "The subscription is not published before all runtime receipts complete.")}</FieldDescription></div><Switch id="meridian-account-enabled" checked={enabled} onCheckedChange={setEnabled} /></Field>
      {error ? <FieldError>{error}</FieldError> : null}<Button disabled={busy === "account" || !name.trim()} type="submit">{busy === "account" ? <Spinner data-icon="inline-start" /> : <PlusIcon data-icon="inline-start" />}{copy(language, "创建账号", "Create account")}</Button>
    </FieldGroup></form>
    <div className="flex flex-col gap-3">{data.meridian.accounts.map((account) => <AccountCard account={account} busy={busy} key={account.id} language={language} onRun={onRun} />)}</div>
  </div>;
}

function AccountCard({ account, busy, language, onRun }: { account: MeridianAccount; busy: string; language: Language; onRun: (key: string, operation: () => Promise<unknown>, success: string) => Promise<void> }) {
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(account.displayName);
  const [quota, setQuota] = useState(String(account.totalBytes / gibibyte));
  const [resetDays, setResetDays] = useState(String(account.resetDays));
  const [expiry, setExpiry] = useState(account.expiryTime ? localDateTimeValue(account.expiryTime) : "");
  const operation = `edit-${account.id}`;
  const save = async (event: FormEvent) => {
    event.preventDefault();
    await onRun(operation, () => api.updateMeridianAccount(account.id, {
      displayName: name.trim(),
      totalBytes: Math.round(Math.max(0, Number(quota)) * gibibyte),
      expiryTime: expiry ? new Date(expiry).getTime() : 0,
      resetDays: Math.max(0, Number(resetDays)),
      enabled: account.enabled,
    }), copy(language, "账号套餐已更新。", "Account plan updated."));
    setEditing(false);
  };
  return <div className="rounded-xl border p-4">
    <div className="flex items-start justify-between gap-3"><div><p className="font-medium">{account.displayName}</p><p className="mt-1 text-xs text-muted-foreground">{formatBytes(account.usedBytes)} / {account.totalBytes ? formatBytes(account.totalBytes) : copy(language, "不限", "Unlimited")} · {account.routeCount} {copy(language, "个落地", "egress route(s)")}</p>{account.expiryTime ? <p className="mt-1 text-xs text-muted-foreground">{copy(language, "到期", "Expires")} · {new Date(account.expiryTime).toLocaleString()}</p> : null}</div><StateBadge language={language} value={account.status} /></div>
    {account.lastError ? <div className="mt-3"><TechnicalError error={account.lastError} language={language} /></div> : null}
    {editing ? <form className="mt-4" onSubmit={(event) => void save(event)}><FieldGroup>
      <Field><FieldLabel htmlFor={`meridian-account-name-${account.id}`}>{copy(language, "账号名称", "Account name")}</FieldLabel><Input id={`meridian-account-name-${account.id}`} onChange={(event) => setName(event.target.value)} required value={name} /></Field>
      <div className="grid gap-4 sm:grid-cols-3"><Field><FieldLabel htmlFor={`meridian-account-quota-${account.id}`}>{copy(language, "共享流量（GiB）", "Shared quota (GiB)")}</FieldLabel><Input id={`meridian-account-quota-${account.id}`} min="0" onChange={(event) => setQuota(event.target.value)} step="0.1" type="number" value={quota} /></Field><Field><FieldLabel htmlFor={`meridian-account-reset-${account.id}`}>{copy(language, "重置周期（天）", "Reset interval (days)")}</FieldLabel><Input id={`meridian-account-reset-${account.id}`} max="3650" min="0" onChange={(event) => setResetDays(event.target.value)} type="number" value={resetDays} /></Field><Field><FieldLabel htmlFor={`meridian-account-expiry-${account.id}`}>{copy(language, "到期时间", "Expiry time")}</FieldLabel><Input id={`meridian-account-expiry-${account.id}`} min={localDateTimeValue(Date.now())} onChange={(event) => setExpiry(event.target.value)} type="datetime-local" value={expiry} /></Field></div>
      <div className="flex flex-wrap gap-2"><Button disabled={busy === operation || !name.trim()} size="sm" type="submit">{busy === operation ? <Spinner data-icon="inline-start" /> : null}{copy(language, "保存套餐", "Save plan")}</Button><Button disabled={busy === operation} onClick={() => setEditing(false)} size="sm" type="button" variant="ghost">{copy(language, "取消", "Cancel")}</Button></div>
    </FieldGroup></form> : <div className="mt-3 flex flex-wrap gap-2"><Button disabled={Boolean(busy)} onClick={() => setEditing(true)} size="sm" variant="outline"><PencilIcon data-icon="inline-start" />{copy(language, "编辑套餐", "Edit plan")}</Button><Button disabled={Boolean(busy)} size="sm" variant="outline" onClick={() => void onRun(`toggle-${account.id}`, () => api.updateMeridianAccount(account.id, { displayName: account.displayName, totalBytes: account.totalBytes, expiryTime: account.expiryTime, resetDays: account.resetDays, enabled: !account.enabled }), account.enabled ? copy(language, "账号已停用。", "Account disabled.") : copy(language, "账号已启用。", "Account enabled."))}>{busy === `toggle-${account.id}` ? <Spinner data-icon="inline-start" /> : null}{account.enabled ? copy(language, "停用", "Disable") : copy(language, "启用", "Enable")}</Button></div>}
  </div>;
}

function RoutesPanel({ data, endpointId, language, busy, error, onRun }: { data: AppData; endpointId: string; language: Language; busy: string; error: string; onRun: (key: string, operation: () => Promise<unknown>, success: string) => Promise<void> }) {
  const [accountId, setAccountId] = useState(data.meridian.accounts.find((account) => account.enabled)?.id ?? "");
  const [egressNodeId, setEgressNodeId] = useState("");
  const [hideNative, setHideNative] = useState(false);
  const [readyEgress, setReadyEgress] = useState<Set<string> | null>(null);
  const [qualityChecks, setQualityChecks] = useState<IPQualityCheck[]>([]);
  useEffect(() => {
    const request = new AbortController();
    setQualityChecks([]);
    void api.ipQuality(request.signal).then((value) => { if (!request.signal.aborted) setQualityChecks(value.checks); }).catch(() => {});
    return () => request.abort();
  }, [endpointId]);
  useEffect(() => {
    let cancelled = false;
    setReadyEgress(null);
    void api.landing().then((view) => {
      if (!cancelled) setReadyEgress(new Set(view.servers.filter((server) => server.status === "ready").map((server) => server.nodeId)));
    }).catch(() => { if (!cancelled) setReadyEgress(new Set()); });
    return () => { cancelled = true; };
  }, [endpointId]);
  const existing = useMemo(() => new Set(data.meridian.grants.filter((grant) => grant.endpointId === endpointId && grant.accountId === accountId).map((grant) => grant.egressNodeId)), [accountId, data.meridian.grants, endpointId]);
  const candidates = data.agents.filter((agent) => readyEgress?.has(agent.id) && agent.id !== data.meridian.endpoints.find((value) => value.id === endpointId)?.nodeId && !existing.has(agent.id));
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    await onRun("route", () => api.createMeridianRoute({ accountId, endpointId, egressNodeId, hideNative }), copy(language, "落地授权已创建，正在等待 Agent 应用。", "The egress grant was created and is waiting for the Agent."));
    setEgressNodeId("");
  };
  return <div className="flex flex-col gap-5">
    <IPQualityComparison language={language} nodeId={data.meridian.endpoints.find((value) => value.id === endpointId)?.nodeId} nodes={data.meridian.endpoints.map((value) => ({ id: value.nodeId, name: value.displayName || value.nodeId }))} allowedNodeIds={candidates.map((agent) => agent.id)} onSelect={setEgressNodeId} />
    <form onSubmit={(event) => void submit(event)}><FieldGroup>
      <Alert><RouteIcon /><AlertTitle>{copy(language, "VLESS 固定落地，不复制套餐", "Fixed VLESS egress without duplicating quota")}</AlertTitle><AlertDescription>{copy(language, "每条 VLESS 落地使用独立 UUID，但流量计入同一个账号；Hysteria2 保持本机出口。缺少私网 SOCKS 回执时配置会停止，不会回退本机出口。", "Each VLESS route has its own UUID but contributes to the same account usage; Hysteria2 remains native. A missing private SOCKS receipt blocks deployment and never falls back to the entry's native exit.")}</AlertDescription></Alert>
      <Field><FieldLabel>{copy(language, "账号", "Account")}</FieldLabel><SelectControl value={accountId} onValueChange={setAccountId} options={[{ value: "", label: copy(language, "没有可用账号", "No available account"), disabled: true }, ...data.meridian.accounts.map((account) => ({ value: account.id, label: account.displayName, disabled: !account.enabled }))]} /></Field>
      <Field><FieldLabel>{copy(language, "落地节点", "Egress node")}</FieldLabel><SelectControl value={egressNodeId} onValueChange={setEgressNodeId} options={[{ value: "", label: readyEgress === null ? copy(language, "正在读取可用落地…", "Loading ready egress nodes…") : copy(language, "选择已启用落地服务的节点", "Select a node with a ready egress service"), disabled: true }, ...candidates.map((agent) => ({ value: agent.id, label: `${agent.name} · ${assessmentLabel(language, qualityChecks.find((check) => check.agentId === agent.id)?.assessment)}`, disabled: !agent.connected }))]} /><FieldDescription>{copy(language, "仅列出已完成私网 SOCKS 回执的落地节点；普通计算节点不会出现在这里。", "Only nodes with a verified private SOCKS receipt are listed; general compute nodes are excluded.")}</FieldDescription></Field>
      <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel>{copy(language, "隐藏本机 VLESS", "Hide native VLESS")}</FieldLabel><FieldDescription>{copy(language, "订阅中隐藏本机 VLESS；原生 Hysteria2 不受影响。", "Hide native VLESS from the subscription; native Hysteria2 is unaffected.")}</FieldDescription></div><Switch checked={hideNative} onCheckedChange={setHideNative} /></Field>
      {error ? <FieldError>{error}</FieldError> : null}<Button disabled={busy === "route" || !endpointId || !accountId || !egressNodeId} type="submit">{busy === "route" ? <Spinner data-icon="inline-start" /> : <PlusIcon data-icon="inline-start" />}{copy(language, "添加落地", "Add egress")}</Button>
    </FieldGroup></form>
    <div className="flex flex-col gap-3">{data.meridian.grants.filter((grant) => !endpointId || grant.endpointId === endpointId).map((grant) => <div className="flex items-center gap-3 rounded-xl border p-4" key={grant.id}><div className="min-w-0 flex-1"><p className="truncate font-medium">{grant.egressNodeName}</p><p className="mt-1 text-xs text-muted-foreground">{data.meridian.accounts.find((account) => account.id === grant.accountId)?.displayName ?? grant.accountId}</p>{grant.lastError ? <div className="mt-2"><TechnicalError error={grant.lastError} language={language} /></div> : null}</div><StateBadge language={language} value={grant.status} /><Button aria-label={copy(language, "移除落地", "Remove egress")} disabled={busy === `revoke-${grant.id}`} size="icon-sm" variant="ghost" onClick={() => void onRun(`revoke-${grant.id}`, () => api.revokeMeridianRoute(grant.id), copy(language, "落地移除已进入安全收敛。", "Egress removal is converging safely."))}>{busy === `revoke-${grant.id}` ? <Spinner /> : <Trash2Icon />}</Button></div>)}</div>
  </div>;
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  if (value >= gibibyte) return `${(value / gibibyte).toFixed(value >= 10 * gibibyte ? 0 : 1)} GiB`;
  if (value >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(1)} MiB`;
  return `${Math.round(value / 1024)} KiB`;
}

function localDateTimeValue(value: number) {
  const date = new Date(value);
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}
