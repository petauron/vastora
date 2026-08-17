import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { CableIcon, CloudIcon, CopyIcon, Globe2Icon, KeyRoundIcon, NetworkIcon, RouterIcon, ServerIcon } from "lucide-react";
import { api } from "../api";
import type { DashboardData, Mutate } from "../App";
import type { AgentView, HeadscaleJoin, Integration, NetworkKind, NetworkProfile } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, copy, formatDate } from "./shared";
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
  const enabledCount = (kind: NetworkKind) => data.agents.filter((agent) => agent.networkProfile?.enabledKinds.includes(kind)).length;

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
          {data.agents.map((agent) => <NodeNetworkCard agent={agent} headscaleReady={headscale.status === "configured"} joinBusy={joinBusy === agent.id} key={agent.id} language={language} onConfigure={() => setProfileAgent(agent)} onJoin={() => void createJoin(agent)} />)}
        </div>
      </div>

      <NetworkProfileSheet agent={profileAgent} language={language} onClose={() => setProfileAgent(null)} onSave={async (agent, profile) => { await mutate(() => api.confirmNetworkProfile(agent.id, profile), copy(language, "节点网络已确认。", "Node network confirmed.")); setProfileAgent(null); }} />
      <HeadscaleSheet integration={headscale} language={language} open={editor === "headscale"} onClose={() => setEditor(null)} onSave={async (input) => { await mutate(() => api.configureHeadscale(input), copy(language, "Headscale 已连接。", "Headscale connected.")); setEditor(null); }} />
      <CloudflareSheet integration={cloudflare} language={language} open={editor === "cloudflare"} onClose={() => setEditor(null)} onSave={async (input) => { await mutate(() => api.configureCloudflare(input), copy(language, "Cloudflare 已连接。", "Cloudflare connected.")); setEditor(null); }} />
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

function HeadscaleSheet({ integration, language, open, onClose, onSave }: { integration: Integration; language: Language; open: boolean; onClose: () => void; onSave: (input: { mode: "builtin" | "external"; url: string; apiKey: string }) => Promise<void> }) {
  const [mode, setMode] = useState<"builtin" | "external">(integration.mode === "external" ? "external" : "builtin");
  const [url, setURL] = useState(integration.endpoint?.startsWith("https://") ? integration.endpoint : "");
  const [apiKey, setAPIKey] = useState("");
  const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  useEffect(() => { if (open) { setMode(integration.mode === "external" ? "external" : "builtin"); setURL(integration.endpoint?.startsWith("https://") ? integration.endpoint : ""); setAPIKey(""); setError(""); } }, [open, integration]);
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); setBusy(true); setError(""); try { await onSave({ mode, url, apiKey }); } catch (submitError) { setError(submitError instanceof Error ? submitError.message : "Request failed"); } finally { setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, "连接 Headscale", "Connect Headscale")}</SheetTitle><SheetDescription>{copy(language, "内置和外部 Headscale 使用相同 API；Center 只保存加密后的 API Key。", "Built-in and external Headscale use the same API. Center stores only the encrypted API key.")}</SheetDescription></SheetHeader><form className="flex flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 px-4"><FieldGroup><Field><FieldLabel htmlFor="headscale-mode">{copy(language, "类型", "Type")}</FieldLabel><NativeSelect id="headscale-mode" onChange={(event) => setMode(event.target.value as "builtin" | "external")} value={mode}><option value="builtin">{copy(language, "Center 部署栈内置", "Built into Center stack")}</option><option value="external">{copy(language, "现有 Headscale", "Existing Headscale")}</option></NativeSelect></Field><Field><FieldLabel htmlFor="headscale-url">{copy(language, "控制面 HTTPS 地址", "HTTPS control-plane URL")}</FieldLabel><Input id="headscale-url" onChange={(event) => setURL(event.target.value)} placeholder="https://headscale.example.com:8443" required type="url" value={url} /></Field><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="headscale-key">API Key</FieldLabel><Input aria-invalid={Boolean(error)} autoComplete="new-password" id="headscale-key" onChange={(event) => setAPIKey(event.target.value)} required type="password" value={apiKey} /><FieldDescription>{integration.secretSet ? copy(language, "已保存一个 Key；输入新值会替换它。", "A key is already saved. Entering a new one replaces it.") : copy(language, "在 Headscale 容器中创建管理员 API Key。", "Create an administrator API key in the Headscale container.")}</FieldDescription>{error ? <FieldError>{error}</FieldError> : null}</Field></FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "验证并保存", "Verify and save")}</Button></SheetFooter></form></SheetContent></Sheet>;
}

function CloudflareSheet({ integration, language, open, onClose, onSave }: { integration: Integration; language: Language; open: boolean; onClose: () => void; onSave: (input: { accountId: string; zoneId: string; apiToken: string }) => Promise<void> }) {
  const [accountId, setAccountID] = useState(integration.accountId ?? ""); const [zoneId, setZoneID] = useState(integration.zoneId ?? ""); const [apiToken, setAPIToken] = useState(""); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  useEffect(() => { if (open) { setAccountID(integration.accountId ?? ""); setZoneID(integration.zoneId ?? ""); setAPIToken(""); setError(""); } }, [open, integration]);
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); setBusy(true); setError(""); try { await onSave({ accountId, zoneId, apiToken }); } catch (submitError) { setError(submitError instanceof Error ? submitError.message : "Request failed"); } finally { setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, "连接 Cloudflare", "Connect Cloudflare")}</SheetTitle><SheetDescription>{copy(language, "Token 需要目标 Zone 的 DNS 编辑权限，以及 Account 的 Tunnel 编辑权限。", "The token needs DNS edit permission for the Zone and Tunnel edit permission for the Account.")}</SheetDescription></SheetHeader><form className="flex flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 px-4"><FieldGroup><Field><FieldLabel htmlFor="cf-account">Account ID</FieldLabel><Input id="cf-account" onChange={(event) => setAccountID(event.target.value)} required value={accountId} /></Field><Field><FieldLabel htmlFor="cf-zone">Zone ID</FieldLabel><Input id="cf-zone" onChange={(event) => setZoneID(event.target.value)} required value={zoneId} /></Field><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="cf-token">API Token</FieldLabel><Input aria-invalid={Boolean(error)} autoComplete="new-password" id="cf-token" onChange={(event) => setAPIToken(event.target.value)} required type="password" value={apiToken} /><FieldDescription>{copy(language, "Token 会由 Center 加密，列表接口只显示是否已配置。", "Center encrypts the token; list APIs reveal only whether it is configured.")}</FieldDescription>{error ? <FieldError>{error}</FieldError> : null}</Field></FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "验证并保存", "Verify and save")}</Button></SheetFooter></form></SheetContent></Sheet>;
}
