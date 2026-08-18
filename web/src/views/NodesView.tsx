import { useEffect, useMemo, useState, type FormEvent } from "react";
import { CheckCircle2Icon, CopyIcon, MapPinIcon, NetworkIcon, PlusIcon, ServerIcon, Settings2Icon, TerminalIcon, Trash2Icon } from "lucide-react";
import { api } from "../api";
import type { DashboardData, Mutate, Screen } from "../App";
import type { AgentEnrollment, AgentView } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, copy, formatDate } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

export function NodesView({ data, language, mutate, onNavigate }: { data: DashboardData; language: Language; mutate: Mutate; onNavigate: (screen: Screen) => void }) {
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<AgentView | null>(null);
  const currentEditing = editing ? data.agents.find((agent) => agent.id === editing.id) ?? editing : null;
  return <section className="flex flex-col gap-7">
    <PageHeading title={copy(language, "节点", "Nodes")} description={copy(language, "节点是运行应用的设备。添加后，Center 会自动发现它的网络。", "Nodes are devices that run apps. Center discovers their networks after they join.")} action={<Button onClick={() => setAdding(true)}><PlusIcon data-icon="inline-start" />{copy(language, "添加节点", "Add node")}</Button>} />
    {data.agents.length === 0 ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><ServerIcon /></EmptyMedia><EmptyTitle>{copy(language, "添加第一台节点", "Add your first node")}</EmptyTitle><EmptyDescription>{copy(language, "准备一台安装了 Docker 的 Linux 设备，然后复制一条命令即可接入。", "Prepare a Linux device with Docker, then join it with one command.")}</EmptyDescription><Button className="mt-3" onClick={() => setAdding(true)}><PlusIcon data-icon="inline-start" />{copy(language, "开始添加", "Get started")}</Button></EmptyHeader></Empty> : <div className="grid gap-4 lg:grid-cols-2">{data.agents.map((agent) => <NodeCard agent={agent} data={data} key={agent.id} language={language} onConfigure={() => setEditing(agent)} onNetwork={() => onNavigate("network")} />)}</div>}
    <AddNodeSheet data={data} language={language} onClose={() => setAdding(false)} onJoined={() => { setAdding(false); onNavigate("network"); }} open={adding} />
    <NodeSettingsSheet agent={currentEditing} data={data} language={language} mutate={mutate} onClose={() => setEditing(null)} />
  </section>;
}

function NodeCard({ agent, data, language, onConfigure, onNetwork }: { agent: AgentView; data: DashboardData; language: Language; onConfigure: () => void; onNetwork: () => void }) {
  const site = data.sites.find((value) => value.id === agent.siteId);
  return <Card><CardHeader><CardTitle className="flex items-center gap-2"><ServerIcon />{agent.name}</CardTitle><CardDescription>{site?.name ?? agent.siteId} · {agent.version}</CardDescription><CardAction><StateBadge value={agent.status === "disabled" ? "disabled" : agent.connected ? "connected" : "offline"} /></CardAction></CardHeader><CardContent className="flex flex-col gap-4"><div className="flex flex-wrap gap-2">{agent.capabilities.docker ? <Badge variant="secondary">Docker</Badge> : null}{agent.capabilities.gateway ? <Badge variant="outline">Gateway</Badge> : null}{agent.capabilities.tunnel ? <Badge variant="outline">Tunnel</Badge> : null}</div><dl className="grid grid-cols-2 gap-4 text-sm"><div><dt className="text-muted-foreground">{copy(language, "网络", "Network")}</dt><dd className="mt-1 font-medium">{agent.networkProfile ? copy(language, "已确认", "Confirmed") : copy(language, "需要确认", "Needs confirmation")}</dd></div><div><dt className="text-muted-foreground">{copy(language, "最后在线", "Last seen")}</dt><dd className="mt-1 font-medium">{formatDate(language, agent.lastSeenAt)}</dd></div><div className="col-span-2"><dt className="text-muted-foreground">{copy(language, "私有服务地址", "Private service address")}</dt><dd className="mt-1 font-mono text-xs">{agent.networkProfile?.serviceAddress || "—"}</dd></div></dl></CardContent><CardFooter className="justify-end gap-2">{agent.status === "active" && !agent.networkProfile ? <Button onClick={onNetwork} size="sm"><NetworkIcon data-icon="inline-start" />{copy(language, "确认网络", "Confirm network")}</Button> : null}{agent.status === "active" ? <Button onClick={onConfigure} size="sm" variant="outline"><Settings2Icon data-icon="inline-start" />{copy(language, "管理", "Manage")}</Button> : null}</CardFooter></Card>;
}

function AddNodeSheet({ data, language, onClose, onJoined, open }: { data: DashboardData; language: Language; onClose: () => void; onJoined: () => void; open: boolean }) {
  const [name, setName] = useState("");
  const [siteID, setSiteID] = useState("");
  const [centerURL, setCenterURL] = useState("");
  const [gateway, setGateway] = useState(true);
  const [tunnel, setTunnel] = useState(false);
  const [useHeadscale, setUseHeadscale] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [enrollment, setEnrollment] = useState<AgentEnrollment | null>(null);
  const [existingAgentIDs, setExistingAgentIDs] = useState<string[]>([]);
  useEffect(() => {
    if (!open) return;
    if (!siteID) setSiteID(data.sites[0]?.id ?? "");
    if (!centerURL && !["localhost", "127.0.0.1", "::1"].includes(window.location.hostname)) setCenterURL(window.location.origin);
  }, [open, siteID, centerURL, data.sites]);
  const roles = gateway ? "worker,gateway" : "worker";
  const capabilities = ["docker", gateway ? "gateway" : "", tunnel ? "tunnel" : ""].filter(Boolean).join(",");
  const headscaleReady = data.integrations.some((integration) => integration.kind === "headscale" && integration.status === "configured");
  const command = useMemo(() => {
    if (!enrollment) return "";
    const bootstrap = enrollment.headscaleCommand ? `command -v tailscale >/dev/null 2>&1 || { echo 'Install Tailscale first.' >&2; exit 1; }; ${enrollment.headscaleCommand} && ` : "";
    if (data.status.agentInstallerAvailable) return `${bootstrap}curl -fsSL ${shellQuote(`${centerURL.replace(/\/$/, "")}/install/agent.sh`)} | sudo sh -s -- --center-url ${shellQuote(centerURL)} --token ${shellQuote(enrollment.token)} --name ${shellQuote(name)} --roles ${shellQuote(roles)} --capabilities ${shellQuote(capabilities)}`;
    return `${bootstrap}printf '%s' ${shellQuote(enrollment.token)} | sudo /usr/local/bin/vastora agent install --center-url ${shellQuote(centerURL)} --token-file - --name ${shellQuote(name)} --roles ${shellQuote(roles)} --capabilities ${shellQuote(capabilities)}`;
  }, [enrollment, data.status.agentInstallerAvailable, name, centerURL, roles, capabilities]);
  const joinedAgent = enrollment ? data.agents.find((agent) => agent.status === "active" && agent.name === name && !existingAgentIDs.includes(agent.id)) : undefined;
  const close = () => { setName(""); setSiteID(data.sites[0]?.id ?? ""); setGateway(true); setTunnel(false); setEnrollment(null); setExistingAgentIDs([]); setUseHeadscale(false); setError(""); setBusy(false); onClose(); };
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); setError("");
    if (!validCenterURL(centerURL)) { setError(copy(language, "Center 必须使用 HTTPS；只有 127.0.0.1 或 localhost 可以使用 HTTP。", "Center must use HTTPS. Only 127.0.0.1 or localhost may use HTTP.")); return; }
    setBusy(true);
    try { setExistingAgentIDs(data.agents.map((agent) => agent.id)); setEnrollment(await api.createAgentEnrollment(siteID, useHeadscale && headscaleReady, gateway)); } catch (submitError) { setError(submitError instanceof Error ? submitError.message : "Request failed"); } finally { setBusy(false); }
  };
  return <Sheet onOpenChange={(next) => { if (!next) close(); }} open={open}><SheetContent className="sm:max-w-xl"><SheetHeader><SheetTitle>{copy(language, "添加节点", "Add node")}</SheetTitle><SheetDescription>{enrollment ? copy(language, "一次性命令将在目标 Linux 设备上注册并启动 Agent 服务。", "The one-time command registers and starts the Agent service on the target Linux device.") : copy(language, "只需选择位置和用途；高级能力以后仍可调整。", "Choose its location and purpose. Advanced capabilities can be adjusted later.")}</SheetDescription></SheetHeader>{enrollment ? <div className="flex flex-1 flex-col gap-5 px-4"><Alert><TerminalIcon /><AlertTitle>{copy(language, "在目标设备运行一次", "Run once on the target device")}</AlertTitle><AlertDescription>{data.status.agentInstallerAvailable ? copy(language, enrollment.headscaleCommand ? "需要 Linux、systemd、Docker、curl 和已安装的 Tailscale。命令会先加入私网，再安装 Agent。" : "需要 Linux、systemd、Docker 和 curl；命令会自动下载匹配 CPU 的 Agent 单文件。", enrollment.headscaleCommand ? "Linux, systemd, Docker, curl, and Tailscale are required. The command joins the private network before installing Agent." : "Linux, systemd, Docker, and curl are required. The command downloads the Agent binary for the detected CPU.") : copy(language, "当前 Center 没有内置 Agent 文件，请先把 vastora 放到 /usr/local/bin/vastora。", "This Center does not include Agent binaries. Put vastora at /usr/local/bin/vastora first.")}</AlertDescription></Alert><div className="relative"><code className="block max-h-56 overflow-auto break-all rounded-xl bg-muted p-4 pr-14 text-xs leading-6">{command}</code><Button aria-label={copy(language, "复制命令", "Copy command")} className="absolute right-2 top-2" onClick={() => void navigator.clipboard.writeText(command)} size="icon" variant="outline"><CopyIcon /></Button></div><div aria-live="polite" className="flex items-start gap-3 rounded-xl border p-4">{joinedAgent ? <CheckCircle2Icon className="mt-0.5 text-success" /> : <Spinner className="mt-0.5" />}<div><p className="text-sm font-medium">{joinedAgent ? copy(language, `${joinedAgent.name} 已上线`, `${joinedAgent.name} is online`) : copy(language, "正在等待节点上线…", "Waiting for the node to come online…")}</p><p className="mt-1 text-xs text-muted-foreground">{joinedAgent ? copy(language, "下一步确认 Agent 自动发现的网络地址。", "Next, confirm the network addresses discovered by the Agent.") : copy(language, `命令将在 ${formatDate(language, enrollment.expiresAt)} 失效。`, `The command expires at ${formatDate(language, enrollment.expiresAt)}.`)}</p></div></div><Alert><CheckCircle2Icon /><AlertTitle>{copy(language, "凭据仅显示这一次", "Credential is shown only once")}</AlertTitle><AlertDescription>{copy(language, "令牌十分钟后失效且只能使用一次。关闭后如未执行，请重新生成。", "The token expires in ten minutes and works only once. Generate a new one if you close before running it.")}</AlertDescription></Alert></div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="new-node-name">{copy(language, "节点名称", "Node name")}</FieldLabel><Input autoFocus id="new-node-name" maxLength={128} onChange={(event) => setName(event.target.value)} placeholder={copy(language, "例如：新加坡服务器", "For example: Singapore server")} required value={name} /><FieldDescription>{copy(language, "使用容易识别设备或位置的名称。", "Use a name that identifies the device or location.")}</FieldDescription></Field><Field><FieldLabel htmlFor="new-node-site">{copy(language, "位置", "Location")}</FieldLabel><NativeSelect id="new-node-site" onChange={(event) => setSiteID(event.target.value)} required value={siteID}>{data.sites.map((site) => <option key={site.id} value={site.id}>{site.name}</option>)}</NativeSelect></Field><Field><FieldLabel htmlFor="new-node-center">{copy(language, "Center 地址", "Center address")}</FieldLabel><Input id="new-node-center" onChange={(event) => setCenterURL(event.target.value)} placeholder="https://center.example.com" required type="url" value={centerURL} /><FieldDescription>{copy(language, "填写目标节点能直接访问的 HTTPS 地址；不能使用本机浏览器的 127.0.0.1 转发地址。", "Use the HTTPS address reachable from the target node, not a 127.0.0.1 browser forwarding address.")}</FieldDescription></Field>{headscaleReady ? <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="new-node-headscale">{copy(language, "先加入 Headscale 私网", "Join Headscale first")}</FieldLabel><FieldDescription>{copy(language, "目标节点无法直接访问 Center 时开启；需要先安装 Tailscale。", "Enable when the target cannot reach Center directly. Tailscale must already be installed.")}</FieldDescription></div><Switch checked={useHeadscale} id="new-node-headscale" onCheckedChange={setUseHeadscale} /></Field> : null}<Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="new-node-gateway">{copy(language, "可作为访问网关", "Can act as an access gateway")}</FieldLabel><FieldDescription>{copy(language, "推荐开启。只有被选为网关时才会安装 Caddy。", "Recommended. Caddy is installed only if this node is selected as a gateway.")}</FieldDescription></div><Switch checked={gateway} id="new-node-gateway" onCheckedChange={setGateway} /></Field><Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="new-node-tunnel">Cloudflare Tunnel</FieldLabel><FieldDescription>{copy(language, "允许以后使用该节点承载 Tunnel；现在不会安装。", "Allows this node to host a Tunnel later. Nothing is installed now.")}</FieldDescription></div><Switch checked={tunnel} id="new-node-tunnel" onCheckedChange={setTunnel} /></Field>{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={close} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !name || !siteID || !centerURL} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "生成接入命令", "Generate join command")}</Button></SheetFooter></form>} {enrollment ? <SheetFooter><Button onClick={joinedAgent ? onJoined : close}>{joinedAgent ? <><NetworkIcon data-icon="inline-start" />{copy(language, "继续确认网络", "Continue to network setup")}</> : copy(language, "完成", "Done")}</Button></SheetFooter> : null}</SheetContent></Sheet>;
}

function NodeSettingsSheet({ agent, data, language, mutate, onClose }: { agent: AgentView | null; data: DashboardData; language: Language; mutate: Mutate; onClose: () => void }) {
  const [name, setName] = useState("");
  const [siteID, setSiteID] = useState("");
  const [gateway, setGateway] = useState(false);
  const [tunnel, setTunnel] = useState(false);
  const [commandKind, setCommandKind] = useState<"purpose" | "update" | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [danger, setDanger] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  useEffect(() => {
    if (!agent) return;
    setName(agent.name);
    setSiteID(agent.siteId);
    setGateway(agent.capabilities.gateway);
    setTunnel(agent.capabilities.tunnel);
    setCommandKind(null);
    setError("");
    setDanger(false);
    setConfirmation("");
  }, [agent?.id]);
  const gatewayRequired = Boolean(agent && (data.sites.some((site) => site.gatewayNodes.includes(agent.id)) || data.publications.some((publication) => publication.gatewayNodeId === agent.id && publication.status !== "stopped")));
  const tunnelRequired = Boolean(agent && data.publications.some((publication) => publication.gatewayNodeId === agent.id && publication.kind === "cloudflare_tunnel" && publication.status !== "stopped"));
  const purposeChanged = Boolean(agent && (gateway !== agent.capabilities.gateway || tunnel !== agent.capabilities.tunnel));
  const roles = gateway ? "worker,gateway" : "worker";
  const capabilities = ["docker", gateway ? "gateway" : "", tunnel ? "tunnel" : ""].filter(Boolean).join(",");
  const purposeCommand = `sudo /usr/local/bin/vastora agent configure --data-dir /var/lib/vastora/agent --roles ${shellQuote(roles)} --capabilities ${shellQuote(capabilities)}`;
  const updateCommand = "sudo /usr/local/bin/vastora agent update --data-dir /var/lib/vastora/agent";
  const command = commandKind === "purpose" ? purposeCommand : updateCommand;
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!agent) return;
    setBusy(true); setError("");
    try {
      await mutate(() => api.updateAgent(agent.id, name, siteID), copy(language, "节点信息已更新。", "Node updated."));
      onClose();
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : "Request failed");
    } finally {
      setBusy(false);
    }
  };
  const disable = async () => {
    if (!agent) return;
    setBusy(true); setError("");
    try {
      await mutate(() => api.disableAgent(agent.id), copy(language, "节点已停用，原凭据已失效。", "Node disabled and its credential is no longer accepted."));
      onClose();
    } catch (disableError) {
      setError(disableError instanceof Error ? disableError.message : "Request failed");
    } finally {
      setBusy(false);
    }
  };
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(agent)}><SheetContent className="sm:max-w-xl"><SheetHeader><SheetTitle>{copy(language, `管理 ${agent?.name ?? ""}`, `Manage ${agent?.name ?? ""}`)}</SheetTitle><SheetDescription>{danger ? copy(language, "停用会立即拒绝此 Agent 的后续连接。", "Disabling immediately rejects future connections from this Agent.") : copy(language, "修改名称、位置或节点用途；需要在节点执行的操作会生成一条命令。", "Change its name, location, or purpose. Operations that must run on the node are provided as one command.")}</SheetDescription></SheetHeader>{danger ? <div className="flex flex-1 flex-col gap-4 px-4"><Alert variant="destructive"><Trash2Icon /><AlertTitle>{copy(language, "确认停用节点", "Confirm node disable")}</AlertTitle><AlertDescription>{copy(language, "Center 会撤销管理权限，但不会远程删除节点上的二进制或数据。", "Center revokes management access but does not remotely delete the binary or local data.")}</AlertDescription></Alert><Field><FieldLabel htmlFor="disable-node-confirmation">{copy(language, `输入“${agent?.name ?? ""}”确认`, `Type “${agent?.name ?? ""}” to confirm`)}</FieldLabel><Input autoFocus id="disable-node-confirmation" onChange={(event) => setConfirmation(event.target.value)} value={confirmation} /></Field>{error ? <FieldError>{error}</FieldError> : null}</div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="node-name">{copy(language, "名称", "Name")}</FieldLabel><Input id="node-name" maxLength={128} onChange={(event) => setName(event.target.value)} required value={name} /></Field><Field><FieldLabel htmlFor="node-site"><MapPinIcon data-icon="inline-start" />{copy(language, "位置", "Location")}</FieldLabel><NativeSelect id="node-site" onChange={(event) => setSiteID(event.target.value)} value={siteID}>{data.sites.map((site) => <option key={site.id} value={site.id}>{site.name}</option>)}</NativeSelect></Field><div className="rounded-xl border p-4"><div className="mb-3 flex items-start justify-between gap-3"><div><p className="text-sm font-medium">{copy(language, "节点用途", "Node purpose")}</p><p className="mt-1 text-xs leading-5 text-muted-foreground">{copy(language, "Docker 应用始终可用；按需启用访问网关和 Cloudflare Tunnel。", "Docker apps remain available; enable Gateway and Cloudflare Tunnel only when needed.")}</p></div><Badge variant="secondary">Docker</Badge></div><div className="flex flex-col gap-3"><Field data-disabled={gatewayRequired} orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="manage-node-gateway">Gateway</FieldLabel><FieldDescription>{gatewayRequired ? copy(language, "此节点仍被位置或访问入口使用，请先移除依赖。", "This node is still used by a location or access point. Remove those dependencies first.") : copy(language, "为局域网、Headscale 或公网 Web 服务提供访问地址。", "Provides access addresses for LAN, Headscale, or public Web services.")}</FieldDescription></div><Switch checked={gateway} disabled={gatewayRequired && gateway} id="manage-node-gateway" onCheckedChange={(checked) => { setGateway(checked); setCommandKind(null); }} /></Field><Field data-disabled={tunnelRequired} orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="manage-node-tunnel">Cloudflare Tunnel</FieldLabel><FieldDescription>{tunnelRequired ? copy(language, "此节点仍承载 Tunnel 入口，请先停止相关入口。", "This node still hosts Tunnel access points. Stop them first.") : copy(language, "仅在需要通过 Cloudflare 发布 Web 服务时启用。", "Enable only when this node will publish Web services through Cloudflare.")}</FieldDescription></div><Switch checked={tunnel} disabled={tunnelRequired && tunnel} id="manage-node-tunnel" onCheckedChange={(checked) => { setTunnel(checked); setCommandKind(null); }} /></Field></div><Button className="mt-3" disabled={!purposeChanged} onClick={() => setCommandKind("purpose")} size="sm" type="button" variant="outline"><TerminalIcon data-icon="inline-start" />{copy(language, "生成修改命令", "Generate change command")}</Button></div><div className="rounded-xl border p-4"><div className="flex items-start justify-between gap-3"><div><p className="text-sm font-medium">Agent</p><p className="mt-1 text-xs text-muted-foreground">{copy(language, `节点版本 ${agent?.version ?? "—"} · Center 版本 ${data.status.version}`, `Node ${agent?.version ?? "—"} · Center ${data.status.version}`)}</p></div><StateBadge value={agent?.version === data.status.version ? "ready" : "pending"} /></div><Button className="mt-3" onClick={() => setCommandKind("update")} size="sm" type="button" variant="outline"><TerminalIcon data-icon="inline-start" />{agent?.version === data.status.version ? copy(language, "重新安装当前版本", "Reinstall current version") : copy(language, "更新 Agent", "Update Agent")}</Button></div>{commandKind ? <Alert><TerminalIcon /><AlertTitle>{commandKind === "purpose" ? copy(language, "在节点运行一次", "Run once on the node") : copy(language, "安全更新 Agent", "Safely update Agent")}</AlertTitle><AlertDescription><p>{commandKind === "purpose" ? copy(language, "命令会更新 systemd 配置并重启 Agent，Center 会自动看到新用途。", "The command updates systemd, restarts Agent, and Center detects the new purpose automatically.") : copy(language, "Agent 会验证下载摘要和版本；若服务重启失败，会恢复上一版。", "Agent verifies the download digest and version. If restart fails, it restores the previous binary.")}</p><div className="relative mt-3"><code className="block break-all rounded-lg bg-muted p-3 pr-12 text-xs leading-5">{command}</code><Button aria-label={copy(language, "复制命令", "Copy command")} className="absolute right-1.5 top-1.5" onClick={() => void navigator.clipboard.writeText(command)} size="icon-sm" type="button" variant="outline"><CopyIcon /></Button></div></AlertDescription></Alert> : null}{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></div><SheetFooter className="justify-between"><Button onClick={() => setDanger(true)} type="button" variant="ghost"><Trash2Icon data-icon="inline-start" />{copy(language, "停用节点", "Disable node")}</Button><div className="flex gap-2"><Button onClick={onClose} type="button" variant="outline">{copy(language, "关闭", "Close")}</Button><Button disabled={busy || !name || name === agent?.name && siteID === agent?.siteId} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "保存信息", "Save details")}</Button></div></SheetFooter></form>}{danger ? <SheetFooter><Button onClick={() => { setDanger(false); setError(""); }} variant="outline">{copy(language, "返回", "Back")}</Button><Button disabled={busy || confirmation !== agent?.name} onClick={() => void disable()} variant="destructive">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "停用节点", "Disable node")}</Button></SheetFooter> : null}</SheetContent></Sheet>;
}

function shellQuote(value: string) { return `'${value.replaceAll("'", `'\\''`)}'`; }
export function validCenterURL(value: string) {
  try {
    const parsed = new URL(value);
    if (parsed.username || parsed.password || parsed.search || parsed.hash) return false;
    if (parsed.protocol === "https:") return true;
    return parsed.protocol === "http:" && (parsed.hostname === "127.0.0.1" || parsed.hostname === "localhost" || parsed.hostname === "::1");
  } catch {
    return false;
  }
}
