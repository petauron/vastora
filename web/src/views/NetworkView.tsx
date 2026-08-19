import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { CableIcon, CloudIcon, CopyIcon, Globe2Icon, KeyRoundIcon, NetworkIcon, RouterIcon, ServerIcon } from "lucide-react";
import { api } from "../api";
import type { DashboardData, Mutate } from "../App";
import type { AgentView, HeadscaleJoin, Integration, NetworkKind, NetworkProfile } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, copy, formatDate } from "./shared";
import { CloudflareOAuthConnect } from "./CloudflareOAuthConnect";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

type IntegrationEditor = "headscale" | "cloudflare" | null;

export function NetworkView({ data, language, mutate }: { data: DashboardData; language: Language; mutate: Mutate }) {
  const [editor, setEditor] = useState<IntegrationEditor>(null);
  const [profileAgent, setProfileAgent] = useState<AgentView | null>(null);
  const [join, setJoin] = useState<HeadscaleJoin | null>(null);
  const [joinBusy, setJoinBusy] = useState("");
  const headscale = integration(data.integrations, "headscale");
  const cloudflare = integration(data.integrations, "cloudflare");
  const activeAgents = data.agents.filter((agent) => agent.status === "active");
  const enabledCount = (kind: NetworkKind) => activeAgents.filter((agent) => agent.networkProfile?.enabledKinds.includes(kind)).length;

  const createJoin = async (agent: AgentView) => {
    setJoinBusy(agent.id);
    try { setJoin(await api.createHeadscaleJoin(agent.id)); } finally { setJoinBusy(""); }
  };

  return (
    <section className="flex flex-col gap-7">
      <PageHeading title={copy(language, "网络", "Network")} description={copy(language, "一个节点可以同时使用局域网、Headscale 和公网。应用安装后，再为服务选择一个或多个访问入口。", "A node can use LAN, Headscale, and public networking together. Add one or more access points after an app is installed.")} />
      <div className="grid gap-4 lg:grid-cols-3">
        <CapabilityCard icon={<CableIcon />} title={copy(language, "局域网", "Local network")} description={copy(language, "适合家中或办公室内直接访问。", "Direct access at home or in the office.")} count={enabledCount("lan")} language={language} />
        <Card>
          <CardHeader><CardTitle className="flex items-center gap-2"><NetworkIcon />Headscale</CardTitle><CardDescription>{copy(language, "把不同网络中的设备安全地放进同一个私网。", "Securely connects devices across different networks.")}</CardDescription><CardAction><StateBadge value={headscale.status} /></CardAction></CardHeader>
          <CardContent><p className="text-sm text-muted-foreground">{headscale.status === "configured" ? `${headscale.mode === "builtin" ? copy(language, "内置", "Built-in") : copy(language, "外部", "External")} · ${headscale.endpoint}` : copy(language, "尚未配置控制面。", "No control plane configured.")}</p></CardContent>
          <CardFooter className="justify-between"><span className="text-xs text-muted-foreground">{enabledCount("headscale")} {copy(language, "个节点已连接", "node(s) connected")}</span><Button onClick={() => setEditor("headscale")} size="sm" variant="outline">{headscale.status === "configured" ? copy(language, "修改", "Edit") : copy(language, "设置", "Set up")}</Button></CardFooter>
        </Card>
        <Card>
          <CardHeader><CardTitle className="flex items-center gap-2"><Globe2Icon />{copy(language, "公网与 Cloudflare", "Public & Cloudflare")}</CardTitle><CardDescription>{copy(language, "公网直连用于真实公网节点；Tunnel 用于 Web 服务。", "Direct public access is for public nodes; Tunnel is for Web services.")}</CardDescription><CardAction><StateBadge value={cloudflare.status} /></CardAction></CardHeader>
          <CardContent><p className="text-sm text-muted-foreground">{cloudflare.status === "configured" ? copy(language, `已连接域名 ${cloudflare.endpoint}`, `Connected zone ${cloudflare.endpoint}`) : copy(language, "Cloudflare 是可选集成。", "Cloudflare is optional.")}</p></CardContent>
          <CardFooter className="justify-between"><span className="text-xs text-muted-foreground">{enabledCount("public")} {copy(language, "个公网节点", "public node(s)")}</span><Button onClick={() => setEditor("cloudflare")} size="sm" variant="outline">{cloudflare.status === "configured" ? copy(language, "修改", "Edit") : copy(language, "设置", "Set up")}</Button></CardFooter>
        </Card>
      </div>

      {join ? <Alert><KeyRoundIcon /><AlertTitle>{copy(language, "一次性加入命令", "One-time join command")}</AlertTitle><AlertDescription><p>{copy(language, `请在 ${formatDate(language, join.expiresAt)} 前仅在目标节点运行一次。`, `Run this once on the target node before ${formatDate(language, join.expiresAt)}.`)}</p><div className="mt-3 flex items-start gap-2"><code className="min-w-0 flex-1 break-all rounded-lg bg-muted p-3 text-xs">{join.command}</code><Button aria-label={copy(language, "复制命令", "Copy command")} onClick={() => void navigator.clipboard.writeText(join.command)} size="icon" variant="outline"><CopyIcon /></Button></div></AlertDescription></Alert> : null}

      <div className="flex flex-col gap-4">
        <div><h2 className="text-lg font-semibold">{copy(language, "节点网络", "Node networks")}</h2><p className="mt-1 text-sm text-muted-foreground">{copy(language, "Agent 自动发现地址，你只需要确认建议配置。", "Agents discover addresses automatically; you only confirm the suggestion.")}</p></div>
        <div className="grid gap-4 lg:grid-cols-2">
          {activeAgents.map((agent) => <NodeNetworkCard agent={agent} headscaleReady={headscale.status === "configured"} joinBusy={joinBusy === agent.id} key={agent.id} language={language} onConfigure={() => setProfileAgent(agent)} onJoin={() => void createJoin(agent)} />)}
        </div>
      </div>

      <NetworkProfileSheet agent={profileAgent} language={language} onClose={() => setProfileAgent(null)} onSave={async (agent, profile) => { await mutate(() => api.confirmNetworkProfile(agent.id, profile), copy(language, "节点网络已确认。", "Node network confirmed.")); setProfileAgent(null); }} />
      <HeadscaleSheet integration={headscale} language={language} open={editor === "headscale"} onClose={() => setEditor(null)} onSave={async (input) => { await mutate(() => api.configureHeadscale(input), copy(language, "Headscale 已连接。", "Headscale connected.")); setEditor(null); }} />
      <CloudflareSheet integration={cloudflare} language={language} open={editor === "cloudflare"} onClose={() => setEditor(null)} onConnected={async () => { await mutate(async () => undefined, copy(language, "Cloudflare 已连接。", "Cloudflare connected.")); setEditor(null); }} />
    </section>
  );
}

function integration(values: Integration[], kind: Integration["kind"]): Integration {
  return values.find((value) => value.kind === kind) ?? { kind, secretSet: false, status: "disabled" };
}

function CapabilityCard({ icon, title, description, count, language }: { icon: ReactNode; title: string; description: string; count: number; language: Language }) {
  return <Card><CardHeader><CardTitle className="flex items-center gap-2">{icon}{title}</CardTitle><CardDescription>{description}</CardDescription><CardAction><StateBadge value={count ? "configured" : "disabled"} /></CardAction></CardHeader><CardContent><p className="text-sm text-muted-foreground">{count} {copy(language, "个节点", "node(s)")}</p></CardContent><CardFooter><span className="text-xs text-muted-foreground">{copy(language, "受保护网络默认使用 HTTP。", "Protected networks use HTTP by default.")}</span></CardFooter></Card>;
}

function NodeNetworkCard({ agent, headscaleReady, joinBusy, language, onConfigure, onJoin }: { agent: AgentView; headscaleReady: boolean; joinBusy: boolean; language: Language; onConfigure: () => void; onJoin: () => void }) {
  const profile = agent.networkProfile;
  return <Card><CardHeader><CardTitle className="flex items-center gap-2"><ServerIcon />{agent.name}</CardTitle><CardDescription>{agent.connected ? copy(language, "Agent 在线", "Agent online") : copy(language, "Agent 离线", "Agent offline")}</CardDescription><CardAction><StateBadge value={profile ? "configured" : "pending"} /></CardAction></CardHeader><CardContent className="flex flex-col gap-3"><div className="flex flex-wrap gap-2">{profile?.enabledKinds.map((kind) => <Badge key={kind} variant="outline">{kind}</Badge>)}{agent.capabilities.gateway ? <Badge variant="secondary"><RouterIcon data-icon="inline-start" />Gateway</Badge> : null}{agent.capabilities.tunnel ? <Badge variant="secondary"><CloudIcon data-icon="inline-start" />Tunnel</Badge> : null}</div><dl className="grid grid-cols-2 gap-3 text-sm"><div><dt className="text-muted-foreground">{copy(language, "私有服务地址", "Private service address")}</dt><dd className="mt-1 font-mono text-xs">{profile?.serviceAddress || "—"}</dd></div><div><dt className="text-muted-foreground">{copy(language, "发现地址", "Discovered addresses")}</dt><dd className="mt-1">{agent.networkCandidates.length}</dd></div></dl></CardContent><CardFooter className="gap-2"><Button className="flex-1" onClick={onConfigure} size="sm" variant="outline">{profile ? copy(language, "修改", "Edit") : copy(language, "确认网络", "Confirm network")}</Button>{headscaleReady && !profile?.enabledKinds.includes("headscale") ? <Button disabled={joinBusy} onClick={onJoin} size="sm">{joinBusy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "加入私网", "Join private network")}</Button> : null}</CardFooter></Card>;
}

function suggestedProfile(agent: AgentView): NetworkProfile {
  if (agent.networkProfile) return agent.networkProfile;
  const lan = agent.networkCandidates.find((candidate) => candidate.kind === "lan")?.address ?? "";
  const headscale = agent.networkCandidates.find((candidate) => candidate.kind === "headscale")?.address ?? "";
  return { serviceAddress: headscale || lan || "127.0.0.1", lanAddress: lan, headscaleAddress: headscale, publicAddress: "", enabledKinds: [lan ? "lan" : null, headscale ? "headscale" : null].filter(Boolean) as NetworkKind[], directPublic: false };
}

function NetworkProfileSheet({ agent, language, onClose, onSave }: { agent: AgentView | null; language: Language; onClose: () => void; onSave: (agent: AgentView, profile: NetworkProfile) => Promise<void> }) {
  const [profile, setProfile] = useState<NetworkProfile>({ serviceAddress: "127.0.0.1", enabledKinds: [], directPublic: false });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { if (agent) { setProfile(suggestedProfile(agent)); setError(""); } }, [agent]);
  const grouped = useMemo(() => ({ lan: agent?.networkCandidates.filter((candidate) => candidate.kind === "lan") ?? [], headscale: agent?.networkCandidates.filter((candidate) => candidate.kind === "headscale") ?? [], public: agent?.networkCandidates.filter((candidate) => candidate.kind === "public") ?? [] }), [agent]);
  const toggle = (kind: NetworkKind, enabled: boolean) => setProfile((current) => {
    const kinds = enabled ? [...new Set([...current.enabledKinds, kind])] : current.enabledKinds.filter((value) => value !== kind);
    return { ...current, enabledKinds: kinds, lanAddress: kind === "lan" && !enabled ? "" : current.lanAddress, headscaleAddress: kind === "headscale" && !enabled ? "" : current.headscaleAddress, publicAddress: kind === "public" && !enabled ? "" : current.publicAddress, directPublic: kind === "public" && !enabled ? false : current.directPublic };
  });
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); if (!agent) return; setBusy(true); setError("");
    try { await onSave(agent, profile); } catch (submitError) { setError(submitError instanceof Error ? submitError.message : "Request failed"); } finally { setBusy(false); }
  };
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(agent)}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, `配置 ${agent?.name ?? ""} 的网络`, `Configure ${agent?.name ?? ""} network`)}</SheetTitle><SheetDescription>{copy(language, "只选择实际可以从其他设备访问的地址。公网地址必须真实配置在本机网卡上。", "Only select addresses reachable by other devices. A public address must be assigned to a local interface.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="service-address">{copy(language, "应用私有地址", "Private service address")}</FieldLabel><NativeSelect id="service-address" onChange={(event) => setProfile((current) => ({ ...current, serviceAddress: event.target.value }))} value={profile.serviceAddress}><option value="127.0.0.1">127.0.0.1 — {copy(language, "仅本机", "this node only")}</option>{agent?.networkCandidates.filter((candidate) => candidate.kind !== "public").map((candidate) => <option key={candidate.address} value={candidate.address}>{candidate.address} — {candidate.interface}</option>)}</NativeSelect><FieldDescription>{copy(language, "安装应用后若要修改，需要先停止该节点上的应用。", "Stop apps on this node before changing it later.")}</FieldDescription></Field><NetworkKindField candidates={grouped.lan} checked={profile.enabledKinds.includes("lan")} kind="lan" language={language} selected={profile.lanAddress ?? ""} onSelected={(value) => setProfile((current) => ({ ...current, lanAddress: value }))} onToggle={(checked) => toggle("lan", checked)} /><NetworkKindField candidates={grouped.headscale} checked={profile.enabledKinds.includes("headscale")} kind="headscale" language={language} selected={profile.headscaleAddress ?? ""} onSelected={(value) => setProfile((current) => ({ ...current, headscaleAddress: value }))} onToggle={(checked) => toggle("headscale", checked)} /><FieldSet><FieldLegend>{copy(language, "公网直连", "Direct public ingress")}</FieldLegend><FieldDescription>{copy(language, "只在节点拥有真实本机公网地址，并允许入站端口时启用。NAT 出口 IP 不算。", "Enable only for a real local public address that accepts inbound ports. A NAT egress address does not qualify.")}</FieldDescription><Field orientation="horizontal" data-disabled={grouped.public.length === 0}><FieldLabel htmlFor="direct-public">{copy(language, "允许直接公网发布", "Allow direct public publication")}</FieldLabel><Switch checked={profile.directPublic} disabled={grouped.public.length === 0} id="direct-public" onCheckedChange={(checked) => { toggle("public", checked); setProfile((current) => ({ ...current, directPublic: checked, publicAddress: checked ? grouped.public[0]?.address ?? "" : "" })); }} /></Field>{profile.directPublic ? <Field><FieldLabel htmlFor="public-address">{copy(language, "公网地址", "Public address")}</FieldLabel><NativeSelect id="public-address" onChange={(event) => setProfile((current) => ({ ...current, publicAddress: event.target.value }))} value={profile.publicAddress}>{grouped.public.map((candidate) => <option key={candidate.address} value={candidate.address}>{candidate.address} — {candidate.interface}</option>)}</NativeSelect></Field> : null}</FieldSet>{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "确认配置", "Confirm configuration")}</Button></SheetFooter></form></SheetContent></Sheet>;
}

function NetworkKindField({ candidates, checked, kind, language, selected, onSelected, onToggle }: { candidates: AgentView["networkCandidates"]; checked: boolean; kind: "lan" | "headscale"; language: Language; selected: string; onSelected: (value: string) => void; onToggle: (value: boolean) => void }) {
  const title = kind === "lan" ? copy(language, "局域网", "Local network") : "Headscale";
  return <FieldSet data-disabled={candidates.length === 0}><FieldLegend>{title}</FieldLegend><Field orientation="horizontal"><FieldLabel htmlFor={`network-${kind}`}>{copy(language, "启用此网络", "Enable this network")}</FieldLabel><Switch checked={checked} disabled={candidates.length === 0} id={`network-${kind}`} onCheckedChange={(value) => { onToggle(value); if (value && !selected) onSelected(candidates[0]?.address ?? ""); }} /></Field>{checked ? <Field><FieldLabel htmlFor={`address-${kind}`}>{copy(language, "使用地址", "Use address")}</FieldLabel><NativeSelect id={`address-${kind}`} onChange={(event) => onSelected(event.target.value)} value={selected}>{candidates.map((candidate) => <option key={candidate.address} value={candidate.address}>{candidate.address} — {candidate.interface}</option>)}</NativeSelect></Field> : null}</FieldSet>;
}

function HeadscaleSheet({ integration, language, open, onClose, onSave }: { integration: Integration; language: Language; open: boolean; onClose: () => void; onSave: (input: { mode: "builtin" | "external"; url: string; apiKey?: string }) => Promise<void> }) {
  const [mode, setMode] = useState<"builtin" | "external">(integration.mode === "external" ? "external" : "builtin");
  const [url, setURL] = useState(integration.endpoint?.startsWith("https://") ? integration.endpoint : "");
  const [apiKey, setAPIKey] = useState("");
  const [urlTouched, setURLTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    setMode(integration.mode === "external" ? "external" : "builtin");
    setURL(integration.endpoint?.startsWith("https://") ? integration.endpoint : "");
    setAPIKey("");
    setURLTouched(false);
    setError("");
  }, [open, integration.mode, integration.endpoint]);
  const urlInvalid = urlTouched && !validHTTPSURL(url);
  const keyInvalid = mode === "external" && apiKey !== "" && apiKey.length < 20;
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); setURLTouched(true); setError("");
    if (!validHTTPSURL(url)) return;
    setBusy(true);
    try { await onSave({ mode, url, ...(mode === "external" ? { apiKey } : {}) }); } catch (submitError) { setError(submitError instanceof Error ? submitError.message : "Request failed"); } finally { setBusy(false); }
  };
  const externalKeyRequired = mode === "external" && !integration.secretSet;
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, "设置 Headscale", "Set up Headscale")}</SheetTitle><SheetDescription>{copy(language, "内置模式由 Vastora 自动安装和维护；外部模式只验证并保存连接信息。", "Vastora installs and maintains bundled mode automatically; external mode only verifies and stores connection details.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="headscale-mode">1. {copy(language, "选择类型", "Choose type")}</FieldLabel><NativeSelect id="headscale-mode" onChange={(event) => setMode(event.target.value as "builtin" | "external")} value={mode}><option value="builtin">{copy(language, "自动安装到 Center 服务器", "Install on the Center server")}</option><option value="external">{copy(language, "连接已有 Headscale", "Connect an existing Headscale")}</option></NativeSelect></Field>{mode === "builtin" ? <Alert><ServerIcon /><AlertTitle>{copy(language, "无需命令和 API Key", "No command or API key needed")}</AlertTitle><AlertDescription>{copy(language, "确保域名已解析到 Center 服务器并开放 80、8443。Vastora 会安装固定版本、配置 HTTPS，并自动创建和加密保存 API Key。", "Point the hostname to the Center server and open ports 80 and 8443. Vastora installs fixed versions, configures HTTPS, and creates and encrypts the API key automatically.")}</AlertDescription></Alert> : null}<Field data-invalid={urlInvalid}><FieldLabel htmlFor="headscale-url">2. {copy(language, "控制面 HTTPS 地址", "HTTPS control-plane URL")}</FieldLabel><Input aria-invalid={urlInvalid} id="headscale-url" onBlur={() => setURLTouched(true)} onChange={(event) => setURL(event.target.value)} placeholder="https://headscale.example.com:8443" required type="url" value={url} />{urlInvalid ? <FieldError>{copy(language, "请输入不含账号、查询参数或片段的 HTTPS 地址。", "Enter an HTTPS URL without credentials, query parameters, or fragments.")}</FieldError> : <FieldDescription>{mode === "builtin" ? copy(language, "内置模式固定使用独立域名和 8443 端口。", "Bundled mode uses a separate hostname and port 8443.") : copy(language, "填写浏览器和节点都能访问的地址。", "Use an address reachable by both browsers and nodes.")}</FieldDescription>}</Field>{mode === "external" ? <Field data-invalid={keyInvalid}><FieldLabel htmlFor="headscale-key">3. API Key</FieldLabel><Input aria-invalid={keyInvalid} autoComplete="new-password" id="headscale-key" minLength={integration.secretSet ? undefined : 20} onChange={(event) => setAPIKey(event.target.value.trim())} required={!integration.secretSet} type="password" value={apiKey} /><FieldDescription>{integration.secretSet ? copy(language, "已安全保存。留空会继续使用原 Key；输入新值才会替换。", "Already stored securely. Leave blank to keep it, or enter a new value to replace it.") : copy(language, "至少 20 个字符，只会加密保存。", "At least 20 characters; stored only in encrypted form.")}</FieldDescription>{keyInvalid ? <FieldError>{copy(language, "Key 至少需要 20 个字符。", "The key must contain at least 20 characters.")}</FieldError> : null}</Field> : null}{busy && mode === "builtin" ? <Alert><Spinner /><AlertTitle>{copy(language, "正在安装 Headscale", "Installing Headscale")}</AlertTitle><AlertDescription>{copy(language, "下载镜像、启动服务和申请 HTTPS 证书可能需要几分钟。", "Downloading images, starting services, and obtaining HTTPS certificates can take a few minutes.")}</AlertDescription></Alert> : null}{error ? <FieldError role="alert">{error} {copy(language, "请确认域名、端口和网络后重试。", "Confirm the hostname, ports, and network, then retry.")}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !url || urlInvalid || keyInvalid || externalKeyRequired && apiKey.length < 20} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{mode === "builtin" ? copy(language, "安装并连接", "Install and connect") : copy(language, "验证并保存", "Verify and save")}</Button></SheetFooter></form></SheetContent></Sheet>;
}

function CloudflareSheet({ integration, language, open, onClose, onConnected }: { integration: Integration; language: Language; open: boolean; onClose: () => void; onConnected: () => Promise<void> }) {
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, "连接 Cloudflare", "Connect Cloudflare")}</SheetTitle><SheetDescription>{copy(language, "登录后选择域名即可，无需复制 Account ID、Zone ID 或 API Token。", "Sign in and choose a zone. No Account ID, Zone ID, or API token copying is needed.")}</SheetDescription></SheetHeader><div className="flex-1 px-4"><CloudflareOAuthConnect available connected={integration.status === "configured" && integration.mode === "oauth"} language={language} onConnected={onConnected} zoneName={integration.endpoint} /></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "关闭", "Close")}</Button></SheetFooter></SheetContent></Sheet>;
}

function validHTTPSURL(value: string) {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" && parsed.username === "" && parsed.password === "" && parsed.search === "" && parsed.hash === "";
  } catch {
    return false;
  }
}
