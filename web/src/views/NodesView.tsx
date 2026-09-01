import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { CheckCircle2Icon, CircleArrowUpIcon, MapPinIcon, NetworkIcon, PlusIcon, RotateCcwIcon, ServerIcon, Settings2Icon, ShieldCheckIcon, TerminalIcon, Trash2Icon } from "lucide-react";
import { api } from "../api";
import { validCenterURL } from "../lib/network";
import type { AppData, Mutate, Screen } from "../App";
import type { AgentEnrollment, AgentView } from "../types";
import type { Language } from "../translations";
import { CopyButton, PageHeading, StateBadge, copy, formatDate, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SelectControl } from "@/components/SelectControl";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

export { validCenterURL } from "../lib/network";

export function agentInstallCommand({ centerURL, enrollment, installerAvailable }: { centerURL: string; enrollment: AgentEnrollment; installerAvailable: boolean }) {
  if (installerAvailable) {
    const installer = "/tmp/vastora-agent-install.sh";
    return `curl -fsSL ${shellQuote(`${enrollment.installerUrl.replace(/\/$/, "")}/install/agent.sh`)} -o ${installer} && chmod +x ${installer} && ${installer} ${shellQuote(enrollment.token)}`;
  }
  return `printf '%s' ${shellQuote(enrollment.token)} | sudo /usr/local/bin/vastora agent install --center-url ${shellQuote(centerURL)} --token-file -`;
}

export function NodesView({ data, language, mutate, onAddFirstNodeHandled, onNavigate, startAdding = false }: { data: AppData; language: Language; mutate: Mutate; onAddFirstNodeHandled?: () => void; onNavigate: (screen: Screen) => void; startAdding?: boolean }) {
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<AgentView | null>(null);
  const currentEditing = editing ? data.agents.find((agent) => agent.id === editing.id) ?? editing : null;
  useEffect(() => {
    if (startAdding) setAdding(true);
  }, [startAdding]);
  const siteGroups = data.sites.map((site) => ({ site, agents: data.agents.filter((agent) => agent.siteId === site.id) })).filter((group) => group.agents.length > 0);
  return <section className="flex flex-col gap-7">
    <PageHeading title={copy(language, "节点", "Nodes")} description={copy(language, "节点是运行应用的设备。添加后，Center 会自动发现它的网络。", "Nodes are devices that run apps. Center discovers their networks after they join.")} action={<Button onClick={() => setAdding(true)}><PlusIcon data-icon="inline-start" />{copy(language, "添加节点", "Add node")}</Button>} />
    {data.agents.length === 0 ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><ServerIcon /></EmptyMedia><EmptyTitle>{copy(language, "添加第一台节点", "Add your first node")}</EmptyTitle><EmptyDescription>{copy(language, "当前 Center 主机或另一台装有 Docker 的 Linux 设备都可以作为节点，复制一条命令即可接入。", "The current Center host or another Docker-enabled Linux device can be a node. Join it with one command.")}</EmptyDescription><Button className="mt-3" onClick={() => setAdding(true)}><PlusIcon data-icon="inline-start" />{copy(language, "开始添加", "Get started")}</Button></EmptyHeader></Empty> : <div className="flex flex-col gap-7">{siteGroups.map(({ site, agents }) => <section className="flex flex-col gap-3" key={site.id}><div className="flex items-center gap-2"><MapPinIcon className="size-4 text-muted-foreground" /><h2 className="text-sm font-semibold">{site.name}</h2><Badge variant="secondary">{site.code}</Badge><span className="text-xs text-muted-foreground">{copy(language, `${agents.length} 台节点`, `${agents.length} node${agents.length === 1 ? "" : "s"}`)}</span></div><div className="grid gap-4 lg:grid-cols-2">{agents.map((agent) => <NodeCard agent={agent} data={data} key={agent.id} language={language} onConfigure={() => setEditing(agent)} onNetwork={() => onNavigate("network")} />)}</div></section>)}</div>}
    <AddNodeSheet data={data} language={language} onClose={() => { setAdding(false); onAddFirstNodeHandled?.(); }} onJoined={() => { setAdding(false); onAddFirstNodeHandled?.(); onNavigate("network"); }} open={adding} />
    <NodeSettingsSheet agent={currentEditing} data={data} language={language} mutate={mutate} onClose={() => setEditing(null)} />
  </section>;
}

function NodeCard({ agent, data, language, onConfigure, onNetwork }: { agent: AgentView; data: AppData; language: Language; onConfigure: () => void; onNetwork: () => void }) {
  const site = data.sites.find((value) => value.id === agent.siteId);
  const selectedGateway = Boolean(site?.gatewayNodes.includes(agent.id));
  const architecture = agent.architecture === "arm64" ? "ARM64" : "x64";
  return <Card><CardHeader><CardTitle className="flex items-center gap-2"><ServerIcon />{agent.name}</CardTitle><CardDescription>{copy(language, "位置", "Location")}：{site?.name ?? agent.siteId} · {agent.version}</CardDescription><CardAction><StateBadge value={agent.status === "disabled" ? "disabled" : agent.connected ? "connected" : "offline"} /></CardAction></CardHeader><CardContent className="flex flex-col gap-4"><div className="flex flex-wrap gap-2"><Badge variant="outline">{architecture}</Badge>{agent.capabilities.docker ? <Badge variant="secondary">{copy(language, "运行应用", "Runs apps")}</Badge> : null}{selectedGateway ? <Badge variant="default">{copy(language, "当前位置网关", "Location gateway")}</Badge> : agent.capabilities.gateway ? <Badge variant="outline">{copy(language, "可作为网关", "Gateway capable")}</Badge> : null}{agent.capabilities.tunnel ? <Badge variant="outline">Cloudflare</Badge> : null}</div><dl className="grid grid-cols-2 gap-4 text-sm"><div><dt className="text-muted-foreground">{copy(language, "网络", "Network")}</dt><dd className="mt-1 font-medium">{agent.networkProfile ? copy(language, "已确认", "Confirmed") : copy(language, "需要确认", "Needs confirmation")}</dd></div><div><dt className="text-muted-foreground">{copy(language, "最后在线", "Last seen")}</dt><dd className="mt-1 font-medium">{formatDate(language, agent.lastSeenAt)}</dd></div><div className="col-span-2"><dt className="text-muted-foreground">{copy(language, "私有服务地址", "Private service address")}</dt><dd className="mt-1 font-mono text-xs">{agent.networkProfile?.serviceAddress || "—"}</dd></div></dl></CardContent><CardFooter className="justify-end gap-2">{agent.status === "active" && !agent.networkProfile ? <Button onClick={onNetwork} size="sm"><NetworkIcon data-icon="inline-start" />{copy(language, "确认网络", "Confirm network")}</Button> : null}{agent.status === "active" ? <Button onClick={onConfigure} size="sm" variant="outline"><Settings2Icon data-icon="inline-start" />{copy(language, "管理", "Manage")}</Button> : null}</CardFooter></Card>;
}

function AddNodeSheet({ data, language, onClose, onJoined, open }: { data: AppData; language: Language; onClose: () => void; onJoined: () => void; open: boolean }) {
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
  const initialized = useRef(false);
  useEffect(() => {
    if (!open) { initialized.current = false; return; }
    if (initialized.current) return;
    initialized.current = true;
    setSiteID(data.sites[0]?.id ?? "");
    setCenterURL(data.status.agentConnectUrl);
    setUseHeadscale(data.status.agentConnectionMode === "headscale");
  }, [open, data.sites, data.status.agentConnectUrl, data.status.agentConnectionMode]);
  const headscaleReady = data.integrations.some((integration) => integration.kind === "headscale" && integration.status === "configured");
  const firstPrivateNode = data.agents.length === 0 && data.status.agentConnectionMode === "headscale" && useHeadscale && headscaleReady;
  const command = useMemo(() => {
    if (!enrollment) return "";
    return agentInstallCommand({ centerURL, enrollment, installerAvailable: data.status.agentInstallerAvailable });
  }, [enrollment, data.status.agentInstallerAvailable, centerURL]);
  const joinedAgent = enrollment ? data.agents.find((agent) => agent.status === "active" && agent.name === name && !existingAgentIDs.includes(agent.id)) : undefined;
  const close = () => { initialized.current = false; setName(""); setSiteID(""); setCenterURL(""); setGateway(true); setTunnel(false); setEnrollment(null); setExistingAgentIDs([]); setUseHeadscale(false); setError(""); setBusy(false); onClose(); };
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); setError("");
    if (!validCenterURL(centerURL)) { setError(copy(language, "Center 必须使用 HTTPS；只有 127.0.0.1 或 localhost 可以使用 HTTP。", "Center must use HTTPS. Only 127.0.0.1 or localhost may use HTTP.")); return; }
    setBusy(true);
    try {
      setExistingAgentIDs(data.agents.map((agent) => agent.id));
      const connectionURL = firstPrivateNode ? "http://127.0.0.1:8080" : centerURL;
      setEnrollment(await api.createAgentEnrollment(siteID, name, connectionURL, useHeadscale && headscaleReady, gateway, tunnel));
    } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); }
  };
  return (
    <Sheet onOpenChange={(next) => { if (!next) close(); }} open={open}>
      <SheetContent className="sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{copy(language, "添加节点", "Add node")}</SheetTitle>
          <SheetDescription>{enrollment ? copy(language, "在要作为节点的 Linux 设备运行一次下面的命令；它可以就是当前 Center 主机。", "Run the command once on the Linux device that will become the node. It can be this Center host.") : copy(language, "填写名称和位置即可。当前 Center 主机也可以同时作为应用节点。", "Enter a name and location. This Center host can also serve as an app node.")}</SheetDescription>
        </SheetHeader>
        {enrollment ? <div className="flex flex-1 flex-col gap-5 px-4">
          <Alert><TerminalIcon /><AlertTitle>{copy(language, "在目标设备运行一次", "Run once on the target device")}</AlertTitle><AlertDescription>{data.status.agentInstallerAvailable ? copy(language, useHeadscale ? "需要 Linux、systemd、Docker 和 curl。脚本会自动安装 Tailscale、加入安全私网并安装 Agent。" : "需要 Linux、systemd、Docker 和 curl。脚本会读取 Center 保存的配置并自动安装 Agent。", useHeadscale ? "Linux, systemd, Docker, and curl are required. The script installs Tailscale, joins the private network, and installs Agent automatically." : "Linux, systemd, Docker, and curl are required. The script reads the configuration saved by Center and installs Agent automatically.") : copy(language, "当前 Center 没有内置 Agent 文件，请先把 vastora 放到 /usr/local/bin/vastora。", "This Center does not include Agent binaries. Put vastora at /usr/local/bin/vastora first.")}</AlertDescription></Alert>
          <div className="relative"><code className="block max-h-56 overflow-auto break-all rounded-xl bg-muted p-4 pr-14 text-xs leading-6">{command}</code><CopyButton className="absolute right-2 top-2" label={copy(language, "复制命令", "Copy command")} language={language} size="icon" value={command} /></div>
          <div aria-live="polite" className="flex items-start gap-3 rounded-xl border p-4">{joinedAgent ? <CheckCircle2Icon className="mt-0.5 text-success" /> : <Spinner className="mt-0.5" />}<div><p className="text-sm font-medium">{joinedAgent ? copy(language, `${joinedAgent.name} 已上线`, `${joinedAgent.name} is online`) : copy(language, "正在等待节点上线…", "Waiting for the node to come online…")}</p><p className="mt-1 text-xs text-muted-foreground">{joinedAgent ? copy(language, "下一步确认 Agent 自动发现的网络地址。", "Next, confirm the network addresses discovered by the Agent.") : copy(language, `命令将在 ${formatDate(language, enrollment.expiresAt)} 失效。`, `The command expires at ${formatDate(language, enrollment.expiresAt)}.`)}</p></div></div>
          <Alert><CheckCircle2Icon /><AlertTitle>{copy(language, "凭据仅显示这一次", "Credential is shown only once")}</AlertTitle><AlertDescription>{copy(language, "令牌十分钟后失效且只能使用一次。关闭后如未执行，请重新生成。", "The token expires in ten minutes and works only once. Generate a new one if you close before running it.")}</AlertDescription></Alert>
        </div> : (
          <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}>
            <div className="flex-1 overflow-y-auto px-4">
              <FieldGroup>
                {firstPrivateNode ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "先让当前 Center 主机加入私网", "Join this Center host first")}</AlertTitle><AlertDescription>{copy(language, "请在安装 Center 的这台服务器运行生成的命令。完成网络确认后，其他节点就能通过私网地址连接。", "Run the generated command on the server hosting Center. After confirming its network, other nodes can connect through the private address.")}</AlertDescription></Alert> : null}
                <Field><FieldLabel htmlFor="new-node-name">{copy(language, "节点名称", "Node name")}</FieldLabel><Input autoFocus id="new-node-name" maxLength={128} onChange={(event) => setName(event.target.value)} placeholder={copy(language, "例如：新加坡服务器", "For example: Singapore server")} required value={name} /><FieldDescription>{copy(language, "使用容易识别设备或位置的名称。", "Use a name that identifies the device or location.")}</FieldDescription></Field>
                <Field><FieldLabel htmlFor="new-node-site">{copy(language, "位置", "Location")}</FieldLabel><SelectControl id="new-node-site" onValueChange={setSiteID} options={data.sites.map((site) => ({ value: site.id, label: site.name }))} required value={siteID} /></Field>
                <div className="rounded-xl border bg-muted/25 p-4"><div className="flex items-center justify-between gap-3"><div><p className="text-sm font-medium">{copy(language, "Agent 将连接 Center", "Agent will connect to Center")}</p><p className="mt-1 text-xs text-muted-foreground">{data.status.agentConnectionMode === "headscale" ? copy(language, "使用安全私网", "Using the secure private network") : data.status.agentConnectionMode === "public" ? copy(language, "使用公网安全连接", "Using a secure public connection") : copy(language, "使用同一局域网", "Using the same local network")}</p></div><Badge variant="secondary">{copy(language, "已自动配置", "Automatic")}</Badge></div></div>
                <details className="rounded-xl border p-3">
                  <summary className="cursor-pointer text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}</summary>
                  <div className="mt-4 flex flex-col gap-4">
                    <Field><FieldLabel htmlFor="new-node-center">{copy(language, "Center 地址", "Center address")}</FieldLabel><Input id="new-node-center" onChange={(event) => setCenterURL(event.target.value)} placeholder="https://center.example.com" required type="url" value={centerURL} /><FieldDescription>{copy(language, "仅当此节点需要使用不同的连接地址时修改。", "Change only when this node needs a different connection address.")}</FieldDescription></Field>
                    {headscaleReady ? <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="new-node-headscale">{copy(language, "先加入安全私网", "Join the secure private network first")}</FieldLabel><FieldDescription>{copy(language, "目标节点无法直接访问 Center 时开启；脚本会自动安装 Tailscale。", "Enable when the target cannot reach Center directly. The script installs Tailscale automatically.")}</FieldDescription></div><Switch checked={useHeadscale} id="new-node-headscale" onCheckedChange={setUseHeadscale} /></Field> : null}
                    <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="new-node-gateway">{copy(language, "可提供服务入口", "Can provide service access")}</FieldLabel><FieldDescription>{copy(language, "推荐开启；只有实际使用时才会安装网关组件。", "Recommended. Gateway components are installed only when used.")}</FieldDescription></div><Switch checked={gateway} id="new-node-gateway" onCheckedChange={setGateway} /></Field>
                    <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="new-node-tunnel">Cloudflare Tunnel</FieldLabel><FieldDescription>{copy(language, "允许以后通过该节点发布网页；现在不会安装。", "Allows this node to publish websites later. Nothing is installed now.")}</FieldDescription></div><Switch checked={tunnel} id="new-node-tunnel" onCheckedChange={setTunnel} /></Field>
                  </div>
                </details>
                {error ? <FieldError role="alert">{error}</FieldError> : null}
              </FieldGroup>
            </div>
            <SheetFooter><Button onClick={close} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !name || !siteID || !centerURL} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "生成接入命令", "Generate join command")}</Button></SheetFooter>
          </form>
        )}
        {enrollment ? <SheetFooter><Button onClick={joinedAgent ? onJoined : close}>{joinedAgent ? <><NetworkIcon data-icon="inline-start" />{copy(language, "继续确认网络", "Continue to network setup")}</> : copy(language, "完成", "Done")}</Button></SheetFooter> : null}
      </SheetContent>
    </Sheet>
  );
}

function NodeSettingsSheet({ agent, data, language, mutate, onClose }: { agent: AgentView | null; data: AppData; language: Language; mutate: Mutate; onClose: () => void }) {
  const [name, setName] = useState("");
  const [siteID, setSiteID] = useState("");
  const [gateway, setGateway] = useState(false);
  const [tunnel, setTunnel] = useState(false);
  const [commandKind, setCommandKind] = useState<"purpose" | null>(null);
  const [busy, setBusy] = useState(false);
  const [updateBusy, setUpdateBusy] = useState(false);
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
  const manualUpdateCommand = "sudo /usr/local/bin/vastora agent update --data-dir /var/lib/vastora/agent";
  const updateActive = agent?.update?.state === "pending" || agent?.update?.state === "running" || agent?.update?.state === "installing";
  const updateRequired = Boolean(agent && agent.version !== data.status.version);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!agent) return;
    setBusy(true); setError("");
    try {
      await mutate(() => api.updateAgent(agent.id, name, siteID), copy(language, "节点信息已更新。", "Node updated."));
      onClose();
    } catch (submitError) {
      setError(userError(language, submitError));
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
      setError(userError(language, disableError));
    } finally {
      setBusy(false);
    }
  };
  const startUpdate = async () => {
    if (!agent) return;
    setUpdateBusy(true); setError("");
    try {
      await mutate(() => api.startAgentUpdate(agent.id), copy(language, "已向 Agent 下发安全更新。", "Secure Agent update queued."));
    } catch (updateError) {
      setError(userError(language, updateError));
    } finally {
      setUpdateBusy(false);
    }
  };
  return (
    <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(agent)}>
      <SheetContent className="sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{copy(language, `管理 ${agent?.name ?? ""}`, `Manage ${agent?.name ?? ""}`)}</SheetTitle>
          <SheetDescription>{danger ? copy(language, "停用会立即拒绝此 Agent 的后续连接。", "Disabling immediately rejects future connections from this Agent.") : copy(language, "修改名称、位置或节点用途；支持的 Agent 可以由 Center 安全更新。", "Change its name, location, or purpose. Supported Agents can update securely through Center.")}</SheetDescription>
        </SheetHeader>
        {danger ? <div className="flex flex-1 flex-col gap-4 px-4">
          <Alert variant="destructive"><Trash2Icon /><AlertTitle>{copy(language, "确认停用节点", "Confirm node disable")}</AlertTitle><AlertDescription>{copy(language, "Center 会撤销管理权限，但不会远程删除节点上的二进制或数据。", "Center revokes management access but does not remotely delete the binary or local data.")}</AlertDescription></Alert>
          <Field><FieldLabel htmlFor="disable-node-confirmation">{copy(language, `输入“${agent?.name ?? ""}”确认`, `Type “${agent?.name ?? ""}” to confirm`)}</FieldLabel><Input autoFocus id="disable-node-confirmation" onChange={(event) => setConfirmation(event.target.value)} value={confirmation} /></Field>
          {error ? <FieldError>{error}</FieldError> : null}
        </div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}>
          <div className="flex-1 overflow-y-auto px-4"><FieldGroup>
            <Field><FieldLabel htmlFor="node-name">{copy(language, "名称", "Name")}</FieldLabel><Input id="node-name" maxLength={128} onChange={(event) => setName(event.target.value)} required value={name} /></Field>
            <Field><FieldLabel htmlFor="node-site"><MapPinIcon data-icon="inline-start" />{copy(language, "位置", "Location")}</FieldLabel><SelectControl id="node-site" onValueChange={setSiteID} options={data.sites.map((site) => ({ value: site.id, label: site.name }))} value={siteID} /></Field>
            <div className="rounded-xl border p-4">
              <div className="mb-3 flex items-start justify-between gap-3"><div><p className="text-sm font-medium">{copy(language, "节点用途", "Node purpose")}</p><p className="mt-1 text-xs leading-5 text-muted-foreground">{copy(language, "Docker 应用始终可用；按需启用访问网关和 Cloudflare Tunnel。", "Docker apps remain available; enable Gateway and Cloudflare Tunnel only when needed.")}</p></div><Badge variant="secondary">Docker</Badge></div>
              <div className="flex flex-col gap-3">
                <Field data-disabled={gatewayRequired} orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="manage-node-gateway">Gateway</FieldLabel><FieldDescription>{gatewayRequired ? copy(language, "此节点仍被位置或访问入口使用，请先移除依赖。", "This node is still used by a location or access point. Remove those dependencies first.") : copy(language, "为局域网、Headscale 或公网 Web 服务提供访问地址。", "Provides access addresses for LAN, Headscale, or public Web services.")}</FieldDescription></div><Switch checked={gateway} disabled={gatewayRequired && gateway} id="manage-node-gateway" onCheckedChange={(checked) => { setGateway(checked); setCommandKind(null); }} /></Field>
                <Field data-disabled={tunnelRequired} orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="manage-node-tunnel">Cloudflare Tunnel</FieldLabel><FieldDescription>{tunnelRequired ? copy(language, "此节点仍承载 Tunnel 入口，请先停止相关入口。", "This node still hosts Tunnel access points. Stop them first.") : copy(language, "仅在需要通过 Cloudflare 发布 Web 服务时启用。", "Enable only when this node will publish Web services through Cloudflare.")}</FieldDescription></div><Switch checked={tunnel} disabled={tunnelRequired && tunnel} id="manage-node-tunnel" onCheckedChange={(checked) => { setTunnel(checked); setCommandKind(null); }} /></Field>
              </div>
              <Button className="mt-3" disabled={!purposeChanged} onClick={() => setCommandKind("purpose")} size="sm" type="button" variant="outline"><TerminalIcon data-icon="inline-start" />{copy(language, "生成修改命令", "Generate change command")}</Button>
            </div>
            <div className="rounded-xl border p-4"><div className="flex items-start justify-between gap-3"><div><p className="text-sm font-medium">Agent</p><p className="mt-1 text-xs text-muted-foreground">{copy(language, `节点版本 ${agent?.version ?? "—"} · Center 版本 ${data.status.version}`, `Node ${agent?.version ?? "—"} · Center ${data.status.version}`)}</p></div><StateBadge value={updateActive ? "applying" : !updateRequired ? "ready" : agent?.update?.state === "failed" ? "failed" : "pending"} /></div>{(updateRequired || updateActive) && agent?.remoteUpdateSupported ? <><Button className="mt-3" disabled={!agent.connected || updateActive || updateBusy} onClick={() => void startUpdate()} size="sm" type="button" variant="outline">{updateBusy || updateActive ? <Spinner data-icon="inline-start" /> : agent.update?.state === "failed" ? <RotateCcwIcon data-icon="inline-start" /> : <CircleArrowUpIcon data-icon="inline-start" />}{updateActive ? copy(language, "正在更新", "Updating") : agent.update?.state === "failed" ? copy(language, "重试更新", "Retry update") : copy(language, "通过 Center 更新", "Update through Center")}</Button>{!agent.connected ? <p className="mt-2 text-xs text-muted-foreground">{copy(language, "节点重新上线后才能开始更新。", "The node must reconnect before updating.")}</p> : null}{updateActive ? <p className="mt-2 text-xs text-muted-foreground">{copy(language, `正在安全更新到 ${agent.update?.targetVersion}；节点会短暂离线并自动重新连接。`, `Safely updating to ${agent.update?.targetVersion}. The node briefly disconnects and reconnects automatically.`)}</p> : null}{agent.update?.state === "failed" && agent.update.lastError ? <FieldError className="mt-2">{agent.update.lastError}</FieldError> : null}</> : null}{updateRequired && !agent?.remoteUpdateSupported ? <Alert className="mt-3"><TerminalIcon /><AlertTitle>{copy(language, "需要一次手动更新", "One manual update required")}</AlertTitle><AlertDescription><p>{copy(language, "当前版本还不支持 Center 远程更新。完成这一次后，后续版本可直接在这里更新。", "This version predates Center-managed updates. After this one-time step, future updates can run here.")}</p><div className="relative mt-3"><code className="block break-all rounded-lg bg-muted p-3 pr-12 text-xs leading-5">{manualUpdateCommand}</code><CopyButton className="absolute right-1.5 top-1.5" label={copy(language, "复制命令", "Copy command")} language={language} size="icon-sm" value={manualUpdateCommand} /></div></AlertDescription></Alert> : null}</div>
            {commandKind ? <Alert><TerminalIcon /><AlertTitle>{copy(language, "在节点运行一次", "Run once on the node")}</AlertTitle><AlertDescription><p>{copy(language, "命令会更新 systemd 配置并重启 Agent，Center 会自动看到新用途。", "The command updates systemd, restarts Agent, and Center detects the new purpose automatically.")}</p><div className="relative mt-3"><code className="block break-all rounded-lg bg-muted p-3 pr-12 text-xs leading-5">{purposeCommand}</code><CopyButton className="absolute right-1.5 top-1.5" label={copy(language, "复制命令", "Copy command")} language={language} size="icon-sm" value={purposeCommand} /></div></AlertDescription></Alert> : null}
            {error ? <FieldError>{error}</FieldError> : null}
          </FieldGroup></div>
          <SheetFooter className="justify-between"><Button onClick={() => setDanger(true)} type="button" variant="ghost"><Trash2Icon data-icon="inline-start" />{copy(language, "停用节点", "Disable node")}</Button><div className="flex gap-2"><Button onClick={onClose} type="button" variant="outline">{copy(language, "关闭", "Close")}</Button><Button disabled={busy || !name || name === agent?.name && siteID === agent?.siteId} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "保存信息", "Save details")}</Button></div></SheetFooter>
        </form>}
        {danger ? <SheetFooter><Button onClick={() => { setDanger(false); setError(""); }} variant="outline">{copy(language, "返回", "Back")}</Button><Button disabled={busy || confirmation !== agent?.name} onClick={() => void disable()} variant="destructive">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "停用节点", "Disable node")}</Button></SheetFooter> : null}
      </SheetContent>
    </Sheet>
  );
}

function shellQuote(value: string) { return `'${value.replaceAll("'", `'\\''`)}'`; }
