import { NodeSSHProtection } from "./NodeSSHProtection";
import { NodeSecurityControls } from "./NodeSecurityControls";
import { canInspectXrayConfiguration } from "./xrayRecoveryEligibility";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { CheckCircle2Icon, CircleArrowUpIcon, GitCompareArrowsIcon, MapPinIcon, NetworkIcon, PlusIcon, RotateCcwIcon, SearchIcon, ServerIcon, Settings2Icon, ShieldCheckIcon, TerminalIcon, Trash2Icon, UnplugIcon } from "lucide-react";
import { api } from "../api";
import { validCenterURL } from "../lib/network";
import type { AppData, Mutate, Screen } from "../App";
import type { AgentEnrollment, AgentEnrollmentStatus, AgentView } from "../types";
import type { Language } from "../translations";
import { CopyButton, PageHeading, StateBadge, copy, formatDate, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { SelectControl } from "@/components/SelectControl";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { displayRegionFlag } from "@/lib/regions";
import { RuntimeRecoveryAlert, XrayConfigurationRecoverySheet } from "./RuntimeRecoveryAlert";
import { RegionFlag } from "./RegionFlag";
import { StopNodeAccessSheet } from "./StopNodeAccessSheet";
import { RemoveNodeDialog } from "./RemoveNodeDialog";
import { ReinstallNodeSheet } from "./ReinstallNodeSheet";
import { agentInstallCommand, shellQuote } from "../lib/agent-install";
import { AgentUpdateRecoveryDialog } from "./AgentUpdateRecoveryDialog";

export { validCenterURL } from "../lib/network";

type NodesViewProps = { data: AppData; language: Language; mutate: Mutate; onAddFirstNodeHandled?: () => void; onNavigate: (screen: Screen) => void; startAdding?: boolean };

export function NodesView({ data, language, mutate, onAddFirstNodeHandled, onNavigate, startAdding = false }: NodesViewProps) {
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<AgentView | null>(null);
  const [stoppingAccessID, setStoppingAccessID] = useState<string | null>(null);
  const [removingID, setRemovingID] = useState<string | null>(null);
  const removingAgent = data.agents.find((agent) => agent.id === removingID);
  const stoppingAccessAgent = data.agents.find((agent) => agent.id === stoppingAccessID);
  const [reconnecting, setReconnecting] = useState<AgentView | null>(null);
  const [query, setQuery] = useState("");
  const [siteFilter, setSiteFilter] = useState("all");
  const [statusFilter, setStatusFilter] = useState("all");
  const [sort, setSort] = useState("site");
  const currentEditing = editing ? data.agents.find((agent) => agent.id === editing.id) ?? editing : null;
  const currentReconnecting = reconnecting ? data.agents.find((agent) => agent.id === reconnecting.id) ?? reconnecting : null;
  useEffect(() => {
    if (startAdding) setAdding(true);
  }, [startAdding]);
  const siteByID = useMemo(() => new Map(data.sites.map((site) => [site.id, site])), [data.sites]);
  const summary = useMemo(() => ({
    connected: data.agents.filter((agent) => agent.status === "active" && agent.connected && !agent.credentialRevoked).length,
    attention: data.agents.filter(nodeNeedsAttention).length,
  }), [data.agents]);
  const visibleAgents = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase();
    return data.agents
      .filter((agent) => siteFilter === "all" || agent.siteId === siteFilter)
      .filter((agent) => statusFilter === "all" || matchesNodeStatus(agent, statusFilter))
      .filter((agent) => {
        if (!normalizedQuery) return true;
        const site = siteByID.get(agent.siteId);
        return [agent.name, agent.version, agent.architecture, site?.name, site?.code]
          .some((value) => value?.toLocaleLowerCase().includes(normalizedQuery));
      })
      .sort((left, right) => compareNodes(left, right, sort, siteByID));
  }, [data.agents, query, siteByID, siteFilter, sort, statusFilter]);
  const visibleGroups = useMemo(() => {
    const groups = new Map<string, { site: AppData["sites"][number]; agents: AgentView[] }>();
    for (const agent of visibleAgents) {
      const site = siteByID.get(agent.siteId);
      if (!site) continue;
      const group = groups.get(site.id) ?? { site, agents: [] };
      group.agents.push(agent);
      groups.set(site.id, group);
    }
    return [...groups.values()];
  }, [siteByID, visibleAgents]);
  return <section className="mac-nodes flex flex-col gap-5">
    <PageHeading title={copy(language, "节点", "Nodes")} description={copy(language, "管理运行应用的设备", "Manage devices that run apps")} action={<Button disabled={data.nodeAuxiliaryDataUnavailable} onClick={() => setAdding(true)}><PlusIcon data-icon="inline-start" />{copy(language, "添加节点", "Add node")}</Button>} />
    {data.nodeAuxiliaryDataUnavailable ? <Alert><AlertTitle>{copy(language, "节点辅助数据暂不可用", "Node auxiliary data is unavailable")}</AlertTitle><AlertDescription>{copy(language, "节点在线状态仍会刷新。辅助配置可能是旧数据；添加节点、修改用途和依赖清单的恢复操作暂不可用，系统会自动重试读取。", "Node connectivity still refreshes. Auxiliary settings may be stale; adding nodes, changing purpose and inventory-dependent recovery are unavailable until an automatic refresh succeeds.")}</AlertDescription></Alert> : null}
    {data.agents.length === 0 ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><ServerIcon /></EmptyMedia><EmptyTitle>{copy(language, "添加第一台节点", "Add your first node")}</EmptyTitle><EmptyDescription>{copy(language, "当前 Center 主机或另一台受支持的 Linux 设备都可以作为节点；复制一条命令即可按需安装 Docker 和 Agent。", "The current Center host or another supported Linux device can be a node. Copy one command to install Docker when needed and then install Agent.")}</EmptyDescription><Button className="mt-3" disabled={data.nodeAuxiliaryDataUnavailable} onClick={() => setAdding(true)}><PlusIcon data-icon="inline-start" />{copy(language, "开始添加", "Get started")}</Button></EmptyHeader></Empty> : <div className="flex min-w-0 flex-col gap-4">
      <FleetSummary agents={data.agents.length} connected={summary.connected} attention={summary.attention} language={language} />
      <div className="mac-workspace-toolbar flex flex-wrap items-center gap-2">
        <InputGroup className="w-full sm:w-64 lg:w-80">
          <InputGroupInput aria-label={copy(language, "搜索节点", "Search nodes")} onChange={(event) => setQuery(event.target.value)} placeholder={copy(language, "搜索节点、位置或版本…", "Search node, location, or version…")} type="search" value={query} />
          <InputGroupAddon><SearchIcon aria-hidden="true" /></InputGroupAddon>
        </InputGroup>
        <SelectControl aria-label={copy(language, "按位置筛选", "Filter by location")} className="w-40" onValueChange={setSiteFilter} options={[{ value: "all", label: copy(language, "所有位置", "All locations") }, ...data.sites.map((site) => ({ value: site.id, label: siteLabel(site) }))]} size="sm" value={siteFilter} />
        <SelectControl aria-label={copy(language, "按状态筛选", "Filter by status")} className="w-36" onValueChange={setStatusFilter} options={[{ value: "all", label: copy(language, "所有状态", "All statuses") }, { value: "connected", label: copy(language, "已连接", "Connected") }, { value: "attention", label: copy(language, "需要处理", "Needs attention") }, { value: "offline", label: copy(language, "离线", "Offline") }, { value: "disabled", label: copy(language, "未启用", "Disabled") }]} size="sm" value={statusFilter} />
        <SelectControl aria-label={copy(language, "节点排序", "Sort nodes")} className="w-40" onValueChange={setSort} options={[{ value: "site", label: copy(language, "按位置排序", "Sort by location") }, { value: "status", label: copy(language, "异常优先", "Attention first") }, { value: "name", label: copy(language, "按名称排序", "Sort by name") }, { value: "last_seen", label: copy(language, "按最后在线排序", "Sort by last seen") }]} size="sm" value={sort} />
      </div>
      <div className="overflow-hidden rounded-xl border bg-card/40">
        <Table aria-label={copy(language, "节点列表", "Node list")} className="min-w-full table-fixed">
          <TableHeader>
            <TableRow>
              <TableHead className="w-[72%] pl-4 text-xs text-muted-foreground md:w-[27%]">{copy(language, "节点", "Node")}</TableHead>
              <TableHead className="hidden w-[16%] text-xs text-muted-foreground md:table-cell">{copy(language, "状态", "Status")}</TableHead>
              <TableHead className="hidden w-[18%] text-xs text-muted-foreground md:table-cell">{copy(language, "用途", "Purpose")}</TableHead>
              <TableHead className="hidden w-[20%] text-xs text-muted-foreground md:table-cell">{copy(language, "Agent 版本", "Agent version")}</TableHead>
              <TableHead className="w-[28%] pr-4 md:w-[19%]"><span className="sr-only">{copy(language, "操作", "Actions")}</span></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {visibleGroups.map(({ site, agents }) => <NodeSiteRows agents={agents} data={data} key={site.id} language={language} onApplications={() => onNavigate("apps")} onConfigure={setEditing} onNetwork={() => onNavigate("network")} onReconnect={setReconnecting} onRemove={setRemovingID} site={site} />)}
            {visibleAgents.length === 0 ? <TableRow><TableCell className="h-24 text-center text-muted-foreground" colSpan={5}>{copy(language, "没有符合当前筛选条件的节点", "No nodes match the current filters")}</TableCell></TableRow> : null}
          </TableBody>
        </Table>
      </div>
    </div>}
    <AddNodeSheet data={data} language={language} onClose={() => { setAdding(false); onAddFirstNodeHandled?.(); }} onJoined={() => { setAdding(false); onAddFirstNodeHandled?.(); onNavigate("network"); }} open={adding} />
    <NodeSettingsSheet agent={currentEditing} data={data} language={language} mutate={mutate} onNavigate={onNavigate} onClose={() => setEditing(null)} onStopAccess={() => { if (currentEditing) { setStoppingAccessID(currentEditing.id); setEditing(null); } }} />
    {stoppingAccessAgent ? <StopNodeAccessSheet agent={stoppingAccessAgent} key={stoppingAccessAgent.id} language={language} mutate={mutate} onClose={() => setStoppingAccessID(null)} /> : null}
    {removingAgent ? <RemoveNodeDialog agent={removingAgent} key={removingAgent.id} language={language} mutate={mutate} onClose={() => setRemovingID(null)} /> : null}
    {currentReconnecting ? <ReinstallNodeSheet agent={currentReconnecting} installerAvailable={data.status.agentInstallerAvailable} key={currentReconnecting.id} language={language} onClose={() => setReconnecting(null)} /> : null}
  </section>;
}

function FleetSummary({ agents, connected, attention, language }: { agents: number; connected: number; attention: number; language: Language }) {
  const items = [
    { label: copy(language, "台节点", "nodes"), value: agents },
    { label: copy(language, "台已连接", "connected"), value: connected },
    { label: copy(language, "台需要处理", attention === 1 ? "needs attention" : "need attention"), value: attention, tone: attention > 0 ? "text-amber-600 dark:text-amber-400" : undefined },
  ];
  return <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground">
    {items.map((item, index) => <span className="inline-flex items-baseline gap-1.5" key={item.label}>
      {index > 0 ? <span aria-hidden="true" className="mr-1 text-border">·</span> : null}
      <strong className={`font-medium tabular-nums ${item.tone ?? "text-foreground"}`}>{item.value}</strong>
      <span className={item.tone}>{item.label}</span>
    </span>)}
  </div>;
}

const siteRegionCodes: Record<string, string> = {
  "中国": "CN", "china": "CN",
  "台湾": "TW", "taiwan": "TW",
  "香港": "HK", "hong kong": "HK",
  "美国": "US", "美西": "US", "美东": "US", "united states": "US", "usa": "US",
  "德国": "DE", "germany": "DE",
  "日本": "JP", "japan": "JP",
};

function siteRegionCode(site: AppData["sites"][number]) {
  const [first = "", second = ""] = site.name.trim().split(/[-–—·｜|]/, 2).map((part) => part.trim().toLowerCase());
  return (first === "中国" || first === "china" ? siteRegionCodes[second] : undefined) ?? siteRegionCodes[first] ?? "";
}

function siteLabel(site: AppData["sites"][number]) {
  const flag = displayRegionFlag(siteRegionCode(site));
  return flag ? `${flag} ${site.name}` : site.name;
}

function NodeSiteRows({ agents, data, language, onApplications, onConfigure, onNetwork, onReconnect, onRemove, site }: { agents: AgentView[]; data: AppData; language: Language; onApplications: () => void; onConfigure: (agent: AgentView) => void; onNetwork: () => void; onReconnect: (agent: AgentView) => void; onRemove: (id: string) => void; site: AppData["sites"][number] }) {
  const regionCode = siteRegionCode(site);
  return <>
    <TableRow className="bg-muted/35 hover:bg-muted/35">
      <TableCell className="h-8 border-y border-border/60 px-4 py-1" colSpan={5}>
        <div className="flex items-center gap-2 text-xs">{regionCode ? <RegionFlag code={regionCode} language={language} /> : <MapPinIcon aria-hidden="true" className="size-3.5 text-muted-foreground" />}<span className="font-medium">{site.name}</span><span className="text-muted-foreground">· {copy(language, `${agents.length} 台节点`, `${agents.length} node${agents.length === 1 ? "" : "s"}`)}</span></div>
      </TableCell>
    </TableRow>
    {agents.map((agent) => <NodeTableRow agent={agent} data={data} key={agent.id} language={language} onApplications={onApplications} onConfigure={() => onConfigure(agent)} onNetwork={onNetwork} onReconnect={() => onReconnect(agent)} onRemove={() => onRemove(agent.id)} />)}
  </>;
}

function NodeTableRow({ agent, data, language, onApplications, onConfigure, onNetwork, onReconnect, onRemove }: { agent: AgentView; data: AppData; language: Language; onApplications: () => void; onConfigure: () => void; onNetwork: () => void; onReconnect: () => void; onRemove: () => void }) {
  const [configurationRecoveryOpen, setConfigurationRecoveryOpen] = useState(false);
  const site = data.sites.find((value) => value.id === agent.siteId);
  const selectedGateway = Boolean(site?.gatewayNodes.includes(agent.id));
  const architecture = agent.architecture === "arm64" ? "ARM64" : "x64";
  const needsNetworkConfirmation = agent.status === "active" && !agent.removal && !agent.reinstall && !agent.networkProfile;
  const canRecoverXray = !data.desktopDataUnavailable && !data.nodeAuxiliaryDataUnavailable && canInspectXrayConfiguration(data, agent.id);
  const needsLegacyConfigurationInspection = canRecoverXray && agent.connected && !agent.runtimeRecovery && data.meridian.endpoints.some((endpoint) => endpoint.nodeId === agent.id && !endpoint.legacyRetired && (endpoint.status === "applying" || endpoint.status === "failed"));
  return <>
    <TableRow className="h-14 hover:bg-muted/20">
      <TableCell className="min-w-0 py-2 pl-4"><div className="flex min-w-0 items-center gap-2.5"><ServerIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" /><div className="min-w-0"><p className="truncate text-sm font-medium" title={agent.name}>{agent.name}</p><div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground md:hidden"><NodeStatus agent={agent} language={language} compact /><span aria-hidden="true">·</span><span className="truncate">{agent.version || "—"}</span></div></div></div></TableCell>
      <TableCell className="hidden py-2 md:table-cell"><NodeStatus agent={agent} language={language} /></TableCell>
      <TableCell className="hidden py-2 md:table-cell"><div className="flex flex-wrap items-center gap-1.5">{agent.capabilities.landingOnly ? <Badge variant="secondary">{copy(language, "仅落地", "Landing only")}</Badge> : null}{selectedGateway ? <Badge className="rounded-md" variant="secondary">{copy(language, "网关", "Gateway")}</Badge> : null}{agent.appliedInstallations > 0 ? <Badge className="rounded-md" variant="secondary">{copy(language, `${agent.appliedInstallations} 个应用`, `${agent.appliedInstallations} app${agent.appliedInstallations === 1 ? "" : "s"}`)}</Badge> : null}{!agent.capabilities.landingOnly && !selectedGateway && agent.appliedInstallations === 0 ? <span className="text-muted-foreground">—</span> : null}</div></TableCell>
      <TableCell className="hidden py-2 md:table-cell"><span className="block truncate text-xs tabular-nums" title={agent.version}>{agent.version || "—"}</span><span className="text-xs text-muted-foreground">{architecture}</span></TableCell>
      <TableCell className="py-2 pr-4"><div className="flex flex-wrap items-center justify-end gap-1">
        {needsLegacyConfigurationInspection ? <Button aria-label={copy(language, `检查 ${agent.name} 的旧 Xray 配置差异`, `Inspect ${agent.name} legacy Xray configuration`)} onClick={() => setConfigurationRecoveryOpen(true)} size="icon-sm" title={copy(language, "检查旧 Xray 配置差异", "Inspect legacy Xray configuration")} variant="ghost"><GitCompareArrowsIcon aria-hidden="true" /></Button> : null}
        {needsNetworkConfirmation ? <Button aria-label={copy(language, `确认 ${agent.name} 的网络`, `Confirm network for ${agent.name}`)} onClick={onNetwork} size="icon-sm" title={copy(language, "确认网络", "Confirm network")} variant="ghost"><NetworkIcon aria-hidden="true" /></Button> : null}
        {!agent.removal && (!agent.connected || agent.reinstall) ? <Button aria-label={agent.reinstall ? copy(language, `查看 ${agent.name} 的恢复进度`, `View recovery for ${agent.name}`) : copy(language, `重新接入 ${agent.name}`, `Reconnect ${agent.name}`)} onClick={onReconnect} size="icon-sm" title={agent.reinstall ? copy(language, "恢复进度", "Recovery progress") : copy(language, "重新接入", "Reconnect")} variant="ghost"><RotateCcwIcon aria-hidden="true" /></Button> : null}
        {!agent.removal && agent.status === "disabled" ? <Button onClick={onConfigure} size="sm" variant="outline"><Trash2Icon data-icon="inline-start" />{copy(language, "删除", "Delete")}</Button> : null}
        {!agent.removal && agent.status === "active" ? <Button aria-label={copy(language, `管理 ${agent.name}`, `Manage ${agent.name}`)} onClick={onConfigure} size="icon-sm" variant="ghost"><Settings2Icon aria-hidden="true" /></Button> : null}
        {!agent.connected ? <Button aria-label={agent.removal ? copy(language, `查看 ${agent.name} 的移除进度`, `View removal progress for ${agent.name}`) : copy(language, `永久移除 ${agent.name}`, `Permanently remove ${agent.name}`)} onClick={onRemove} size="icon-sm" title={agent.removal ? copy(language, "查看移除进度", "View removal progress") : copy(language, "永久移除", "Permanently remove")} variant="ghost"><Trash2Icon aria-hidden="true" /></Button> : null}
      </div>{needsLegacyConfigurationInspection ? <XrayConfigurationRecoverySheet agent={agent} language={language} onOpenChange={setConfigurationRecoveryOpen} open={configurationRecoveryOpen} /> : null}</TableCell>
    </TableRow>
    {agent.connected && agent.runtimeRecovery ? <TableRow><TableCell className="py-2" colSpan={5}><RuntimeRecoveryAlert agent={agent} canRecoverXray={canRecoverXray} language={language} onApplications={onApplications} /></TableCell></TableRow> : null}
  </>;
}

function NodeStatus({ agent, language, compact = false }: { agent: AgentView; language: Language; compact?: boolean }) {
  const state = nodeState(agent);
  const connected = state === "connected";
  const label = state === "connected" ? copy(language, "已连接", "Connected")
    : state === "offline" ? copy(language, "离线", "Offline")
      : state === "disabled" ? copy(language, "未启用", "Disabled")
        : state === "access_stopped" ? copy(language, "已停止接入", "Access stopped")
          : state === "removal_failed" ? copy(language, "移除未完成", "Removal incomplete") : copy(language, "正在移除", "Removing");
  const issue = agent.reinstall ? copy(language, "重装恢复待处理", "Recovery needs attention") : connected && !agent.networkProfile ? copy(language, "网络待确认", "Network unconfirmed")
    : connected && agent.update?.state === "failed" ? copy(language, "更新需处理", "Update needs attention")
      : connected && agent.runtimeRecovery ? copy(language, "恢复中", "Recovering") : "";
  return <span className={`inline-flex min-w-0 ${compact ? "items-center gap-1" : "flex-col items-start gap-0.5"}`}>
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap"><span aria-hidden="true" className={`size-1.5 rounded-full ${connected ? "bg-emerald-500" : state === "offline" || state === "removal_failed" ? "bg-destructive" : "bg-amber-500"}`} /><span className={connected ? "text-foreground" : "text-muted-foreground"}>{label}</span></span>
    {issue ? <span className={`text-xs text-amber-600 dark:text-amber-400 ${compact ? "truncate" : ""}`}>{compact ? `· ${issue}` : issue}</span> : null}
  </span>;
}

function nodeState(agent: AgentView) {
  return agent.removal ? agent.removal.state === "failed" ? "removal_failed" : "removing" : agent.status === "disabled" ? "disabled" : agent.credentialRevoked ? "access_stopped" : agent.connected ? "connected" : "offline";
}

function nodeNeedsAttention(agent: AgentView) {
  return agent.status !== "active" || !agent.connected || agent.credentialRevoked || !agent.networkProfile || Boolean(agent.runtimeRecovery) || Boolean(agent.reinstall) || agent.removal?.state === "failed" || agent.update?.state === "failed";
}

function matchesNodeStatus(agent: AgentView, status: string) {
  if (status === "attention") return nodeNeedsAttention(agent);
  if (status === "connected") return agent.status === "active" && agent.connected && !agent.credentialRevoked;
  if (status === "offline") return agent.status === "active" && !agent.connected && !agent.credentialRevoked;
  if (status === "disabled") return agent.status === "disabled" || agent.credentialRevoked;
  return true;
}

function compareNodes(left: AgentView, right: AgentView, sort: string, siteByID: Map<string, AppData["sites"][number]>) {
  if (sort === "status") {
    const severity = Number(nodeNeedsAttention(right)) - Number(nodeNeedsAttention(left));
    if (severity !== 0) return severity;
  }
  if (sort === "last_seen") {
    const recency = Date.parse(right.lastSeenAt) - Date.parse(left.lastSeenAt);
    if (Number.isFinite(recency) && recency !== 0) return recency;
  }
  if (sort === "site") {
    const site = (siteByID.get(left.siteId)?.name ?? left.siteId).localeCompare(siteByID.get(right.siteId)?.name ?? right.siteId);
    if (site !== 0) return site;
  }
  return left.name.localeCompare(right.name);
}

function AddNodeSheet({ data, language, onClose, onJoined, open }: { data: AppData; language: Language; onClose: () => void; onJoined: () => void; open: boolean }) {
	const [landingOnly, setLandingOnly] = useState(false);
  const [name, setName] = useState("");
  const [siteID, setSiteID] = useState("");
  const [centerURL, setCenterURL] = useState("");
  const [gateway, setGateway] = useState(true);
  const [tunnel, setTunnel] = useState(false);
  const [caCertificate, setCACertificate] = useState("");
  const [useHeadscale, setUseHeadscale] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [enrollment, setEnrollment] = useState<(AgentEnrollment & { statusId: string }) | null>(null);
  const [enrollmentStatus, setEnrollmentStatus] = useState<AgentEnrollmentStatus | null>(null);
  const [statusError, setStatusError] = useState("");
  const initialized = useRef(false);
  const requestGeneration = useRef(0);
  const submitting = useRef(false);
  useEffect(() => () => { requestGeneration.current += 1; }, []);
  useEffect(() => {
    if (!open) { initialized.current = false; return; }
    if (initialized.current) return;
    initialized.current = true;
    setSiteID(data.sites[0]?.id ?? "");
    setCenterURL(data.status.agentConnectUrl);
    setUseHeadscale(data.status.agentConnectionMode === "headscale");
  }, [open, data.sites, data.status.agentConnectUrl, data.status.agentConnectionMode]);
  useEffect(() => {
    if (!open || !enrollment) return;
    const controller = new AbortController();
    const generation = requestGeneration.current;
    let timer = 0;
    const statusID = enrollment.statusId;
    const refresh = async () => {
      try {
        const next = await api.agentEnrollmentStatus(statusID, controller.signal);
        if (controller.signal.aborted || requestGeneration.current !== generation) return;
        setEnrollmentStatus(next); setStatusError("");
        if (next.state === "expired" || next.state === "unavailable") return;
      } catch (cause) {
        if (controller.signal.aborted || requestGeneration.current !== generation) return;
        // A read failure is not successful enrollment and never creates a token.
        setEnrollmentStatus(null); setStatusError(userError(language, cause));
      }
      if (!controller.signal.aborted && requestGeneration.current === generation) timer = window.setTimeout(() => void refresh(), 3000);
    };
    void refresh();
    return () => { controller.abort(); window.clearTimeout(timer); };
  }, [open, enrollment, language]);
  const headscaleReady = data.integrations.some((integration) => integration.kind === "headscale" && integration.status === "configured");
  const firstPrivateNode = data.agents.length === 0 && data.status.agentConnectionMode === "headscale" && useHeadscale && headscaleReady;
  const command = useMemo(() => {
    if (!enrollment) return "";
    return agentInstallCommand({ centerURL, enrollment, installerAvailable: data.status.agentInstallerAvailable });
  }, [enrollment, data.status.agentInstallerAvailable, centerURL]);
  const joinedAgent = enrollmentStatus?.state === "connected" && enrollmentStatus.connected
    ? data.agents.find((agent) => agent.id === enrollmentStatus.agentId && agent.status === "active" && agent.connected && !agent.credentialRevoked) : undefined;
  const close = () => { requestGeneration.current += 1; submitting.current = false; initialized.current = false; setLandingOnly(false); setName(""); setSiteID(""); setCenterURL(""); setGateway(true); setTunnel(false); setCACertificate(""); setEnrollment(null); setEnrollmentStatus(null); setStatusError(""); setUseHeadscale(false); setError(""); setBusy(false); onClose(); };
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!open || submitting.current || data.nodeAuxiliaryDataUnavailable) return;
    setError("");
    if (!validCenterURL(centerURL)) { setError(copy(language, "Center 必须使用 HTTPS；只有 127.0.0.1 或 localhost 可以使用 HTTP。", "Center must use HTTPS. Only 127.0.0.1 or localhost may use HTTP.")); return; }
    const current = ++requestGeneration.current;
    submitting.current = true;
    setBusy(true);
    try {
      const next = await api.createAgentEnrollment(siteID, name, centerURL, (landingOnly || useHeadscale) && headscaleReady, !landingOnly && gateway, !landingOnly && tunnel, centerURL.startsWith("https://") ? caCertificate : "", landingOnly);
      // A closed or replaced form must not inherit an earlier one-shot token.
      // Ignoring a response does not undo creation or automatically retry it.
      if (requestGeneration.current === current) { setEnrollmentStatus(null); setStatusError(""); setEnrollment(next); }
    } catch (submitError) {
      if (requestGeneration.current === current) setError(userError(language, submitError));
    } finally {
      if (requestGeneration.current === current) { submitting.current = false; setBusy(false); }
    }
  };
  return (
    <Sheet onOpenChange={(next) => { if (!next) close(); }} open={open}>
      <SheetContent className="sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{copy(language, "添加节点", "Add node")}</SheetTitle>
          <SheetDescription>{enrollment ? copy(language, "在要作为节点的 Linux 设备运行一次下面的命令；它可以就是当前 Center 主机。", "Run the command once on the Linux device that will become the node. It can be this Center host.") : copy(language, "填写名称和位置即可。当前 Center 主机也可以同时作为应用节点。", "Enter a name and location. This Center host can also serve as an app node.")}</SheetDescription>
        </SheetHeader>
        {data.nodeAuxiliaryDataUnavailable ? <FieldError role="alert">{copy(language, "辅助配置暂不可用，请待状态刷新后再生成接入命令。", "Auxiliary configuration is unavailable. Wait for a successful refresh before generating a join command.")}</FieldError> : null}
        {enrollment ? <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4">
          <Alert><TerminalIcon /><AlertTitle>{copy(language, "在目标设备运行一次", "Run once on the target device")}</AlertTitle><AlertDescription>{data.status.agentInstallerAvailable ? <><p>{copy(language, "支持 Debian 12/13 或 Ubuntu 22.04/24.04/26.04（x64/ARM64），需要 systemd 和 curl。", "Supports Debian 12/13 or Ubuntu 22.04/24.04/26.04 (x64/ARM64) with systemd and curl.")}</p><p>{copy(language, landingOnly ? "脚本只安装 Agent 和 Tailscale；原生 systemd 应用需在备份和权限审核后另行安装，不安装或启动 Docker。" : useHeadscale ? "脚本会按需安装 Docker 和 Tailscale、加入安全私网并安装 Agent。" : "脚本会先检查 Docker，缺少时自动安装，再安装 Agent。", landingOnly ? "Installs Agent and Tailscale only. Native systemd apps require a separate reviewed installation after backup. Docker is never installed or started." : useHeadscale ? "The script installs Docker and Tailscale when needed, joins the secure private network, and installs Agent." : "The script checks Docker first, installs it when missing, and then installs Agent.")}</p></> : copy(language, "当前 Center 没有内置 Agent 文件，请先把 vastora 放到 /usr/local/bin/vastora。", "This Center does not include Agent binaries. Put vastora at /usr/local/bin/vastora first.")}</AlertDescription></Alert>
          <div className="relative"><code className="block max-h-56 overflow-auto break-all rounded-xl bg-muted p-4 pr-14 text-xs leading-6">{command}</code><CopyButton className="absolute right-2 top-2" label={copy(language, "复制命令", "Copy command")} language={language} size="icon" value={command} /></div>
          <div aria-live="polite" className="flex items-start gap-3 rounded-xl border p-4">{joinedAgent ? <CheckCircle2Icon className="mt-0.5 text-success" /> : enrollmentStatus?.state !== "expired" && enrollmentStatus?.state !== "unavailable" ? <Spinner className="mt-0.5" /> : null}<div><p className="text-sm font-medium">{joinedAgent ? copy(language, `${joinedAgent.name} 已上线`, `${joinedAgent.name} is online`) : enrollmentStatus?.state === "expired" ? copy(language, "接入命令已过期", "Join command expired") : enrollmentStatus?.state === "unavailable" ? copy(language, "本次接入记录不可用", "Enrollment record unavailable") : copy(language, "正在等待本次节点上线…", "Waiting for this enrolled node to come online…")}</p><p className="mt-1 text-xs text-muted-foreground">{joinedAgent ? copy(language, "下一步确认 Agent 自动发现的网络地址。", "Next, confirm the network addresses discovered by the Agent.") : enrollmentStatus?.state === "enrolled" ? copy(language, "本次注册已完成，等待该节点的实时心跳；其他同名节点不会作为成功依据。", "Registration completed; waiting for this node’s live heartbeat, not another node with the same name.") : enrollmentStatus?.state === "unavailable" ? copy(language, "请先核对节点列表中的实际设备，不会自动重新注册。", "Inspect the actual device in the node list. Registration will not retry automatically.") : copy(language, `命令将在 ${formatDate(language, enrollment.expiresAt)} 失效。`, `The command expires at ${formatDate(language, enrollment.expiresAt)}.`)}</p></div></div>
          {statusError ? <FieldError role="alert">{statusError}</FieldError> : null}
          <Alert><CheckCircle2Icon /><AlertTitle>{copy(language, "凭据仅显示这一次", "Credential is shown only once")}</AlertTitle><AlertDescription>{copy(language, "令牌十分钟后失效且只能使用一次。关闭后如未执行，请重新生成。", "The token expires in ten minutes and works only once. Generate a new one if you close before running it.")}</AlertDescription></Alert>
        </div> : (
          <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}>
            <div className="flex-1 overflow-y-auto px-4">
              <FieldGroup>
                <Field data-disabled={!headscaleReady} orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="new-node-landing-only">{copy(language, "仅落地节点（不安装 Docker）", "Landing-only node (no Docker)")}</FieldLabel><FieldDescription>{copy(language, headscaleReady ? "允许审核授权的原生 systemd 应用；不支持 Docker 应用、网关或 Tunnel。不会卸载已有 Docker。" : "请先配置 Headscale 安全私网，再接入仅落地节点。", headscaleReady ? "Permits reviewed native systemd apps only, without Docker apps, gateways or tunnels. Existing Docker is not removed." : "Configure the Headscale private network before adding a landing-only node.")}</FieldDescription></div><Switch checked={landingOnly} disabled={!headscaleReady} id="new-node-landing-only" onCheckedChange={setLandingOnly} /></Field>
                {firstPrivateNode ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "先让当前 Center 主机加入私网", "Join this Center host first")}</AlertTitle><AlertDescription>{copy(language, "请在安装 Center 的这台服务器运行生成的命令。完成网络确认后，其他节点就能通过私网地址连接。", "Run the generated command on the server hosting Center. After confirming its network, other nodes can connect through the private address.")}</AlertDescription></Alert> : null}
                <Field><FieldLabel htmlFor="new-node-name">{copy(language, "节点名称", "Node name")}</FieldLabel><Input autoFocus id="new-node-name" maxLength={128} onChange={(event) => setName(event.target.value)} placeholder={copy(language, "例如：新加坡服务器", "For example: Singapore server")} required value={name} /><FieldDescription>{copy(language, "使用容易识别设备或位置的名称。", "Use a name that identifies the device or location.")}</FieldDescription></Field>
                <Field><FieldLabel htmlFor="new-node-site">{copy(language, "位置", "Location")}</FieldLabel><SelectControl id="new-node-site" onValueChange={setSiteID} options={data.sites.map((site) => ({ value: site.id, label: site.name }))} required value={siteID} /></Field>
                <div className="rounded-xl border bg-muted/25 p-4"><div className="flex items-center justify-between gap-3"><div><p className="text-sm font-medium">{copy(language, "Agent 将连接 Center", "Agent will connect to Center")}</p><p className="mt-1 text-xs text-muted-foreground">{data.status.agentConnectionMode === "headscale" ? copy(language, "使用安全私网", "Using the secure private network") : data.status.agentConnectionMode === "public" ? copy(language, "使用公网安全连接", "Using a secure public connection") : copy(language, "使用同一局域网", "Using the same local network")}</p></div><Badge variant="secondary">{copy(language, "已自动配置", "Automatic")}</Badge></div></div>
                <details className="rounded-xl border p-3">
                  <summary className="cursor-pointer text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}</summary>
                  <div className="mt-4 flex flex-col gap-4">
                    <Field><FieldLabel htmlFor="new-node-center">{copy(language, "Center 地址", "Center address")}</FieldLabel><Input id="new-node-center" onChange={(event) => setCenterURL(event.target.value)} placeholder="https://center.example.com" required type="url" value={centerURL} /><FieldDescription>{copy(language, "仅当此节点需要使用不同的连接地址时修改。", "Change only when this node needs a different connection address.")}</FieldDescription></Field>
                    <Field><FieldLabel htmlFor="new-node-ca">{copy(language, "私有 CA 证书（可选）", "Private CA certificate (optional)")}</FieldLabel><Textarea id="new-node-ca" onChange={(event) => setCACertificate(event.target.value)} placeholder="-----BEGIN CERTIFICATE-----" value={caCertificate} /><FieldDescription>{copy(language, "仅当 Center 使用系统不信任的私有证书时填写根 CA；它会在令牌首次发送前建立信任。", "Provide the root CA only when Center uses a private certificate not trusted by the system. It is applied before the token is first sent.")}</FieldDescription></Field>
                    {headscaleReady && !landingOnly ? <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="new-node-headscale">{copy(language, "先加入安全私网", "Join the secure private network first")}</FieldLabel><FieldDescription>{copy(language, "目标节点无法直接访问 Center 时开启；脚本会自动安装 Tailscale。", "Enable when the target cannot reach Center directly. The script installs Tailscale automatically.")}</FieldDescription></div><Switch checked={useHeadscale} id="new-node-headscale" onCheckedChange={setUseHeadscale} /></Field> : null}
                    {!landingOnly ? <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="new-node-gateway">{copy(language, "可提供服务入口", "Can provide service access")}</FieldLabel><FieldDescription>{copy(language, "推荐开启；只有实际使用时才会安装网关组件。", "Recommended. Gateway components are installed only when used.")}</FieldDescription></div><Switch checked={gateway} id="new-node-gateway" onCheckedChange={setGateway} /></Field> : null}
                    {!landingOnly ? <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="new-node-tunnel">Cloudflare Tunnel</FieldLabel><FieldDescription>{copy(language, "允许以后通过该节点发布网页；现在不会安装。", "Allows this node to publish websites later. Nothing is installed now.")}</FieldDescription></div><Switch checked={tunnel} id="new-node-tunnel" onCheckedChange={setTunnel} /></Field> : null}
                  </div>
                </details>
                {error ? <FieldError role="alert">{error}</FieldError> : null}
              </FieldGroup>
            </div>
            <SheetFooter><Button onClick={close} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || data.nodeAuxiliaryDataUnavailable || !name || !siteID || !centerURL || (landingOnly && !headscaleReady)} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "生成接入命令", "Generate join command")}</Button></SheetFooter>
          </form>
        )}
        {enrollment ? <SheetFooter><Button onClick={joinedAgent ? onJoined : close}>{joinedAgent ? <><NetworkIcon data-icon="inline-start" />{copy(language, "继续确认网络", "Continue to network setup")}</> : copy(language, "完成", "Done")}</Button></SheetFooter> : null}
      </SheetContent>
    </Sheet>
  );
}

function NodeSettingsSheet({ agent, data, language, mutate, onClose, onStopAccess, onNavigate }: { agent: AgentView | null; data: AppData; language: Language; mutate: Mutate; onClose: () => void; onStopAccess: () => void; onNavigate: (screen: Screen) => void }) {
  const navigating = useRef(false);
  const [name, setName] = useState("");
  const [siteID, setSiteID] = useState("");
  const [gateway, setGateway] = useState(false);
  const [tunnel, setTunnel] = useState(false);
  const [commandKind, setCommandKind] = useState<"purpose" | null>(null);
  const [busy, setBusy] = useState(false);
  const [updateBusy, setUpdateBusy] = useState(false);
  const [recoveringUpdateID, setRecoveringUpdateID] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [danger, setDanger] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const deleting = agent?.status === "disabled";
  const canStopAccess = agent?.status === "active" && !agent.connected;
  const dependenciesUnavailable = Boolean(data.desktopDataUnavailable || data.nodeAuxiliaryDataUnavailable);
  useEffect(() => {
    if (!agent) return;
    navigating.current = false;
    setName(agent.name);
    setSiteID(agent.siteId);
    setGateway(agent.capabilities.gateway);
    setTunnel(agent.capabilities.tunnel);
    setCommandKind(null);
    setError("");
    setDanger(agent.status === "disabled");
    setConfirmation("");
    setRecoveringUpdateID(null);
  }, [agent?.id]);
  const gatewayRequired = Boolean(agent && (data.sites.some((site) => site.gatewayNodes.includes(agent.id)) || data.publications.some((publication) => publication.ingress.owner === "site_gateway" && publication.ingress.entryNodeId === agent.id && publication.status !== "stopped")));
  const tunnelRequired = Boolean(agent && data.publications.some((publication) => publication.ingress.owner === "tunnel_connector" && publication.ingress.entryNodeId === agent.id && publication.status !== "stopped"));
  const purposeChanged = Boolean(agent && (gateway !== agent.capabilities.gateway || tunnel !== agent.capabilities.tunnel));
  const roles = gateway ? "worker,gateway" : "worker";
  const capabilities = ["docker", gateway ? "gateway" : "", tunnel ? "tunnel" : ""].filter(Boolean).join(",");
  const purposeCommand = `sudo /usr/local/bin/vastora agent configure --data-dir /var/lib/vastora/agent --roles ${shellQuote(roles)} --capabilities ${shellQuote(capabilities)}`;
  const manualUpdateCommand = "sudo /usr/local/bin/vastora agent update --data-dir /var/lib/vastora/agent";
  const updateActive = agent?.update?.state === "pending" || agent?.update?.state === "running" || agent?.update?.state === "installing";
  const updateRequired = Boolean(agent && agent.version !== data.status.version);
  const updateFailed = agent?.update?.state === "failed";
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
    if (!agent || busy || confirmation.trim() !== agent.name.trim()) return;
    setBusy(true); setError("");
    try {
      await mutate(() => deleting ? api.deleteAgent(agent.id) : api.disableAgent(agent.id), deleting ? copy(language, "节点已删除，服务器上的数据未改动。", "Node deleted. Server data was not changed.") : copy(language, "节点已停用，原凭据已失效。", "Node disabled and its credential is no longer accepted."));
      onClose();
    } catch (disableError) {
      setError(userError(language, disableError));
    } finally {
      setBusy(false);
    }
  };
  const startUpdate = async () => {
    if (!agent || updateBusy || updateActive || !agent.connected) return;
    const failedUpdateId = agent.update?.state === "failed" ? agent.update.id : undefined;
    if (failedUpdateId) {
      setRecoveringUpdateID(failedUpdateId);
      return;
    }
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
    <Sheet onOpenChange={(next) => { if (!next && !recoveringUpdateID) onClose(); }} open={Boolean(agent)}>
      <SheetContent className="sm:max-w-xl" finalFocus={() => !navigating.current}>
        <SheetHeader>
          <SheetTitle>{copy(language, `管理 ${agent?.name ?? ""}`, `Manage ${agent?.name ?? ""}`)}</SheetTitle>
          <SheetDescription>{deleting ? copy(language, "从 Center 移除这个已停用的节点。", "Remove this disabled node from Center.") : danger ? copy(language, "停用会立即拒绝此 Agent 的后续连接。", "Disabling immediately rejects future connections from this Agent.") : copy(language, "修改名称、位置或节点用途；支持的 Agent 可以由 Center 安全更新。", "Change its name, location, or purpose. Supported Agents can update securely through Center.")}</SheetDescription>
        </SheetHeader>
        {danger ? <div className="flex flex-1 flex-col gap-4 px-4">
          <Alert variant="destructive"><Trash2Icon /><AlertTitle>{deleting ? copy(language, "确认删除节点", "Confirm node deletion") : copy(language, "确认停用节点", "Confirm node disable")}</AlertTitle><AlertDescription>{deleting ? copy(language, "删除后无法撤销，服务器上的程序和数据不会被删除。仍被应用或入口使用的节点不能删除。", "Deletion cannot be undone. Programs and data on the server are not deleted. Nodes still used by apps or access points cannot be deleted.") : copy(language, "Center 会撤销管理权限，但不会远程删除节点上的二进制或数据。", "Center revokes management access but does not remotely delete the binary or local data.")}</AlertDescription></Alert>
          <Field><FieldLabel htmlFor="disable-node-confirmation">{copy(language, `输入“${agent?.name ?? ""}”确认`, `Type “${agent?.name ?? ""}” to confirm`)}</FieldLabel><Input autoFocus id="disable-node-confirmation" onChange={(event) => setConfirmation(event.target.value)} value={confirmation} /></Field>
          {error ? <FieldError>{error}</FieldError> : null}
        </div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}>
          <div className="flex-1 overflow-y-auto px-4"><FieldGroup>
            {dependenciesUnavailable ? <FieldDescription role="status">{copy(language, "辅助数据暂不可用，不能确认服务依赖。用途修改和相关恢复操作暂不可用；仍可修改名称和位置。", "Auxiliary data is unavailable, so service dependencies cannot be verified. Purpose changes and related recovery are unavailable; name and location can still be edited.")}</FieldDescription> : null}
            {agent?.credentialRevoked ? <Alert><UnplugIcon aria-hidden="true" /><AlertTitle>{copy(language, "已停止接入", "Access stopped")}</AlertTitle><AlertDescription>{copy(language, "旧 Agent 无法再连接 Center，应用和数据仍保留。需要恢复管理时，请使用“重新接入”。", "The old Agent cannot connect to Center. Apps and data are kept. Use Reconnect to restore management.")}</AlertDescription></Alert> : null}
            <Field><FieldLabel htmlFor="node-name">{copy(language, "名称", "Name")}</FieldLabel><Input id="node-name" maxLength={128} onChange={(event) => setName(event.target.value)} required value={name} /></Field>
            <Field><FieldLabel htmlFor="node-site"><MapPinIcon data-icon="inline-start" />{copy(language, "位置", "Location")}</FieldLabel><SelectControl id="node-site" onValueChange={setSiteID} options={data.sites.map((site) => ({ value: site.id, label: site.name }))} value={siteID} /></Field>
            <div className="rounded-xl border p-4">
              <div className="mb-3 flex items-start justify-between gap-3"><div><p className="text-sm font-medium">{copy(language, "节点用途", "Node purpose")}</p><p className="mt-1 text-xs leading-5 text-muted-foreground">{copy(language, agent?.capabilities.landingOnly ? "仅落地：安全私网和 Agent，可安装审核授权的原生 systemd 应用。不启用 Docker、网关或 Tunnel；不能在此转换用途。" : "Docker 应用始终可用；按需启用访问网关和 Cloudflare Tunnel。", agent?.capabilities.landingOnly ? "Landing-only: private networking, Agent and reviewed native systemd apps. Docker, gateways, tunnels and purpose conversion remain disabled." : "Docker apps remain available; enable Gateway and Cloudflare Tunnel only when needed.")}</p></div><Badge variant="secondary">{agent?.capabilities.landingOnly ? copy(language, "仅落地", "Landing only") : "Docker"}</Badge></div>
              <div className="flex flex-col gap-3">
                <Field data-disabled={dependenciesUnavailable || gatewayRequired || agent?.capabilities.landingOnly} orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="manage-node-gateway">Gateway</FieldLabel><FieldDescription>{gatewayRequired ? copy(language, "此节点仍被位置或访问入口使用，请先移除依赖。", "This node is still used by a location or access point. Remove those dependencies first.") : copy(language, "为局域网、Headscale 或公网 Web 服务提供访问地址。", "Provides access addresses for LAN, Headscale, or public Web services.")}</FieldDescription></div><Switch checked={gateway} disabled={dependenciesUnavailable || Boolean(agent?.capabilities.landingOnly) || gatewayRequired && gateway} id="manage-node-gateway" onCheckedChange={(checked) => { setGateway(checked); setCommandKind(null); }} /></Field>
                <Field data-disabled={dependenciesUnavailable || tunnelRequired || agent?.capabilities.landingOnly} orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="manage-node-tunnel">Cloudflare Tunnel</FieldLabel><FieldDescription>{tunnelRequired ? copy(language, "此节点仍承载 Tunnel 入口，请先停止相关入口。", "This node still hosts Tunnel access points. Stop them first.") : copy(language, "仅在需要通过 Cloudflare 发布 Web 服务时启用。", "Enable only when this node will publish Web services through Cloudflare.")}</FieldDescription></div><Switch checked={tunnel} disabled={dependenciesUnavailable || Boolean(agent?.capabilities.landingOnly) || tunnelRequired && tunnel} id="manage-node-tunnel" onCheckedChange={(checked) => { setTunnel(checked); setCommandKind(null); }} /></Field>
              </div>
              <Button className="mt-3" disabled={dependenciesUnavailable || !purposeChanged || agent?.capabilities.landingOnly} onClick={() => setCommandKind("purpose")} size="sm" type="button" variant="outline"><TerminalIcon data-icon="inline-start" />{copy(language, "生成修改命令", "Generate change command")}</Button>
            </div>
            <div className="rounded-xl border p-4"><div className="flex items-start justify-between gap-3"><div><p className="text-sm font-medium">Agent</p><p className="mt-1 text-xs text-muted-foreground">{copy(language, `节点版本 ${agent?.version ?? "—"} · Center 版本 ${data.status.version}`, `Node ${agent?.version ?? "—"} · Center ${data.status.version}`)}</p></div><StateBadge value={updateActive ? "applying" : updateFailed ? "failed" : !updateRequired ? "ready" : "pending"} /></div>{agent?.update?.lastError ? <FieldError className="mt-2">{agent.update.lastError}</FieldError> : null}{updateFailed ? <Button className="mt-3" type="button" size="sm" variant="outline" onClick={() => { navigating.current = true; onClose(); onNavigate("activity"); }}>{copy(language, "核对更新任务", "Review update task")}</Button> : null}{(updateRequired || updateActive) && agent?.remoteUpdateSupported ? <><Button className="mt-3" disabled={!agent.connected || updateActive || updateBusy} onClick={() => void startUpdate()} size="sm" type="button" variant="outline">{updateBusy || updateActive ? <Spinner data-icon="inline-start" /> : agent.update?.state === "failed" ? <RotateCcwIcon data-icon="inline-start" /> : <CircleArrowUpIcon data-icon="inline-start" />}{updateActive ? copy(language, "正在更新", "Updating") : agent.update?.state === "failed" ? copy(language, "重试更新", "Retry update") : copy(language, "通过 Center 更新", "Update through Center")}</Button>{!agent.connected ? <p className="mt-2 text-xs text-muted-foreground">{copy(language, "节点重新上线后才能开始更新。", "The node must reconnect before updating.")}</p> : null}{updateActive ? <p className="mt-2 text-xs text-muted-foreground">{copy(language, `正在安全更新到 ${agent.update?.targetVersion}；节点会短暂离线并自动重新连接。`, `Safely updating to ${agent.update?.targetVersion}. The node briefly disconnects and reconnects automatically.`)}</p> : null}</> : null}{updateRequired && !agent?.remoteUpdateSupported ? <Alert className="mt-3"><TerminalIcon /><AlertTitle>{copy(language, "需要一次手动更新", "One manual update required")}</AlertTitle><AlertDescription><p>{copy(language, "当前版本还不支持 Center 远程更新。完成这一次后，后续版本可直接在这里更新。", "This version predates Center-managed updates. After this one-time step, future updates can run here.")}</p><div className="relative mt-3"><code className="block break-all rounded-lg bg-muted p-3 pr-12 text-xs leading-5">{manualUpdateCommand}</code><CopyButton className="absolute right-1.5 top-1.5" label={copy(language, "复制命令", "Copy command")} language={language} size="icon-sm" value={manualUpdateCommand} /></div></AlertDescription></Alert> : null}</div>
            {agent ? <NodeSSHProtection key={`ssh-${agent.id}`} nodeId={agent.id} connected={agent.connected} applications={data.applications} language={language} /> : null}
            {agent?.capabilities.nodeSecurity ? <NodeSecurityControls key={`security-${agent.id}`} nodeId={agent.id} language={language} /> : null}
            {commandKind && !dependenciesUnavailable ? <Alert><TerminalIcon /><AlertTitle>{copy(language, "在节点运行一次", "Run once on the node")}</AlertTitle><AlertDescription><p>{copy(language, "命令会更新 systemd 配置并重启 Agent，Center 会自动看到新用途。", "The command updates systemd, restarts Agent, and Center detects the new purpose automatically.")}</p><div className="relative mt-3"><code className="block break-all rounded-lg bg-muted p-3 pr-12 text-xs leading-5">{purposeCommand}</code><CopyButton className="absolute right-1.5 top-1.5" label={copy(language, "复制命令", "Copy command")} language={language} size="icon-sm" value={purposeCommand} /></div></AlertDescription></Alert> : null}
            {error ? <FieldError>{error}</FieldError> : null}
          </FieldGroup></div>
          <SheetFooter className="flex-wrap justify-between"><div className="flex gap-2">{canStopAccess ? <Button disabled={busy} onClick={onStopAccess} type="button" variant="outline"><UnplugIcon aria-hidden="true" data-icon="inline-start" />{copy(language, "停止接入", "Stop access")}</Button> : null}<Button disabled={busy} onClick={() => setDanger(true)} type="button" variant="ghost"><Trash2Icon data-icon="inline-start" />{copy(language, "停用节点", "Disable node")}</Button></div><div className="flex gap-2"><Button onClick={onClose} type="button" variant="outline">{copy(language, "关闭", "Close")}</Button><Button disabled={busy || !name || name === agent?.name && siteID === agent?.siteId} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "保存信息", "Save details")}</Button></div></SheetFooter>
        </form>}
        {danger ? <SheetFooter><Button disabled={busy} onClick={() => { if (deleting) onClose(); else { setDanger(false); setError(""); } }} variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !agent || confirmation.trim() !== agent.name.trim()} onClick={() => void disable()} variant="destructive">{busy ? <Spinner data-icon="inline-start" /> : null}{deleting ? copy(language, "删除节点", "Delete node") : copy(language, "停用节点", "Disable node")}</Button></SheetFooter> : null}
      </SheetContent>
      {agent && recoveringUpdateID ? <AgentUpdateRecoveryDialog agent={agent} failedUpdateId={recoveringUpdateID} key={recoveringUpdateID} language={language} mutate={mutate} onClose={() => setRecoveringUpdateID(null)} targetVersion={data.status.version} /> : null}
    </Sheet>
  );
}
